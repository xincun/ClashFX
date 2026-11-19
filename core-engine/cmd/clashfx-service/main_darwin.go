//go:build darwin

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/clashfx/core-engine/internal/service"
	"github.com/clashfx/core-engine/internal/sysproxy"
)

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

func runDaemon() error {
	_, _ = sysproxy.CleanLegacyTunDNS()
	dir, err := resolveTrustedDir()
	if err != nil {
		return err
	}
	ln, err := service.ListenSecurePipe()
	if err != nil {
		return err
	}
	sockPath := service.ServiceSocketPath()
	sockID := service.StatSocketIdentity(sockPath)
	runner := service.NewRunner(dir)
	runner.InitPrivilegedService("")
	go acceptLoop(ln, runner)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	_ = ln.Close()
	_ = runner.ShutdownService()
	service.RemoveSocketIfMatches(sockPath, sockID)
	return nil
}

func copyRootOwnedExecutable(srcPath, dstPath string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", srcPath, err)
	}
	defer src.Close()

	dstDir := filepath.Dir(dstPath)
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dstDir, ".cfx-bin-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := io.Copy(tmp, src); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0755); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	_ = os.Chown(tmpPath, 0, 0)
	if err := os.Rename(tmpPath, dstPath); err != nil {
		return err
	}
	cleanup = false
	_ = exec.Command("/usr/bin/xattr", "-d", "com.apple.quarantine", dstPath).Run()
	return nil
}

func buildLaunchDaemonPlist() string {
	binPath := filepath.Join(service.DarwinPrivilegedBinDir, "clashfx-service")
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>daemon</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardOutPath</key>
	<string>/var/log/clashfx-service.log</string>
	<key>StandardErrorPath</key>
	<string>/var/log/clashfx-service.log</string>
</dict>
</plist>
`, service.DarwinLaunchDaemonLabel, binPath)
}

func retireLegacyDarwinHelper() {
	const (
		legacyPlist  = "/Library/LaunchDaemons/com.clashfx.app.Helper.plist"
		legacyLabel  = "com.clashfx.app.Helper"
		legacyHelper = "/Library/PrivilegedHelperTools/com.clashfx.app.Helper"
	)
	_ = exec.Command("/bin/launchctl", "bootout", "system", legacyPlist).Run()
	_ = exec.Command("/bin/launchctl", "bootout", "system/"+legacyLabel).Run()
	_ = os.Remove(legacyPlist)
	_ = os.Remove(legacyHelper)
	if matches, err := filepath.Glob(legacyHelper + "*"); err == nil {
		for _, m := range matches {
			_ = os.Remove(m)
		}
	}
	_ = exec.Command("/usr/bin/pkill", "-TERM", "-f", `mihomo_core.*\.enhanced_config`).Run()
	time.Sleep(250 * time.Millisecond)
	_, _ = sysproxy.CleanLegacyTunDNS()
}

const darwinSocketFilterFW = "/usr/libexec/ApplicationFirewall/socketfilterfw"

func runSocketFilterFW(args ...string) error {
	if _, err := os.Stat(darwinSocketFilterFW); err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, darwinSocketFilterFW, args...).Run()
}

func ensureDarwinFirewallRules(srcDir string) {
	if _, err := os.Stat(darwinSocketFilterFW); err != nil {
		return
	}
	targets := []string{
		filepath.Join(service.DarwinPrivilegedBinDir, "mihomo_core"),
		filepath.Join(service.DarwinPrivilegedBinDir, "clashfx-service"),
		filepath.Join(srcDir, "clashfx-daemon"),
		filepath.Join(srcDir, "mihomo_core"),
	}
	cleanSrc := filepath.Clean(srcDir)
	if strings.HasSuffix(cleanSrc, string(filepath.Separator)+"Contents"+string(filepath.Separator)+"MacOS") {
		appBundle := filepath.Dir(filepath.Dir(cleanSrc))
		targets = append(targets, appBundle)
	}
	seen := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		clean := filepath.Clean(target)
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		if _, err := os.Stat(clean); err != nil {
			continue
		}
		_ = runSocketFilterFW("--add", clean)
		_ = runSocketFilterFW("--unblockapp", clean)
	}
}

func removeDarwinFirewallRules() {
	for _, name := range []string{"mihomo_core", "clashfx-service"} {
		_ = runSocketFilterFW("--remove", filepath.Join(service.DarwinPrivilegedBinDir, name))
	}
}

func installAndStartService(sourceDir string) error {
	if os.Geteuid() != 0 {
		return errors.New("install must be run as root")
	}
	srcDir := strings.TrimSpace(sourceDir)
	if srcDir == "" {
		dir, err := resolveTrustedDir()
		if err != nil {
			return err
		}
		srcDir = dir
	}

	retireLegacyDarwinHelper()

	if err := os.MkdirAll(service.DarwinPrivilegedBinDir, 0755); err != nil {
		return err
	}
	_ = os.Chown(service.DarwinPrivilegedBinDir, 0, 0)

	// Boot out existing service before replacing binaries if already registered,
	// and wait for launchd to unload it cleanly so shutdown does not race bootstrap.
	_ = exec.Command("/bin/launchctl", "bootout", "system/"+service.DarwinLaunchDaemonLabel).Run()
	_ = exec.Command("/bin/launchctl", "bootout", "system", service.DarwinLaunchDaemonPlist).Run()
	bootoutDeadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(bootoutDeadline) {
		if err := exec.Command("/bin/launchctl", "print", "system/"+service.DarwinLaunchDaemonLabel).Run(); err != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	for _, name := range []string{"clashfx-service", "mihomo_core"} {
		src := filepath.Join(srcDir, name)
		dst := filepath.Join(service.DarwinPrivilegedBinDir, name)
		if name == "mihomo_core" && service.ShouldPreserveInstalledCore(src, dst) {
			_ = os.Chown(dst, 0, 0)
			_ = os.Chmod(dst, 0755)
			_ = exec.Command("/usr/bin/xattr", "-d", "com.apple.quarantine", dst).Run()
			continue
		}
		if err := copyRootOwnedExecutable(src, dst); err != nil {
			return fmt.Errorf("install %s: %w", name, err)
		}
	}

	ensureDarwinFirewallRules(srcDir)

	plistData := []byte(buildLaunchDaemonPlist())
	if err := os.WriteFile(service.DarwinLaunchDaemonPlist, plistData, 0644); err != nil {
		return fmt.Errorf("write LaunchDaemon plist: %w", err)
	}
	_ = os.Chown(service.DarwinLaunchDaemonPlist, 0, 0)

	// RunAtLoad=true in the plist starts the daemon on bootstrap; avoid an immediate
	// kickstart -k which would SIGTERM instance #1 while spawning instance #2.
	if out, err := exec.Command("/bin/launchctl", "bootstrap", "system", service.DarwinLaunchDaemonPlist).CombinedOutput(); err != nil {
		if kOut, kErr := exec.Command("/bin/launchctl", "kickstart", "-k", "system/"+service.DarwinLaunchDaemonLabel).CombinedOutput(); kErr != nil {
			return fmt.Errorf("launchctl bootstrap failed: %w (%s) / kickstart: %v (%s)", err, strings.TrimSpace(string(out)), kErr, strings.TrimSpace(string(kOut)))
		}
	}

	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		resp, err := service.Call(ctx, service.Request{Action: "status"})
		cancel()
		if err == nil && resp.OK {
			return nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	return fmt.Errorf("service started but did not respond at %s", service.ServiceSocketPath())
}

func uninstallService() error {
	if os.Geteuid() != 0 {
		return errors.New("uninstall must be run as root")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	_, _ = service.Call(ctx, service.Request{Action: "disarm_ai_guard"})
	_, _ = service.Call(ctx, service.Request{Action: "stop_core"})
	cancel()
	// Always flush pf anchor directly in case the service was dead/unresponsive.
	service.FlushDarwinPFAnchorDirect()
	_ = exec.Command("/bin/launchctl", "bootout", "system/"+service.DarwinLaunchDaemonLabel).Run()
	_ = exec.Command("/bin/launchctl", "bootout", "system", service.DarwinLaunchDaemonPlist).Run()
	_ = os.Remove(service.DarwinLaunchDaemonPlist)
	_ = os.Remove(service.ServiceSocketPath())
	removeDarwinFirewallRules()
	service.ClearPersistedServiceState("")
	_ = os.RemoveAll(service.DarwinPrivilegedBinDir)
	return nil
}

func controlService(start bool) error {
	if os.Geteuid() != 0 {
		return errors.New("start/stop must be run as root")
	}
	if start {
		if out, err := exec.Command("/bin/launchctl", "kickstart", "-k", "system/"+service.DarwinLaunchDaemonLabel).CombinedOutput(); err != nil {
			if bOut, bErr := exec.Command("/bin/launchctl", "bootstrap", "system", service.DarwinLaunchDaemonPlist).CombinedOutput(); bErr != nil {
				return fmt.Errorf("launchctl start failed: %v (%s) / %v (%s)", err, strings.TrimSpace(string(out)), bErr, strings.TrimSpace(string(bOut)))
			}
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	_, _ = service.Call(ctx, service.Request{Action: "disarm_ai_guard"})
	_, _ = service.Call(ctx, service.Request{Action: "stop_core"})
	cancel()
	// Use bootout rather than kill SIGTERM so KeepAlive=true does not immediately respawn it.
	_ = exec.Command("/bin/launchctl", "bootout", "system/"+service.DarwinLaunchDaemonLabel).Run()
	_ = exec.Command("/bin/launchctl", "bootout", "system", service.DarwinLaunchDaemonPlist).Run()
	service.FlushDarwinPFAnchorDirect()
	return nil
}

func main() {
	cmd := "status"
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd = args[0]
		args = args[1:]
	}

	var err error
	switch cmd {
	case "daemon", "console":
		err = runDaemon()
	case "install":
		fs := flag.NewFlagSet("install", flag.ContinueOnError)
		srcDir := fs.String("source-dir", "", "source directory containing clashfx-service and mihomo_core")
		if err = fs.Parse(args); err == nil {
			err = installAndStartService(*srcDir)
		}
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
	case "version":
		fmt.Println("clashfx-service 1.0")
	default:
		err = fmt.Errorf("usage: %s [daemon|install|uninstall|start|stop|status|console|version]", filepath.Base(os.Args[0]))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
