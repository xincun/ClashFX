package sysproxy

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/clashfx/core-engine/internal/profile"
)

type memoryBackend struct {
	mu    sync.Mutex
	state OSProxyState
}

func (m *memoryBackend) ReadState() (OSProxyState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state, nil
}

func (m *memoryBackend) WriteState(st OSProxyState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state = st
	return nil
}

const SnapshotFileName = "sysproxy-snapshot.json"

var defaultWindowsBypassTokens = []string{
	"localhost",
	"127.*",
	"10.*",
	"172.16.*", "172.17.*", "172.18.*", "172.19.*",
	"172.20.*", "172.21.*", "172.22.*", "172.23.*",
	"172.24.*", "172.25.*", "172.26.*", "172.27.*",
	"172.28.*", "172.29.*", "172.30.*", "172.31.*",
	"192.168.*",
	"169.254.*",
	"*.local",
	"*.lan",
	"*.localdomain",
	"*.home.arpa",
	"*.internal",
	"*.test",
	"*.corp",
	"*.intranet",
	"*.private",
	"*.msftconnecttest.com",
	"*.msftncsi.com",
	"<local>",
}

type OSProxyState struct {
	ProxyEnable      uint32 `json:"proxy_enable"`
	HasProxyServer   bool   `json:"has_proxy_server"`
	ProxyServer      string `json:"proxy_server,omitempty"`
	HasProxyOverride bool   `json:"has_proxy_override"`
	ProxyOverride    string `json:"proxy_override,omitempty"`
	HasAutoConfigURL bool   `json:"has_auto_config_url"`
	AutoConfigURL    string `json:"auto_config_url,omitempty"`
}

type Backend interface {
	ReadState() (OSProxyState, error)
	WriteState(OSProxyState) error
}

type persistedSnapshot struct {
	PreClashFXState    OSProxyState `json:"pre_clashfx_state"`
	AppliedProxyServer string       `json:"applied_proxy_server"`
}

// Manager manages Windows HKCU WinINet system proxy settings with crash-safe
// pre-enable snapshotting, AI Proxy Lock fail-closed exit semantics, one-click
// full reset, and wake/drift self-healing.
type Manager struct {
	mu           sync.Mutex
	snapshotPath string
	backend      Backend
}

func NewManager(appDataDir string, backend Backend) *Manager {
	if backend == nil {
		backend = NewOSBackend()
	}
	return &Manager{
		snapshotPath: filepath.Join(appDataDir, SnapshotFileName),
		backend:      backend,
	}
}

func (m *Manager) CurrentOSState() (OSProxyState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.backend.ReadState()
}

// RecoverAfterUncleanShutdown checks whether a previous ClashFX session crashed
// while owning the system proxy. If wantsSystemProxy is false, it immediately
// restores the pre-ClashFX proxy settings; if wantsSystemProxy is true, it keeps
// the preserved pre-ClashFX snapshot intact until the next Apply/Restore.
func (m *Manager) RecoverAfterUncleanShutdown(wantsSystemProxy bool) (recovered bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	snap, exists, err := m.readSnapshotLocked()
	if err != nil || !exists {
		return false, err
	}
	if wantsSystemProxy {
		return false, nil
	}
	if err := m.backend.WriteState(sanitizeRestoreTarget(snap.PreClashFXState, snap.AppliedProxyServer)); err != nil {
		return false, err
	}
	_ = os.Remove(m.snapshotPath)
	return true, nil
}

// Apply enables the Windows system proxy pointing at 127.0.0.1:<port>.
// On first enable, it persists the user's pre-existing OS proxy state to disk
// BEFORE mutating HKCU so a hard crash can never lose their original settings.
func (m *Manager) Apply(port int, customBypass string) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("invalid system proxy port %d", port)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	targetServer := fmt.Sprintf("127.0.0.1:%d", port)
	override := FormatBypassForOS(customBypass)

	current, err := m.backend.ReadState()
	if err != nil {
		return err
	}

	snap, hasSnap, err := m.readSnapshotLocked()
	if err != nil {
		return err
	}
	if !hasSnap {
		snap = persistedSnapshot{
			PreClashFXState:    sanitizeCapturedBaseline(current),
			AppliedProxyServer: targetServer,
		}
	} else {
		snap.AppliedProxyServer = targetServer
	}

	if err := m.writeSnapshotLocked(snap); err != nil {
		return fmt.Errorf("persist system proxy snapshot: %w", err)
	}

	desired := OSProxyState{
		ProxyEnable:      1,
		HasProxyServer:   true,
		ProxyServer:      targetServer,
		HasProxyOverride: true,
		ProxyOverride:    override,
		HasAutoConfigURL: false,
	}
	return m.backend.WriteState(desired)
}

// Restore reverts HKCU Internet Settings back to the pre-ClashFX snapshot (or
// disables ProxyEnable if no snapshot exists) and removes the snapshot file.
func (m *Manager) Restore() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	snap, hasSnap, err := m.readSnapshotLocked()
	if err != nil {
		return err
	}
	if hasSnap {
		if err := m.backend.WriteState(sanitizeRestoreTarget(snap.PreClashFXState, snap.AppliedProxyServer)); err != nil {
			return err
		}
		_ = os.Remove(m.snapshotPath)
		return nil
	}

	cur, err := m.backend.ReadState()
	if err != nil {
		return err
	}
	if cur.ProxyEnable == 0 {
		return nil
	}
	cur.ProxyEnable = 0
	return m.backend.WriteState(cur)
}

// Shutdown handles graceful daemon termination and restores pre-ClashFX proxy
// settings (clearing any lingering loopback proxy if the snapshot is absent) so
// exiting ClashFX never leaves the machine's browser or Captive Portal offline.
func (m *Manager) Shutdown(_ bool) error {
	return m.CleanupForUninstall()
}

// CleanupForUninstall unconditionally restores pre-ClashFX proxy settings (even if
// AI Proxy Lock fail-closed was active) or disables any lingering loopback proxy so
// uninstalling ClashFX never leaves the machine offline. Non-loopback corporate
// proxies are left untouched if no ClashFX snapshot exists.
func (m *Manager) CleanupForUninstall() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	snap, hasSnap, err := m.readSnapshotLocked()
	if err == nil && hasSnap {
		if wErr := m.backend.WriteState(sanitizeRestoreTarget(snap.PreClashFXState, snap.AppliedProxyServer)); wErr != nil {
			return wErr
		}
		_ = os.Remove(m.snapshotPath)
		return nil
	}

	cur, rErr := m.backend.ReadState()
	if rErr != nil {
		return rErr
	}
	if cur.ProxyEnable != 0 && isLoopbackProxyServer(cur.ProxyServer) {
		cur.ProxyEnable = 0
		return m.backend.WriteState(cur)
	}
	return nil
}

// ForceClearAll implements "Turn Off All Proxy Modes": unconditionally turns off
// ProxyEnable, clears ProxyServer and AutoConfigURL, and removes any snapshot.
func (m *Manager) ForceClearAll() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	cur, _ := m.backend.ReadState()
	cleared := OSProxyState{
		ProxyEnable:      0,
		HasProxyServer:   false,
		HasProxyOverride: cur.HasProxyOverride,
		ProxyOverride:    cur.ProxyOverride,
		HasAutoConfigURL: false,
	}
	if err := m.backend.WriteState(cleared); err != nil {
		return err
	}
	_ = os.Remove(m.snapshotPath)
	return nil
}

// ReconcileIfExpected checks HKCU after system wake or network switch and
// re-applies 127.0.0.1:<port> if another process or sleep transition cleared it.
func (m *Manager) ReconcileIfExpected(expectedEnabled bool, port int, customBypass string) (repaired bool, err error) {
	if !expectedEnabled || port < 1 || port > 65535 {
		return false, nil
	}
	m.mu.Lock()
	cur, err := m.backend.ReadState()
	m.mu.Unlock()
	if err != nil {
		return false, err
	}
	wantServer := fmt.Sprintf("127.0.0.1:%d", port)
	wantOverride := FormatBypassForOS(customBypass)
	if cur.ProxyEnable == 1 &&
		strings.EqualFold(strings.TrimSpace(cur.ProxyServer), wantServer) &&
		strings.EqualFold(strings.TrimSpace(cur.ProxyOverride), strings.TrimSpace(wantOverride)) &&
		!cur.HasAutoConfigURL {
		return false, nil
	}
	if err := m.Apply(port, customBypass); err != nil {
		return false, err
	}
	return true, nil
}

// FormatWindowsBypass converts user-provided bypass items (or empty for defaults)
// into a valid semicolon-delimited WinINet ProxyOverride string ending in <local>.
func FormatWindowsBypass(custom string) string {
	var rawTokens []string
	if strings.TrimSpace(custom) == "" {
		rawTokens = defaultWindowsBypassTokens
	} else {
		rawTokens = strings.FieldsFunc(custom, func(r rune) bool {
			return r == ';' || r == ',' || r == '\n' || r == '\r'
		})
	}

	seen := map[string]bool{}
	var out []string
	hasLocalTag := false

	for _, tok := range rawTokens {
		norm := normalizeWinINetToken(tok)
		if norm == "" {
			continue
		}
		if strings.EqualFold(norm, "<local>") {
			hasLocalTag = true
			continue
		}
		key := strings.ToLower(norm)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, norm)
	}

	if hasLocalTag || strings.TrimSpace(custom) == "" {
		out = append(out, "<local>")
	}
	return strings.Join(out, ";")
}

// FormatDarwinBypass parses custom or default bypass rules for macOS networksetup
// (-setproxybypassdomains), preserving CIDRs while dropping Windows-only <local>.
func FormatDarwinBypass(custom string) []string {
	if strings.TrimSpace(custom) == "" {
		custom = profile.DefaultDarwinProxyBypass
	}
	rawTokens := strings.FieldsFunc(custom, func(r rune) bool {
		return r == ';' || r == ',' || r == '\n' || r == '\r'
	})
	seen := map[string]bool{}
	out := make([]string, 0, len(rawTokens))
	for _, tok := range rawTokens {
		t := strings.TrimSpace(tok)
		if t == "" || strings.EqualFold(t, "<local>") {
			continue
		}
		key := strings.ToLower(t)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, t)
	}
	return out
}

func FormatBypassForOS(custom string) string {
	if runtime.GOOS == "darwin" {
		return strings.Join(FormatDarwinBypass(custom), ",")
	}
	return FormatWindowsBypass(custom)
}

func normalizeWinINetToken(token string) string {
	t := strings.TrimSpace(token)
	if t == "" {
		return ""
	}
	// Convert common IPv4 CIDR /8, /16, /24 notations into WinINet wildcards
	// because WinINet ProxyOverride only understands '*' wildcards, not CIDR!
	if ip, ipNet, err := net.ParseCIDR(t); err == nil {
		if v4 := ip.Mask(ipNet.Mask).To4(); v4 != nil {
			ones, bits := ipNet.Mask.Size()
			if bits == 32 {
				switch ones {
				case 8:
					return fmt.Sprintf("%d.*", v4[0])
				case 16:
					return fmt.Sprintf("%d.%d.*", v4[0], v4[1])
				case 24:
					return fmt.Sprintf("%d.%d.%d.*", v4[0], v4[1], v4[2])
				case 32:
					return v4.String()
				}
			}
		}
	}
	return t
}

func sanitizeCapturedBaseline(cur OSProxyState) OSProxyState {
	// If ProxyEnable is already pointing to 127.0.0.1 without a snapshot (e.g. an
	// orphaned loopback proxy from a deleted snapshot), do not treat a dead loopback
	// port as the user's "original corporate proxy" when they later disable proxy.
	if cur.ProxyEnable == 1 && isLoopbackProxyServer(cur.ProxyServer) {
		cur.ProxyEnable = 0
	}
	return cur
}

func sanitizeRestoreTarget(base OSProxyState, applied string) OSProxyState {
	if base.ProxyEnable == 1 && strings.EqualFold(strings.TrimSpace(base.ProxyServer), strings.TrimSpace(applied)) {
		base.ProxyEnable = 0
	}
	return base
}

func isLoopbackProxyServer(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	if strings.HasPrefix(s, "127.0.0.1:") || strings.HasPrefix(s, "localhost:") {
		return true
	}
	for _, part := range strings.Split(s, ";") {
		part = strings.TrimSpace(part)
		if idx := strings.IndexByte(part, '='); idx >= 0 {
			part = strings.TrimSpace(part[idx+1:])
		}
		if strings.HasPrefix(part, "127.0.0.1:") || strings.HasPrefix(part, "localhost:") {
			return true
		}
	}
	return false
}

func (m *Manager) readSnapshotLocked() (persistedSnapshot, bool, error) {
	data, err := os.ReadFile(m.snapshotPath)
	if os.IsNotExist(err) {
		return persistedSnapshot{}, false, nil
	}
	if err != nil {
		return persistedSnapshot{}, false, err
	}
	var snap persistedSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		_ = os.Remove(m.snapshotPath)
		return persistedSnapshot{}, false, nil
	}
	return snap, true, nil
}

func (m *Manager) writeSnapshotLocked(snap persistedSnapshot) error {
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	return profile.WriteFileAtomic(m.snapshotPath, data, 0600)
}
