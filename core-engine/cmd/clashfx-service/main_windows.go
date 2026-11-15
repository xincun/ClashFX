//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/clashfx/core-engine/internal/service"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

type clashFXWinService struct {
	runner *service.Runner
}

func (s *clashFXWinService) Execute(_ []string, requests <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown
	changes <- svc.Status{State: svc.StartPending}

	ln, err := service.ListenSecurePipe()
	if err != nil {
		return false, 1
	}
	defer ln.Close()
	defer func() { _ = s.runner.Stop() }()

	go acceptLoop(ln, s.runner)
	changes <- svc.Status{State: svc.Running, Accepts: accepted}

	for req := range requests {
		switch req.Cmd {
		case svc.Interrogate:
			changes <- req.CurrentStatus
		case svc.Stop, svc.Shutdown:
			changes <- svc.Status{State: svc.StopPending}
			_ = ln.Close()
			_ = s.runner.Stop()
			return false, 0
		}
	}
	return false, 0
}

func acceptLoop(ln net.Listener, runner *service.Runner) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		go func(c net.Conn) {
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(20 * time.Second))
			if err := service.VerifyPipePeer(c, runner.TrustedDir()); err != nil {
				_ = json.NewEncoder(c).Encode(service.Response{OK: false, Message: err.Error()})
				return
			}
			var req service.Request
			if err := json.NewDecoder(c).Decode(&req); err != nil {
				_ = json.NewEncoder(c).Encode(service.Response{OK: false, Message: err.Error()})
				return
			}
			if req.Action == "upgrade_core" {
				_ = c.SetDeadline(time.Now().Add(125 * time.Second))
			}
			resp := runner.Handle(req)
			_ = json.NewEncoder(c).Encode(resp)
		}(conn)
	}
}

var windowsFirewallBinaries = []string{
	"mihomo_core.exe",
	"clashfx-daemon.exe",
	"clashfx-service.exe",
	"clashfx-ui.exe",
}

func netshExePath() string {
	if sysRoot := os.Getenv("SystemRoot"); sysRoot != "" {
		cand := filepath.Join(sysRoot, "System32", "netsh.exe")
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	return "netsh.exe"
}

func runNetshHidden(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, netshExePath(), args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
	return cmd.Run()
}

func ensureWindowsFirewallRules(trustedDir string) {
	for _, binName := range windowsFirewallBinaries {
		prog := filepath.Join(trustedDir, binName)
		if _, err := os.Stat(prog); err != nil {
			continue
		}
		baseName := "ClashFX - " + binName
		for _, suffix := range []string{"", " (In)", " (Out)"} {
			_ = runNetshHidden("advfirewall", "firewall", "delete", "rule", "name="+baseName+suffix)
		}
		for _, spec := range []struct {
			suffix string
			dir    string
		}{
			{" (In)", "in"},
			{" (Out)", "out"},
		} {
			if err := runNetshHidden(
				"advfirewall", "firewall", "add", "rule",
				"name="+baseName+spec.suffix,
				"dir="+spec.dir,
				"action=allow",
				"program="+prog,
				"enable=yes",
				"profile=any",
			); err != nil {
				fmt.Fprintf(os.Stderr, "[clashfx-service] firewall add rule %s%s warning: %v\n", baseName, spec.suffix, err)
			}
		}
	}
}

func removeWindowsFirewallRules() {
	for _, binName := range windowsFirewallBinaries {
		baseName := "ClashFX - " + binName
		for _, suffix := range []string{"", " (In)", " (Out)"} {
			_ = runNetshHidden("advfirewall", "firewall", "delete", "rule", "name="+baseName+suffix)
		}
	}
}

func resolveTrustedDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Dir(exe), nil
}

func installAndStartService() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect SCM (run as Administrator): %w", err)
	}
	defer m.Disconnect()

	cfg := mgr.Config{
		StartType:   mgr.StartAutomatic,
		DisplayName: "ClashFX Privileged Tunnel Service",
		Description: "Manages ClashFX Mihomo TUN adapter lifecycle without daily UAC prompts.",
	}
	s, err := m.OpenService(service.ServiceName)
	if err == nil {
		defer s.Close()
		stopAndWait(s, 8*time.Second)
		if existingCfg, cfgErr := s.Config(); cfgErr == nil {
			cfg = existingCfg
		}
		cfg.StartType = mgr.StartAutomatic
		cfg.BinaryPathName = exe
		cfg.DisplayName = "ClashFX Privileged Tunnel Service"
		cfg.Description = "Manages ClashFX Mihomo TUN adapter lifecycle without daily UAC prompts."
		if err := s.UpdateConfig(cfg); err != nil {
			return fmt.Errorf("update service config: %w", err)
		}
	} else {
		s, err = m.CreateService(service.ServiceName, exe, cfg)
		if err != nil {
			return fmt.Errorf("create service: %w", err)
		}
		defer s.Close()
		_ = s.SetRecoveryActions([]mgr.RecoveryAction{
			{Type: mgr.ServiceRestart, Delay: 3 * time.Second},
			{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
			{Type: mgr.NoAction},
		}, 86400)
	}
	ensureWindowsFirewallRules(filepath.Dir(exe))
	if err := s.Start(); err != nil {
		st, qErr := s.Query()
		if qErr != nil || (st.State != svc.Running && st.State != svc.StartPending) {
			return fmt.Errorf("start service: %w", err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st, qErr := s.Query()
		if qErr == nil && st.State == svc.Running {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}

func uninstallService() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, _ = service.Call(ctx, service.Request{Action: "stop_core"})
	cancel()

	removeWindowsFirewallRules()

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect SCM (run as Administrator): %w", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(service.ServiceName)
	if err != nil {
		return nil // Already uninstalled
	}
	defer s.Close()
	stopAndWait(s, 8*time.Second)
	return s.Delete()
}

func controlService(start bool) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(service.ServiceName)
	if err != nil {
		return err
	}
	defer s.Close()
	if start {
		return s.Start()
	}
	stopAndWait(s, 8*time.Second)
	return nil
}

func stopAndWait(s *mgr.Service, timeout time.Duration) {
	_, _ = s.Control(svc.Stop)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st, err := s.Query()
		if err != nil || st.State == svc.Stopped {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
}

func main() {
	isSvc, err := svc.IsWindowsService()
	if err == nil && isSvc {
		dir, err := resolveTrustedDir()
		if err != nil {
			os.Exit(1)
		}
		if err := svc.Run(service.ServiceName, &clashFXWinService{runner: service.NewRunner(dir)}); err != nil {
			os.Exit(1)
		}
		return
	}

	cmd := "status"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "install":
		err = installAndStartService()
	case "uninstall":
		err = uninstallService()
	case "start":
		err = controlService(true)
	case "stop":
		err = controlService(false)
	case "status":
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var resp service.Response
		resp, err = service.Call(ctx, service.Request{Action: "status"})
		if err == nil {
			_ = json.NewEncoder(os.Stdout).Encode(resp)
		}
	case "console":
		var dir string
		dir, err = resolveTrustedDir()
		if err == nil {
			var ln net.Listener
			ln, err = service.ListenSecurePipe()
			if err == nil {
				runner := service.NewRunner(dir)
				go acceptLoop(ln, runner)
				sigCh := make(chan os.Signal, 1)
				signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
				<-sigCh
				_ = ln.Close()
				_ = runner.Stop()
			}
		}
	case "version":
		fmt.Println("clashfx-service 1.0")
	default:
		err = fmt.Errorf("usage: %s [install|uninstall|start|stop|status|console|version]", filepath.Base(os.Args[0]))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
