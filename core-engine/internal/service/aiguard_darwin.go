//go:build darwin

package service

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const darwinPFAnchorName = "com.apple/clashfx.ailock"

var (
	mihomoDefaultTunV4Prefix = netip.MustParsePrefix("198.18.0.0/15")
	mihomoDefaultTunV6Prefix = netip.MustParsePrefix("fdfe:dcba:9876::/48")
)

// FlushDarwinPFAnchorDirect unconditionally flushes ClashFX's dedicated pf anchor.
// Used during uninstall or recovery even when the privileged service is unresponsive.
func FlushDarwinPFAnchorDirect() {
	if os.Geteuid() != 0 || strings.TrimSpace(os.Getenv("CLASHFX_DISABLE_PRIVILEGED_SERVICE")) == "1" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = exec.CommandContext(ctx, "/sbin/pfctl", "-a", darwinPFAnchorName, "-F", "all").Run()
	cancel()
}

// darwinAIGuard implements crash-window fail-closed protection on macOS using a
// dedicated pf anchor under Apple's built-in "com.apple/*" anchor point (ref-counted
// via pfctl -E / -X so /etc/pf.conf is never modified and existing PF users are never disturbed).
// While mihomo_core + utun are healthy, the anchor stays empty; if mihomo_core crashes or
// enters restart/crash_loop while AI Lock is active, direct public-internet egress for the
// desktop console user is blocked immediately until TUN recovers or AI Lock is disarmed.
type darwinAIGuard struct {
	mu       sync.Mutex
	pfToken  string
	blocking bool
	status   AIGuardStatus
}

func newPlatformAIGuard() aiGuardController {
	return &darwinAIGuard{
		status: AIGuardStatus{
			Mode:    AIGuardModeInactive,
			Backend: "pf",
		},
	}
}

func ifaceHasMihomoTunAddr(addrs []net.Addr, strictAlias bool) bool {
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip == nil {
			continue
		}
		addr, ok := netip.AddrFromSlice(ip)
		if !ok {
			continue
		}
		addr = addr.Unmap()
		if addr.IsLinkLocalUnicast() || addr.IsLoopback() || addr.IsUnspecified() {
			continue
		}
		if strictAlias {
			return true
		}
		if mihomoDefaultTunV4Prefix.Contains(addr) || mihomoDefaultTunV6Prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func (g *darwinAIGuard) IsTunHealthy(alias string) (uint64, bool) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return 0, false
	}
	wantExact := strings.TrimSpace(alias)
	strictAlias := strings.HasPrefix(wantExact, "utun")
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		if strictAlias {
			if iface.Name != wantExact {
				continue
			}
		} else if !strings.HasPrefix(iface.Name, "utun") {
			continue
		}
		addrs, aErr := iface.Addrs()
		if aErr == nil && ifaceHasMihomoTunAddr(addrs, strictAlias) {
			return uint64(iface.Index), true
		}
	}
	return 0, false
}

func (g *darwinAIGuard) Status() AIGuardStatus {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.status
}

func (g *darwinAIGuard) Disarm() AIGuardStatus {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.clearAnchorLocked()
	g.status = AIGuardStatus{
		Enabled:   false,
		Armed:     false,
		Mode:      AIGuardModeInactive,
		Backend:   "pf",
		UpdatedAt: time.Now().Format(time.RFC3339),
	}
	return g.status
}

func (g *darwinAIGuard) Sync(cfg AIGuardConfig, coreRunning bool) AIGuardStatus {
	g.mu.Lock()
	defer g.mu.Unlock()

	if !cfg.Enabled {
		g.clearAnchorLocked()
		g.status = AIGuardStatus{
			Enabled:   false,
			Armed:     false,
			Mode:      AIGuardModeInactive,
			Backend:   "pf",
			UpdatedAt: time.Now().Format(time.RFC3339),
		}
		return g.status
	}

	if coreRunning {
		g.clearAnchorLocked()
		g.status = AIGuardStatus{
			Enabled:        true,
			Armed:          true,
			Mode:           AIGuardModeArmedNormal,
			Backend:        "pf",
			ProtectedCount: len(cfg.ProtectedExePaths),
			UpdatedAt:      time.Now().Format(time.RFC3339),
		}
		return g.status
	}

	// Only touch pf when running as privileged LaunchDaemon (root) outside test harness.
	if os.Geteuid() != 0 || strings.TrimSpace(os.Getenv("CLASHFX_DISABLE_PRIVILEGED_SERVICE")) == "1" {
		g.status = AIGuardStatus{
			Enabled:        true,
			Armed:          false,
			Mode:           AIGuardModeBlockingLeaks,
			Backend:        "pf",
			ProtectedCount: len(cfg.ProtectedExePaths),
			UpdatedAt:      time.Now().Format(time.RFC3339),
		}
		return g.status
	}

	uid := cfg.ConsoleUID
	if uid < 500 {
		if detected, ok := consoleOwnerUID(); ok && detected >= 500 {
			uid = detected
		}
	}
	if uid < 500 {
		g.status = AIGuardStatus{
			Enabled:        true,
			Armed:          false,
			Mode:           AIGuardModeError,
			Backend:        "pf",
			ProtectedCount: len(cfg.ProtectedExePaths),
			LastError:      "no active desktop console UID available for macOS pf AI guard",
			UpdatedAt:      time.Now().Format(time.RFC3339),
		}
		return g.status
	}

	rules := buildDarwinPFBlockRules(uid, cfg.AllowLAN, cfg.AllowCIDRs)
	if err := g.applyAnchorLocked(rules); err != nil {
		g.status = AIGuardStatus{
			Enabled:        true,
			Armed:          false,
			Mode:           AIGuardModeError,
			Backend:        "pf",
			ProtectedCount: len(cfg.ProtectedExePaths),
			LastError:      err.Error(),
			UpdatedAt:      time.Now().Format(time.RFC3339),
		}
		return g.status
	}

	g.status = AIGuardStatus{
		Enabled:        true,
		Armed:          true,
		Mode:           AIGuardModeBlockingLeaks,
		Backend:        "pf",
		ProtectedCount: len(cfg.ProtectedExePaths),
		UpdatedAt:      time.Now().Format(time.RFC3339),
	}
	return g.status
}

func buildDarwinPFBlockRules(consoleUID uint32, allowLAN bool, allowCIDRs []string) string {
	v4Allow, v6Allow := parseAllowPrefixes(allowLAN, allowCIDRs)
	var b strings.Builder
	b.WriteString("pass quick on lo0 all\n")
	b.WriteString("pass out quick proto { tcp, udp } user root\n")
	for _, p := range v4Allow {
		fmt.Fprintf(&b, "pass out quick inet to %s\n", p.String())
	}
	for _, p := range v6Allow {
		fmt.Fprintf(&b, "pass out quick inet6 to %s\n", p.String())
	}
	fmt.Fprintf(&b, "block return-rst out quick inet proto tcp user %d\n", consoleUID)
	fmt.Fprintf(&b, "block drop out quick inet proto udp user %d\n", consoleUID)
	fmt.Fprintf(&b, "block return-rst out quick inet6 proto tcp user %d\n", consoleUID)
	fmt.Fprintf(&b, "block drop out quick inet6 proto udp user %d\n", consoleUID)
	return b.String()
}

func (g *darwinAIGuard) applyAnchorLocked(rules string) error {
	acquiredToken := false
	if g.pfToken == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		out, err := exec.CommandContext(ctx, "/sbin/pfctl", "-E").CombinedOutput()
		cancel()
		if err != nil {
			return fmt.Errorf("pfctl -E failed: %w (%s)", err, strings.TrimSpace(string(out)))
		}
		for _, line := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "Token :") {
				g.pfToken = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "Token :"))
				acquiredToken = g.pfToken != ""
				break
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	cmd := exec.CommandContext(ctx, "/sbin/pfctl", "-a", darwinPFAnchorName, "-f", "-")
	cmd.Stdin = bytes.NewBufferString(rules)
	out, err := cmd.CombinedOutput()
	cancel()
	if err != nil {
		// Release newly acquired pfctl -E reference so failed loads never leak pf enable refs.
		if acquiredToken && g.pfToken != "" {
			rCtx, rCancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = exec.CommandContext(rCtx, "/sbin/pfctl", "-X", g.pfToken).Run()
			rCancel()
			g.pfToken = ""
		}
		return fmt.Errorf("pfctl load anchor %s failed: %w (%s)", darwinPFAnchorName, err, strings.TrimSpace(string(out)))
	}
	g.blocking = true
	return nil
}

func (g *darwinAIGuard) clearAnchorLocked() {
	if os.Geteuid() != 0 || strings.TrimSpace(os.Getenv("CLASHFX_DISABLE_PRIVILEGED_SERVICE")) == "1" {
		g.blocking = false
		g.pfToken = ""
		return
	}
	// Always flush our dedicated anchor even when g.blocking is false, so rules left
	// behind across a crash, SIGKILL, or LaunchDaemon restart are never stranded.
	FlushDarwinPFAnchorDirect()
	g.blocking = false
	if g.pfToken != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = exec.CommandContext(ctx, "/sbin/pfctl", "-X", g.pfToken).Run()
		cancel()
		g.pfToken = ""
	}
}
