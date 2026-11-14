//go:build darwin

package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/xml"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/clashfx/core-engine/internal/service"
	"golang.org/x/sys/unix"
)

const loginAgentLabel = "com.clashfx.app.login"

func darwinSocketDir() string {
	dir := fmt.Sprintf("/tmp/clashfx-%d", os.Getuid())
	_ = os.MkdirAll(dir, 0700)
	return dir
}

func resolveUnixSocketPath(endpoint string) string {
	ep := strings.TrimSpace(endpoint)
	if ep == "" {
		return DefaultEndpointName()
	}
	// macOS sockaddr_un.sun_path max is 104 bytes; keep a generous safety margin.
	if len(ep) <= 96 {
		return ep
	}
	sum := sha256.Sum256([]byte(ep))
	return filepath.Join(darwinSocketDir(), fmt.Sprintf("ep-%x.sock", sum[:6]))
}

func DefaultEndpointName() string {
	return filepath.Join(darwinSocketDir(), "daemon.sock")
}

func AcquireSingleInstanceLock(key string) (func(), error) {
	cleaned := strings.TrimSpace(key)
	if cleaned == "" {
		cleaned = "default"
	}
	sum := sha256.Sum256([]byte(cleaned))
	lockPath := filepath.Join(darwinSocketDir(), fmt.Sprintf("lock-%x.lock", sum[:6]))
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open single-instance lock %s: %w", lockPath, err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("another clashfx-daemon instance is already running for %s", cleaned)
	}
	return func() {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}, nil
}

func ListenIPC(endpoint string) (net.Listener, error) {
	sockPath := resolveUnixSocketPath(endpoint)
	if err := os.MkdirAll(filepath.Dir(sockPath), 0700); err != nil {
		return nil, err
	}
	_ = os.Remove(sockPath)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(sockPath, 0600)
	return ln, nil
}

func DialIPC(endpoint string) (net.Conn, error) {
	return net.DialTimeout("unix", resolveUnixSocketPath(endpoint), 5*time.Second)
}

func IsPrivilegedServiceInstalled(_ string) bool {
	for _, reqPath := range []string{
		service.DarwinLaunchDaemonPlist,
		filepath.Join(service.DarwinPrivilegedBinDir, "clashfx-service"),
		filepath.Join(service.DarwinPrivilegedBinDir, "mihomo_core"),
	} {
		if _, err := os.Stat(reqPath); err != nil {
			return false
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, err := service.Call(ctx, service.Request{Action: "status"})
	return err == nil && resp.OK
}

// ElevateAndInstallService triggers a native macOS administrator authorization
// prompt via osascript to install/upgrade and start com.clashfx.service.
func ElevateAndInstallService(serviceExePath string) error {
	cleanedExe := filepath.Clean(strings.TrimSpace(serviceExePath))
	if _, err := os.Stat(cleanedExe); err != nil {
		return fmt.Errorf("clashfx-service binary missing at %s: %w", cleanedExe, err)
	}
	srcDir := filepath.Dir(cleanedExe)
	shellCmd := fmt.Sprintf("%s install --source-dir %s", posixShellQuote(cleanedExe), posixShellQuote(srcDir))
	appleScript := fmt.Sprintf("do shell script %q with administrator privileges", shellCmd)

	cmd := exec.Command("/usr/bin/osascript", "-e", appleScript)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return fmt.Errorf("administrator service installation failed: %w (%s)", err, msg)
		}
		return fmt.Errorf("administrator service installation cancelled or failed: %w", err)
	}
	return nil
}

func SetLaunchAtLogin(enabled bool, uiExePath string) error {
	if strings.TrimSpace(os.Getenv("CLASHFX_DISABLE_LAUNCH_AGENT")) == "1" {
		return nil
	}
	if flag.Lookup("test.v") != nil && strings.TrimSpace(os.Getenv("CLASHFX_TEST_REAL_LAUNCH_AGENT")) != "1" {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	agentDir := filepath.Join(home, "Library", "LaunchAgents")
	plistPath := filepath.Join(agentDir, loginAgentLabel+".plist")
	if !enabled {
		err := os.Remove(plistPath)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	target := strings.TrimSpace(uiExePath)
	if target == "" {
		return fmt.Errorf("empty ClashFX executable path for LaunchAgent")
	}
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		return err
	}

	var escTarget bytes.Buffer
	_ = xml.EscapeText(&escTarget, []byte(filepath.Clean(target)))
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>ProcessType</key>
	<string>Interactive</string>
</dict>
</plist>
`, loginAgentLabel, escTarget.String())
	return os.WriteFile(plistPath, []byte(plist), 0644)
}

func StartPowerResumeMonitor(onResume func()) func() {
	if onResume == nil {
		return func() {}
	}
	stopCh := make(chan struct{})
	go func() {
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		lastTick := time.Now()
		for {
			select {
			case <-stopCh:
				return
			case now := <-ticker.C:
				// Go Time.Sub uses monotonic clock (which pauses across macOS sleep),
				// whereas Round(0).Sub strips monotonic clock to measure wall-clock elapsed.
				monoElapsed := now.Sub(lastTick)
				wallElapsed := now.Round(0).Sub(lastTick.Round(0))
				lastTick = now
				if wallElapsed-monoElapsed > 12*time.Second || monoElapsed > 15*time.Second {
					onResume()
				}
			}
		}
	}()
	return func() {
		close(stopCh)
	}
}

func posixShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
