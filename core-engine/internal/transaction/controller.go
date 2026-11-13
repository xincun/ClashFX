package transaction

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/clashfx/core-engine/internal/portprobe"
	"github.com/clashfx/core-engine/internal/profile"
	"gopkg.in/yaml.v3"
)

// TransformHook mutates the candidate runtime YAML map before validation (e.g.
// AI Proxy Lock, Multi-Node fixed ports, CN Apps bypass, Enhanced TUN settings).
type TransformHook func(root map[string]any, settings profile.AppSettings) error

// CoreApplier abstracts validating a candidate config and applying it to the active core.
type CoreApplier interface {
	ValidateConfig(homeDir, configPath string) error
	ApplyRuntimeConfig(ctx context.Context, settings profile.AppSettings, homeDir, runtimeConfigPath string, apiPort int, secret string) error
}

type AppliedRuntimeState struct {
	ProfileName string `json:"profile_name"`
	MixedPort   int    `json:"mixed_port"`
	SocksPort   int    `json:"socks_port"`
	HTTPPort    int    `json:"http_port"`
	APIPort     int    `json:"api_port"`
	DNSPort     int    `json:"dns_port"`
	APISecret   string `json:"api_secret"`
	Enhanced    bool   `json:"enhanced"`
}

// Controller serializes runtime configuration generation, preflight validation,
// port reservation checks, and live reload with automatic rollback on failure.
type Controller struct {
	mu          sync.Mutex
	store       *profile.Store
	applier     CoreApplier
	dashboardUI string
	hooks       []TransformHook
	probePort   func(port int, checkUDP bool) (bool, bool)
	lastState   AppliedRuntimeState
}

func NewController(store *profile.Store, applier CoreApplier, dashboardUI string, hooks ...TransformHook) *Controller {
	return &Controller{
		store:       store,
		applier:     applier,
		dashboardUI: dashboardUI,
		hooks:       hooks,
		probePort:   portprobe.ProbeLoopbackPort,
	}
}

func (c *Controller) SetPortProbeForTest(fn func(port int, checkUDP bool) (bool, bool)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.probePort = fn
}

func (c *Controller) LastState() AppliedRuntimeState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastState
}

func (c *Controller) ResetActiveState() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastState = AppliedRuntimeState{}
}

func (c *Controller) RuntimeConfigPath() string {
	return filepath.Join(c.store.ActiveConfigDir(), ".runtime-config.yaml")
}

func (c *Controller) LastGoodConfigPath() string {
	return filepath.Join(c.store.ActiveConfigDir(), ".runtime-last-good.yaml")
}

// Apply builds a candidate runtime config for the selected profile, validates it,
// applies it to the core, and rolls back automatically if validation or apply fails.
func (c *Controller) Apply(ctx context.Context, includeRecoverySelections bool) (AppliedRuntimeState, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	settings := c.store.Settings()
	cfgDir := c.store.ActiveConfigDir()
	srcPath := c.store.ProfilePath(settings.SelectedProfile)

	srcBytes, err := os.ReadFile(srcPath)
	if err != nil {
		return c.lastState, fmt.Errorf("read profile %q: %w", settings.SelectedProfile, err)
	}
	root, err := profile.UnmarshalYAMLMap(srcBytes)
	if err != nil {
		return c.lastState, fmt.Errorf("parse profile %q: %w", settings.SelectedProfile, err)
	}

	// 1. Merge Profile Mixin (unless Enhanced Mode CustomConfigAsIs is enabled)
	if !(settings.EnhancedMode && settings.CustomConfigAsIs) {
		mixinBytes, err := c.store.ReadProfileMixinBytes()
		if err != nil {
			return c.lastState, fmt.Errorf("read Profile Mixin: %w", err)
		}
		root, err = profile.ApplyProfileMixin(root, mixinBytes)
		if err != nil {
			return c.lastState, err
		}
	}

	// 2. Apply runtime port, controller, DNS, TUN, and recovery selection policies
	state, err := c.prepareRuntimeTopology(root, settings, includeRecoverySelections)
	if err != nil {
		return c.lastState, err
	}

	// 3. Ensure disk-backed built-in ruleset files exist, then run registered feature hooks
	if err := profile.EnsureBuiltinRulesetFiles(cfgDir); err != nil {
		return c.lastState, err
	}
	for _, hook := range c.hooks {
		if err := hook(root, settings); err != nil {
			return c.lastState, err
		}
	}

	candidateBytes, err := yaml.Marshal(root)
	if err != nil {
		return c.lastState, fmt.Errorf("marshal runtime config: %w", err)
	}

	candidatePath := filepath.Join(cfgDir, ".runtime-candidate.yaml")
	defer os.Remove(candidatePath)
	if err := profile.WriteFileAtomic(candidatePath, candidateBytes, 0600); err != nil {
		return c.lastState, err
	}

	// 4. Fail-closed preflight validation with mihomo_core -t BEFORE touching active runtime!
	if c.applier != nil {
		if err := c.applier.ValidateConfig(cfgDir, candidatePath); err != nil {
			return c.lastState, err
		}
	}

	runtimePath := c.RuntimeConfigPath()
	lastGoodPath := c.LastGoodConfigPath()
	hadPreviousGood := false
	if _, statErr := os.Stat(lastGoodPath); statErr == nil {
		hadPreviousGood = true
	}

	if err := profile.WriteFileAtomic(runtimePath, candidateBytes, 0600); err != nil {
		return c.lastState, err
	}

	// 5. Apply to core; if apply fails and a last-good snapshot exists, automatically roll back!
	// Note: if the core is already running, REST reload must target the currently active API port/secret.
	dialAPIPort := state.APIPort
	dialSecret := state.APISecret
	if c.lastState.APIPort > 0 {
		dialAPIPort = c.lastState.APIPort
		dialSecret = c.lastState.APISecret
	}
	if c.applier != nil {
		if applyErr := c.applier.ApplyRuntimeConfig(ctx, settings, cfgDir, runtimePath, dialAPIPort, dialSecret); applyErr != nil {
			if hadPreviousGood {
				if goodBytes, rErr := os.ReadFile(lastGoodPath); rErr == nil {
					_ = profile.WriteFileAtomic(runtimePath, goodBytes, 0600)
					_ = c.applier.ApplyRuntimeConfig(ctx, settings, cfgDir, runtimePath, dialAPIPort, dialSecret)
				}
			}
			return c.lastState, fmt.Errorf("apply runtime config failed (rolled back): %w", applyErr)
		}
	}

	_ = profile.WriteFileAtomic(lastGoodPath, candidateBytes, 0600)
	c.lastState = state
	return state, nil
}

func (c *Controller) prepareRuntimeTopology(root map[string]any, settings profile.AppSettings, includeRecoverySelections bool) (AppliedRuntimeState, error) {
	state := AppliedRuntimeState{
		ProfileName: settings.SelectedProfile,
		Enhanced:    settings.EnhancedMode,
	}

	// Custom Config as-is (P2-2): preserve user's TUN/DNS/ports verbatim, only read back ports/secret!
	if settings.EnhancedMode && settings.CustomConfigAsIs {
		state.MixedPort = intFromYAML(root["mixed-port"])
		state.HTTPPort = intFromYAML(root["port"])
		state.SocksPort = intFromYAML(root["socks-port"])
		state.APIPort = extractPortFromHostPort(fmt.Sprint(root["external-controller"]), settings.APIPort)
		if sec, ok := root["secret"].(string); ok {
			state.APISecret = sec
		}
		if c.dashboardUI != "" && root["external-ui"] == nil {
			root["external-ui"] = c.dashboardUI
		}
		return state, nil
	}

	// Proxy ports normalization
	if settings.PortOverride > 0 {
		root["mixed-port"] = settings.PortOverride
		delete(root, "port")
		delete(root, "socks-port")
	} else if intFromYAML(root["mixed-port"]) <= 0 {
		switch {
		case intFromYAML(root["port"]) > 0:
			root["mixed-port"] = intFromYAML(root["port"])
			delete(root, "port")
		case intFromYAML(root["socks-port"]) > 0:
			root["mixed-port"] = intFromYAML(root["socks-port"])
			delete(root, "socks-port")
		default:
			root["mixed-port"] = 7890
		}
	}
	mixedPort := intFromYAML(root["mixed-port"])
	if intFromYAML(root["port"]) == mixedPort {
		delete(root, "port")
	}
	if intFromYAML(root["socks-port"]) == mixedPort {
		delete(root, "socks-port")
	}
	state.MixedPort = mixedPort
	state.HTTPPort = intFromYAML(root["port"])
	if state.HTTPPort <= 0 {
		state.HTTPPort = mixedPort
	}
	state.SocksPort = intFromYAML(root["socks-port"])
	if state.SocksPort <= 0 {
		state.SocksPort = mixedPort
	}

	reserved := map[int]bool{
		state.MixedPort: true,
		state.HTTPPort:  true,
		state.SocksPort: true,
	}

	// Select API port (skipping Hyper-V/WinNAT WSAEACCES reserved ports if not already owned by us!)
	apiPort := settings.APIPort
	if apiPort <= 0 {
		apiPort = 19090
	}
	ownedAPIPort := c.lastState.APIPort
	if reserved[apiPort] || (apiPort != ownedAPIPort && !c.isPortUsable(apiPort, false)) {
		if ownedAPIPort > 0 && !reserved[ownedAPIPort] {
			apiPort = ownedAPIPort
		} else {
			fallback, err := portprobe.FindAvailablePort(19090, 19999, reserved, false, c.probePort)
			if err != nil {
				return state, fmt.Errorf("select external-controller port: %w", err)
			}
			apiPort = fallback
		}
	}
	reserved[apiPort] = true
	state.APIPort = apiPort
	root["external-controller"] = fmt.Sprintf("127.0.0.1:%d", apiPort)

	if settings.APISecret != "" {
		root["secret"] = settings.APISecret
		state.APISecret = settings.APISecret
	} else if sec, ok := root["secret"].(string); ok {
		state.APISecret = sec
	}
	if c.dashboardUI != "" {
		root["external-ui"] = c.dashboardUI
	}

	root["allow-lan"] = settings.AllowLan
	if !settings.AllowLan {
		root["bind-address"] = "127.0.0.1"
	} else if root["bind-address"] == nil {
		root["bind-address"] = "*"
	}
	root["ipv6"] = settings.IPv6
	if settings.OutboundMode != "" {
		root["mode"] = settings.OutboundMode
	}
	if settings.LogLevel != "" {
		root["log-level"] = settings.LogLevel
	}

	profMap, _ := root["profile"].(map[string]any)
	if profMap == nil {
		profMap = map[string]any{}
	}
	profMap["store-selected"] = true
	root["profile"] = profMap

	// DNS + TUN configuration
	dnsMap, _ := root["dns"].(map[string]any)
	if dnsMap == nil {
		dnsMap = map[string]any{}
	}
	if settings.EnhancedMode || boolFromYAML(dnsMap["enable"]) {
		dnsMap["enable"] = true
		preferredDNS := extractPortFromHostPort(fmt.Sprint(dnsMap["listen"]), 1053)
		if preferredDNS < 1024 {
			preferredDNS = 1053
		}
		ownedDNSPort := c.lastState.DNSPort
		if reserved[preferredDNS] || (preferredDNS != ownedDNSPort && !c.isPortUsable(preferredDNS, true)) {
			if ownedDNSPort > 0 && !reserved[ownedDNSPort] {
				preferredDNS = ownedDNSPort
			} else {
				nextDNS, err := portprobe.FindAvailablePort(10530, 11530, reserved, true, c.probePort)
				if err != nil {
					return state, fmt.Errorf("select DNS listen port: %w", err)
				}
				preferredDNS = nextDNS
			}
		}
		reserved[preferredDNS] = true
		state.DNSPort = preferredDNS
		dnsMap["listen"] = fmt.Sprintf("127.0.0.1:%d", preferredDNS)
		if mode, _ := dnsMap["enhanced-mode"].(string); mode == "" || mode == "normal" {
			dnsMap["enhanced-mode"] = "fake-ip"
		}
		if fir, _ := dnsMap["fake-ip-range"].(string); fir == "" {
			dnsMap["fake-ip-range"] = "198.18.0.1/16"
		}
		if dnsMap["default-nameserver"] == nil {
			dnsMap["default-nameserver"] = []string{"114.114.114.114", "223.5.5.5", "119.29.29.29"}
		}
		if dnsMap["nameserver"] == nil {
			dnsMap["nameserver"] = []string{"https://doh.pub/dns-query", "tls://223.5.5.5:853"}
		}
		root["dns"] = dnsMap
	}

	if settings.EnhancedMode {
		stack := strings.ToLower(strings.TrimSpace(settings.TunStack))
		if stack != "system" && stack != "gvisor" && stack != "mixed" {
			stack = "mixed"
		}
		mtu := settings.TunMTU
		if mtu == 0 {
			mtu = 1500
		}
		iface := strings.TrimSpace(settings.TunInterfaceName)
		tunMap := map[string]any{
			"enable":                true,
			"device":                "ClashFX",
			"stack":                 stack,
			"auto-route":            true,
			"auto-detect-interface": iface == "",
			"strict-route":          true,
			"dns-hijack":            []string{"any:53", "tcp://any:53"},
			"mtu":                   mtu,
		}
		if iface != "" {
			root["interface-name"] = iface
		}
		if excludes := splitTrimmedCSV(settings.TunRouteExcludes); len(excludes) > 0 {
			var cidrs, domains []string
			for _, item := range excludes {
				if strings.Contains(item, "/") {
					cidrs = append(cidrs, item)
				} else {
					domains = append(domains, item)
				}
			}
			if len(cidrs) > 0 {
				tunMap["route-exclude-address"] = cidrs
			}
			if len(domains) > 0 {
				existingFilter, _ := dnsMap["fake-ip-filter"].([]any)
				for _, d := range domains {
					existingFilter = append(existingFilter, d)
				}
				dnsMap["fake-ip-filter"] = existingFilter
				root["dns"] = dnsMap
			}
		}
		root["tun"] = tunMap
		// Prevent proxy loops from core/daemon executables under Windows TUN via inline rule-set
		profile.SetInlineClassicalRuleProvider(root, profile.InternalDirectRuleProviderName, []string{
			"PROCESS-NAME,mihomo_core.exe",
			"PROCESS-NAME,clashfx-daemon.exe",
			"PROCESS-NAME,clashfx-service.exe",
		})
		PrependUniqueRules(root, []string{
			"RULE-SET," + profile.InternalDirectRuleProviderName + ",DIRECT",
		})
	} else {
		root["tun"] = map[string]any{"enable": false}
	}

	// Startup recovery selections (only for cold start; Mihomo's PUT /configs rejects unknown top-level keys?
	// Wait: Mihomo's YAML unmarshaler ignores unknown top-level keys without strict mode, AND our
	// mihomo-bin reads x-clashfx-recovery-selections on startup!)
	if includeRecoverySelections {
		if sel := c.store.RecoverySelectionsForProfile(settings.SelectedProfile); len(sel) > 0 {
			filtered := filterValidSelections(root, sel)
			if len(filtered) > 0 {
				root[profile.RecoverySelectionsKey] = filtered
			}
		}
	} else {
		delete(root, profile.RecoverySelectionsKey)
	}

	return state, nil
}

func (c *Controller) isPortUsable(port int, checkUDP bool) bool {
	if c.probePort == nil {
		return true
	}
	ok, _ := c.probePort(port, checkUDP)
	return ok
}

// ReloadViaREST sends PUT /configs?force=true to a running Mihomo external-controller.
func ReloadViaREST(ctx context.Context, apiPort int, secret, runtimeConfigPath string) error {
	payload, err := json.Marshal(map[string]string{"path": runtimeConfigPath})
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/configs?force=true", apiPort)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("PUT /configs returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func PrependUniqueRules(root map[string]any, prefixRules []string) {
	var existing []string
	switch r := root["rules"].(type) {
	case []string:
		existing = r
	case []any:
		for _, item := range r {
			if s, ok := item.(string); ok {
				existing = append(existing, s)
			}
		}
	}
	seen := make(map[string]struct{}, len(prefixRules)+len(existing))
	out := make([]any, 0, len(prefixRules)+len(existing))
	for _, s := range append(prefixRules, existing...) {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	root["rules"] = out
}

func filterValidSelections(root map[string]any, sel map[string]string) map[string]string {
	groups, _ := root["proxy-groups"].([]any)
	if len(groups) == 0 {
		return nil
	}
	out := map[string]string{}
	for _, gRaw := range groups {
		g, ok := gRaw.(map[string]any)
		if !ok {
			continue
		}
		gName, _ := g["name"].(string)
		gType, _ := g["type"].(string)
		if gName == "" || strings.ToLower(gType) != "select" {
			continue
		}
		want, ok := sel[gName]
		if !ok || want == "" {
			continue
		}
		// Verify candidate is listed in group proxies (if explicit proxies list present)
		if cands, ok := g["proxies"].([]any); ok && len(cands) > 0 {
			found := false
			for _, c := range cands {
				if fmt.Sprint(c) == want {
					found = true
					break
				}
			}
			if !found && g["use"] == nil {
				continue
			}
		}
		out[gName] = want
	}
	return out
}

func intFromYAML(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case uint64:
		return int(n)
	case float64:
		return int(n)
	case string:
		var parsed int
		_, _ = fmt.Sscanf(strings.TrimSpace(n), "%d", &parsed)
		return parsed
	default:
		return 0
	}
}

func boolFromYAML(v any) bool {
	b, _ := v.(bool)
	return b
}

func extractPortFromHostPort(addr string, fallback int) int {
	addr = strings.TrimSpace(addr)
	if idx := strings.LastIndex(addr, ":"); idx >= 0 {
		if p := intFromYAML(addr[idx+1:]); p > 0 && p <= 65535 {
			return p
		}
	}
	return fallback
}

func splitTrimmedCSV(raw string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r'
	}) {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
