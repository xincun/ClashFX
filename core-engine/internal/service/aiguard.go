package service

import (
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	AIGuardModeInactive      = "inactive"
	AIGuardModeArmedNormal   = "armed_normal"
	AIGuardModeBlockingLeaks = "blocking_leaks"
	AIGuardModeError         = "error"
)

// AIGuardConfig describes the OS-level fail-closed AI Proxy Lock guard policy
// enforced by clashfx-service independently of mihomo_core's liveness.
type AIGuardConfig struct {
	Enabled           bool     `json:"enabled"`
	ConfigDir         string   `json:"config_dir,omitempty"`
	ProtectedExePaths []string `json:"protected_exe_paths,omitempty"`
	TunDeviceName     string   `json:"tun_device_name,omitempty"`
	AllowCIDRs        []string `json:"allow_cidrs,omitempty"`
	AllowLAN          bool     `json:"allow_lan"`
	ConsoleUID        uint32   `json:"console_uid,omitempty"`
}

// AIGuardStatus reports the live enforcement state of the OS-level AI Lock guard.
type AIGuardStatus struct {
	Enabled        bool   `json:"enabled"`
	Armed          bool   `json:"armed"`
	Mode           string `json:"mode"` // "inactive", "armed_normal", "blocking_leaks", "error"
	Backend        string `json:"backend,omitempty"`
	ProtectedCount int    `json:"protected_count"`
	TunAlias       string `json:"tun_alias,omitempty"`
	TunLUID        uint64 `json:"tun_luid,omitempty"`
	LastError      string `json:"last_error,omitempty"`
	UpdatedAt      string `json:"updated_at,omitempty"`
}

type aiGuardController interface {
	Sync(cfg AIGuardConfig, coreRunning bool) AIGuardStatus
	Disarm() AIGuardStatus
	Status() AIGuardStatus
	IsTunHealthy(alias string) (uint64, bool)
}

var defaultLANPrefixes = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
}

func parseAllowPrefixes(allowLAN bool, rawCIDRs []string) (v4 []netip.Prefix, v6 []netip.Prefix) {
	seen := make(map[string]bool)
	add := func(p netip.Prefix) {
		m := p.Masked()
		k := m.String()
		if seen[k] {
			return
		}
		seen[k] = true
		if m.Addr().Is4() {
			v4 = append(v4, m)
		} else if m.Addr().Is6() {
			v6 = append(v6, m)
		}
	}
	if allowLAN {
		for _, p := range defaultLANPrefixes {
			add(p)
		}
	}
	for _, raw := range rawCIDRs {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		if p, err := netip.ParsePrefix(s); err == nil {
			add(p)
		} else if addr, aErr := netip.ParseAddr(s); aErr == nil {
			if addr.Is4() {
				add(netip.PrefixFrom(addr, 32))
			} else if addr.Is6() {
				add(netip.PrefixFrom(addr, 128))
			}
		}
	}
	return v4, v6
}

// DefaultServiceDataDir returns the machine-scoped privileged data directory for
// clashfx-service (used for service logs and persisted boot-time AI guard policy).
func DefaultServiceDataDir(trustedDir string) string {
	if runtime.GOOS == "windows" {
		if pd := strings.TrimSpace(os.Getenv("ProgramData")); pd != "" {
			return filepath.Join(pd, "ClashFX")
		}
		if strings.TrimSpace(trustedDir) != "" {
			return filepath.Join(trustedDir, "service-data")
		}
		return filepath.Join(os.TempDir(), "ClashFX-service")
	}
	if runtime.GOOS == "darwin" {
		if os.Geteuid() == 0 {
			return "/Library/Application Support/ClashFX"
		}
	}
	if strings.TrimSpace(trustedDir) != "" {
		return filepath.Join(trustedDir, "service-data")
	}
	return filepath.Join(os.TempDir(), "clashfx-service")
}

func aiGuardStateFilePath(serviceDataDir string) string {
	return filepath.Join(serviceDataDir, "ai-guard-state.json")
}

func savePersistedAIGuardConfig(serviceDataDir string, cfg AIGuardConfig) error {
	if strings.TrimSpace(serviceDataDir) == "" {
		return nil
	}
	if err := os.MkdirAll(serviceDataDir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	path := aiGuardStateFilePath(serviceDataDir)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return os.WriteFile(path, data, 0600)
	}
	return nil
}

func loadPersistedAIGuardConfig(serviceDataDir string) (AIGuardConfig, bool) {
	if strings.TrimSpace(serviceDataDir) == "" {
		return AIGuardConfig{}, false
	}
	data, err := os.ReadFile(aiGuardStateFilePath(serviceDataDir))
	if err != nil {
		return AIGuardConfig{}, false
	}
	var cfg AIGuardConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return AIGuardConfig{}, false
	}
	return cfg, true
}

func clearPersistedAIGuardConfig(serviceDataDir string) {
	if strings.TrimSpace(serviceDataDir) == "" {
		return
	}
	_ = os.Remove(aiGuardStateFilePath(serviceDataDir))
}

// ClearPersistedServiceState removes persisted boot-time AI guard state during uninstall.
func ClearPersistedServiceState(serviceDataDir string) {
	dir := strings.TrimSpace(serviceDataDir)
	if dir == "" {
		dir = DefaultServiceDataDir("")
	}
	clearPersistedAIGuardConfig(dir)
}
