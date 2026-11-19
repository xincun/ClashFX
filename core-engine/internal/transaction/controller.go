package transaction

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/clashfx/core-engine/internal/portprobe"
	"github.com/clashfx/core-engine/internal/profile"
	"gopkg.in/yaml.v3"
)

var captivePortalFakeIPFilter = []string{
	"+.msftconnecttest.com",
	"+.msftncsi.com",
	"dns.msftncsi.com",
	"captive.apple.com",
}

var captivePortalSystemDNSPolicyKeys = []string{
	"captive.apple.com",
	"+.msftconnecttest.com",
	"+.msftncsi.com",
}

var defaultOSExtraFakeIPFilter = []string{
	"time.apple.com",
	"time-ios.apple.com",
	"sequoia.apple.com",
	"seed-sequoia.siri.apple.com",
	"mesu.apple.com",
	"time.*.com",
	"time.*.gov",
	"ntp.*.com",
	"localhost.ptlogin2.qq.com",
	"localhost.sec.qq.com",
	"+.stun.*.*",
	"+.stun.*.*.*",
	"+.srv.nintendo.net",
	"+.stun.playstation.net",
	"xbox.*.microsoft.com",
}

// TransformHook mutates the candidate runtime YAML map before validation (e.g.
// AI Proxy Lock, Multi-Node fixed ports, CN Apps bypass, Enhanced TUN settings).
type TransformHook func(root map[string]any, settings profile.AppSettings) error

// CoreApplier abstracts validating a candidate config and applying it to the active core.
type CoreApplier interface {
	ValidateConfig(homeDir, configPath string) error
	ApplyRuntimeConfig(ctx context.Context, settings profile.AppSettings, homeDir, runtimeConfigPath string, apiPort int, secret string, forceRestart bool) error
}

type AppliedRuntimeState struct {
	ProfileName      string `json:"profile_name"`
	MixedPort        int    `json:"mixed_port"`
	SocksPort        int    `json:"socks_port"`
	HTTPPort         int    `json:"http_port"`
	APIPort          int    `json:"api_port"`
	DNSPort          int    `json:"dns_port"`
	APISecret        string `json:"api_secret"`
	Enhanced         bool   `json:"enhanced"`
	requestedAPIPort int
}

// Controller serializes runtime configuration generation, preflight validation,
// port reservation checks, and live reload with automatic rollback on failure.
type Controller struct {
	applyMu          sync.Mutex
	stateMu          sync.RWMutex
	store            *profile.Store
	applier          CoreApplier
	dashboardUI      string
	hooks            []TransformHook
	probePort        func(port int, checkUDP bool) (bool, bool)
	customPortProbe  bool
	sourceResolver   func(profileName string) ([]byte, bool)
	settingsResolver func(profile.AppSettings) profile.AppSettings
	lastState        AppliedRuntimeState
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

// SetProfileSourceResolver overrides disk lookup when synthesizing runtime config bytes.
func (c *Controller) SetProfileSourceResolver(fn func(profileName string) ([]byte, bool)) {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	c.sourceResolver = fn
}

// SetEffectiveSettingsResolver transforms persisted settings into effective runtime settings
// during config generation without mutating settings.json on disk.
func (c *Controller) SetEffectiveSettingsResolver(fn func(profile.AppSettings) profile.AppSettings) {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	c.settingsResolver = fn
}

func (c *Controller) SetPortProbeForTest(fn func(port int, checkUDP bool) (bool, bool)) {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	c.probePort = fn
	c.customPortProbe = true
}

func (c *Controller) LastState() AppliedRuntimeState {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.lastState
}

func (c *Controller) setLastState(st AppliedRuntimeState) {
	c.stateMu.Lock()
	c.lastState = st
	c.stateMu.Unlock()
}

func (c *Controller) ResetActiveState() {
	c.setLastState(AppliedRuntimeState{})
}

// SeedActiveStateFromDisk populates lastState from an existing .runtime-config.yaml
// when adopting an already-running ClashFXService core across daemon restarts so
// subsequent Apply calls can use zero-downtime REST reload instead of hard-restarting TUN.
func (c *Controller) SeedActiveStateFromDisk(configPath string) bool {
	if strings.TrimSpace(configPath) == "" {
		configPath = c.RuntimeConfigPath()
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return false
	}
	root, err := profile.UnmarshalYAMLMap(raw)
	if err != nil || root == nil {
		return false
	}
	settings := c.store.Settings()
	apiPort := extractPortFromHostPort(fmt.Sprint(root["external-controller"]), 0)
	if apiPort <= 0 {
		return false
	}
	requestedAPIPort := settings.APIPort
	if requestedAPIPort <= 0 {
		requestedAPIPort = 19090
	}
	mixedPort := intFromYAML(root["mixed-port"])
	httpPort := intFromYAML(root["port"])
	if httpPort <= 0 {
		httpPort = mixedPort
	}
	socksPort := intFromYAML(root["socks-port"])
	if socksPort <= 0 {
		socksPort = mixedPort
	}
	var dnsPort int
	if dnsMap, ok := root["dns"].(map[string]any); ok && dnsMap != nil {
		dnsPort = extractPortFromHostPort(fmt.Sprint(dnsMap["listen"]), 0)
	}
	var enhanced bool
	if tunMap, ok := root["tun"].(map[string]any); ok && tunMap != nil {
		enhanced = boolFromYAML(tunMap["enable"])
	}
	secret, _ := root["secret"].(string)
	c.setLastState(AppliedRuntimeState{
		ProfileName:      settings.SelectedProfile,
		MixedPort:        mixedPort,
		SocksPort:        socksPort,
		HTTPPort:         httpPort,
		APIPort:          apiPort,
		DNSPort:          dnsPort,
		APISecret:        secret,
		Enhanced:         enhanced,
		requestedAPIPort: requestedAPIPort,
	})
	return true
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
	return c.ApplyWithOptions(ctx, includeRecoverySelections, false)
}

// ApplyWithOptions is like Apply, with an optional forceRestartOverride used when
// switching execution backends (localRunner <-> ClashFXService) without wiping prevState
// owned ports before the new topology is validated and committed.
func (c *Controller) ApplyWithOptions(ctx context.Context, includeRecoverySelections bool, forceRestartOverride bool) (AppliedRuntimeState, error) {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()

	prevState := c.LastState()
	settings := c.store.Settings()
	if c.settingsResolver != nil {
		settings = c.settingsResolver(settings)
	}
	cfgDir := c.store.ActiveConfigDir()

	var srcBytes []byte
	var err error
	if c.sourceResolver != nil {
		if raw, ok := c.sourceResolver(settings.SelectedProfile); ok {
			srcBytes = raw
		}
	}
	if srcBytes == nil {
		srcPath := c.store.ProfilePath(settings.SelectedProfile)
		srcBytes, err = os.ReadFile(srcPath)
		if err != nil {
			if os.IsNotExist(err) && (strings.TrimSpace(settings.SelectedProfile) == "" || strings.EqualFold(strings.TrimSpace(settings.SelectedProfile), "config")) {
				srcBytes = []byte(profile.DefaultStarterConfigYAML)
			} else {
				return prevState, fmt.Errorf("read profile %q: %w", settings.SelectedProfile, err)
			}
		}
	}
	root, err := profile.UnmarshalYAMLMap(srcBytes)
	if err != nil {
		return prevState, fmt.Errorf("parse profile %q: %w", settings.SelectedProfile, err)
	}

	// 1. Merge Profile Mixin (unless Enhanced Mode CustomConfigAsIs is enabled)
	if !(settings.EnhancedMode && settings.CustomConfigAsIs) {
		mixinBytes, err := c.store.ReadProfileMixinBytes()
		if err != nil {
			return prevState, fmt.Errorf("read Profile Mixin: %w", err)
		}
		root, err = profile.ApplyProfileMixin(root, mixinBytes)
		if err != nil {
			return prevState, err
		}
	}

	// Defense-in-depth: strip unsafe or ClashFX-reserved provider paths from untrusted profiles
	// (including subscriptions imported prior to this guard) before launching privileged core.
	profile.SanitizeUntrustedProviderPaths(root)

	// 2. Ensure disk-backed editable built-in ruleset files exist BEFORE prepareRuntimeTopology
	// so clashfx-private-direct.list drives dns.fake-ip-filter and dns.nameserver-policy dynamically.
	if err := profile.EnsureBuiltinRulesetFiles(cfgDir); err != nil {
		return prevState, err
	}

	// 3. Apply runtime port, controller, DNS, and TUN policies
	state, err := c.prepareRuntimeTopology(root, settings)
	if err != nil {
		return prevState, err
	}

	// 4. Run registered feature hooks
	for _, hook := range c.hooks {
		if hook == nil {
			continue
		}
		if err := hook(root, settings); err != nil {
			return prevState, err
		}
	}
	// Sync editable built-in file rule-provider paths to the active writable
	// ruleset directory (falls back to ./clashfx-rulesets when legacy ./ruleset
	// is root-owned / non-writable on macOS).
	if rpMap, ok := root["rule-providers"].(map[string]any); ok {
		for _, name := range []string{profile.AILockRuleProviderName, profile.PrivateDirectRuleProviderName, profile.CNAppsRuleProviderName} {
			if entry, exists := rpMap[name].(map[string]any); exists {
				entry["path"] = profile.BuiltinRulesetRelPathInDir(cfgDir, name)
			}
		}
	}

	candidateBytes, err := yaml.Marshal(root)
	if err != nil {
		return prevState, fmt.Errorf("marshal runtime config: %w", err)
	}

	candidatePath := filepath.Join(cfgDir, ".runtime-candidate.yaml")
	defer os.Remove(candidatePath)
	if err := profile.WriteFileAtomic(candidatePath, candidateBytes, 0600); err != nil {
		return prevState, err
	}

	// 4. Fail-closed preflight validation with mihomo_core -t BEFORE touching active runtime!
	if c.applier != nil {
		if err := c.applier.ValidateConfig(cfgDir, candidatePath); err != nil {
			return prevState, err
		}
	}

	runtimePath := c.RuntimeConfigPath()
	lastGoodPath := c.LastGoodConfigPath()
	hadPreviousGood := false
	if _, statErr := os.Stat(lastGoodPath); statErr == nil {
		hadPreviousGood = true
	}

	if err := profile.WriteFileAtomic(runtimePath, candidateBytes, 0600); err != nil {
		return prevState, err
	}

	// 5. Apply to core; if apply fails and a last-good snapshot exists, automatically roll back!
	// Note: Mihomo's PUT /configs hot reload never recreates external-controller or updates its
	// secret. Only use REST reload when the core already has a known controller state whose
	// APIPort and APISecret are unchanged and the runner backend did not change.
	controllerChanged := prevState.APIPort > 0 &&
		(state.APIPort != prevState.APIPort || state.APISecret != prevState.APISecret)
	dialAPIPort := state.APIPort
	dialSecret := state.APISecret
	if prevState.APIPort > 0 {
		dialAPIPort = prevState.APIPort
		dialSecret = prevState.APISecret
	}
	forceRestart := forceRestartOverride || prevState.APIPort <= 0 || controllerChanged
	if c.applier != nil {
		if applyErr := c.applier.ApplyRuntimeConfig(ctx, settings, cfgDir, runtimePath, dialAPIPort, dialSecret, forceRestart); applyErr != nil {
			if hadPreviousGood {
				if goodBytes, rErr := os.ReadFile(lastGoodPath); rErr == nil {
					_ = profile.WriteFileAtomic(runtimePath, goodBytes, 0600)
					// Roll back with a fresh bounded context and prevState's EnhancedMode so a
					// cancelled UAC/timeout never re-prompts elevation or fails immediately on expired ctx.
					rbSettings := settings
					if prevState.APIPort > 0 || prevState.ProfileName != "" {
						rbSettings.EnhancedMode = prevState.Enhanced
					} else if goodRoot, gErr := profile.UnmarshalYAMLMap(goodBytes); gErr == nil {
						if tunMap, ok := goodRoot["tun"].(map[string]any); ok && tunMap != nil {
							rbSettings.EnhancedMode = boolFromYAML(tunMap["enable"])
						} else {
							rbSettings.EnhancedMode = false
						}
					}
					rbCtx, rbCancel := context.WithTimeout(context.Background(), 15*time.Second)
					rbErr := c.applier.ApplyRuntimeConfig(rbCtx, rbSettings, cfgDir, runtimePath, dialAPIPort, dialSecret, forceRestart)
					rbCancel()
					if rbErr == nil && prevState.APIPort == 0 {
						c.SeedActiveStateFromDisk(runtimePath)
						prevState = c.LastState()
					}
				}
			}
			return prevState, fmt.Errorf("apply runtime config failed (rolled back): %w", applyErr)
		}
	}

	_ = profile.WriteFileAtomic(lastGoodPath, candidateBytes, 0600)
	c.setLastState(state)
	return state, nil
}

func (c *Controller) prepareRuntimeTopology(root map[string]any, settings profile.AppSettings) (AppliedRuntimeState, error) {
	prevState := c.LastState()
	probe := c.portProbeForSettings(settings)
	state := AppliedRuntimeState{
		ProfileName: settings.SelectedProfile,
		Enhanced:    settings.EnhancedMode,
	}

	// Custom Config as-is (P2-2): preserve user's TUN/DNS/ports verbatim, only read back ports/secret!
	if settings.EnhancedMode && settings.CustomConfigAsIs {
		state.MixedPort = intFromYAML(root["mixed-port"])
		state.HTTPPort = intFromYAML(root["port"])
		if state.HTTPPort <= 0 {
			state.HTTPPort = state.MixedPort
		}
		state.SocksPort = intFromYAML(root["socks-port"])
		if state.SocksPort <= 0 {
			state.SocksPort = state.MixedPort
		}
		// Do not fabricate settings.APIPort when custom config omits external-controller.
		state.APIPort = extractPortFromHostPort(fmt.Sprint(root["external-controller"]), 0)
		state.requestedAPIPort = state.APIPort
		if sec, ok := root["secret"].(string); ok {
			state.APISecret = sec
		}
		if tunMap, ok := root["tun"].(map[string]any); ok && tunMap != nil {
			state.Enhanced = boolFromYAML(tunMap["enable"])
		} else {
			state.Enhanced = false
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
	ownedMixedPort := prevState.MixedPort
	if mixedPort != ownedMixedPort && !isPortUsableWith(probe, mixedPort, false) {
		if settings.PortOverride > 0 {
			return state, fmt.Errorf("configured proxy port %d is unavailable or occupied by another application", mixedPort)
		}
		if ownedMixedPort > 0 {
			mixedPort = ownedMixedPort
		} else {
			fallback, err := portprobe.FindAvailablePort(7890, 7999, nil, false, probe)
			if err != nil {
				return state, fmt.Errorf("select mixed-port: %w", err)
			}
			mixedPort = fallback
		}
		root["mixed-port"] = mixedPort
	}
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

	// Select API port (skipping Hyper-V/WinNAT WSAEACCES reserved ports if not already owned by us!).
	// Because Mihomo's PUT /configs reload does not rebind external-controller, preserve an
	// already-owned fallback API port across routine hot reloads when settings.APIPort did not change.
	requestedAPIPort := settings.APIPort
	if requestedAPIPort <= 0 {
		requestedAPIPort = 19090
	}
	apiPort := requestedAPIPort
	ownedAPIPort := prevState.APIPort
	prevRequestedAPIPort := prevState.requestedAPIPort
	if prevRequestedAPIPort <= 0 {
		prevRequestedAPIPort = ownedAPIPort
	}
	if ownedAPIPort > 0 && !reserved[ownedAPIPort] && requestedAPIPort == prevRequestedAPIPort {
		apiPort = ownedAPIPort
	} else if reserved[apiPort] || (apiPort != ownedAPIPort && !isPortUsableWith(probe, apiPort, false)) {
		if ownedAPIPort > 0 && !reserved[ownedAPIPort] {
			apiPort = ownedAPIPort
		} else {
			fallback, err := portprobe.FindAvailablePort(19090, 19999, reserved, false, probe)
			if err != nil {
				return state, fmt.Errorf("select external-controller port: %w", err)
			}
			apiPort = fallback
		}
	}
	reserved[apiPort] = true
	state.APIPort = apiPort
	state.requestedAPIPort = requestedAPIPort
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

	cfgDir := c.store.ActiveConfigDir()
	privateDirectRules := profile.ReadPrivateDirectRules(cfgDir)
	privateDNSPatterns := profile.ExtractPrivateDirectDNSPatterns(privateDirectRules)
	excludeCIDRs, excludeDomains := parseTunRouteExcludes(settings.TunRouteExcludes)

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
		ownedDNSPort := prevState.DNSPort
		if reserved[preferredDNS] || (preferredDNS != ownedDNSPort && !isPortUsableWith(probe, preferredDNS, true)) {
			if ownedDNSPort > 0 && !reserved[ownedDNSPort] {
				preferredDNS = ownedDNSPort
			} else {
				nextDNS, err := portprobe.FindAvailablePort(10530, 11530, reserved, true, probe)
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
		if strings.EqualFold(fmt.Sprint(dnsMap["enhanced-mode"]), "fake-ip") {
			filterMode, _ := dnsMap["fake-ip-filter-mode"].(string)
			filterMode = strings.ToLower(strings.TrimSpace(filterMode))
			if filterMode == "" || filterMode == "blacklist" {
				additions := make([]string, 0, len(privateDNSPatterns)+len(excludeDomains)+len(captivePortalFakeIPFilter)+len(defaultOSExtraFakeIPFilter))
				additions = append(additions, privateDNSPatterns...)
				for _, d := range excludeDomains {
					additions = append(additions, d.dnsPattern)
				}
				additions = append(additions, captivePortalFakeIPFilter...)
				if dnsMap["fake-ip-filter"] == nil {
					additions = append(additions, defaultOSExtraFakeIPFilter...)
				}
				dnsMap["fake-ip-filter"] = mergeUniqueStringsFold(dnsMap["fake-ip-filter"], additions)
			}
		}
		if dnsMap["default-nameserver"] == nil {
			dnsMap["default-nameserver"] = []string{"114.114.114.114", "223.5.5.5", "119.29.29.29", "system"}
		}
		if dnsMap["nameserver"] == nil {
			dnsMap["nameserver"] = []string{"https://doh.pub/dns-query", "tls://223.5.5.5:853"}
		}
		policyMap, _ := dnsMap["nameserver-policy"].(map[string]any)
		if policyMap == nil {
			if norm, ok := profile.NormalizeYAMLValue(dnsMap["nameserver-policy"]).(map[string]any); ok && norm != nil {
				policyMap = norm
			} else {
				policyMap = map[string]any{}
			}
		}
		for _, k := range captivePortalSystemDNSPolicyKeys {
			if !hasMapKeyFold(policyMap, k) {
				policyMap[k] = "system"
			}
		}
		for _, k := range privateDNSPatterns {
			if !hasMapKeyFold(policyMap, k) {
				policyMap[k] = "system"
			}
		}
		for _, d := range excludeDomains {
			if !hasMapKeyFold(policyMap, d.dnsPattern) {
				policyMap[d.dnsPattern] = "system"
			}
		}
		dnsMap["nameserver-policy"] = policyMap
		if dnsMap["direct-nameserver"] != nil && dnsMap["direct-nameserver-follow-policy"] == nil {
			dnsMap["direct-nameserver-follow-policy"] = true
		}
		root["dns"] = dnsMap
	}

	// Register editable file-backed clashfx-private-direct.list (plus optional inline
	// clashfx-tun-excludes when the user configured settings.TunRouteExcludes).
	profile.SetFileClassicalRuleProvider(
		root,
		profile.PrivateDirectRuleProviderName,
		profile.BuiltinRulesetRelPathInDir(cfgDir, profile.PrivateDirectRuleProviderName),
	)
	leadingPrivateRules := []string{
		"RULE-SET," + profile.PrivateDirectRuleProviderName + ",DIRECT",
	}
	if tunExcludesPayload := buildTunExcludesPayload(excludeCIDRs, excludeDomains); len(tunExcludesPayload) > 0 {
		profile.SetInlineClassicalRuleProvider(root, profile.TunExcludesRuleProviderName, tunExcludesPayload)
		leadingPrivateRules = append(leadingPrivateRules, "RULE-SET,"+profile.TunExcludesRuleProviderName+",DIRECT")
	}

	captiveDirectRules := []string{
		"DOMAIN,captive.apple.com,DIRECT",
		"DOMAIN-SUFFIX,msftconnecttest.com,DIRECT",
		"DOMAIN-SUFFIX,msftncsi.com,DIRECT",
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
			"stack":                 stack,
			"auto-route":            true,
			"auto-detect-interface": iface == "",
			"strict-route":          true,
			"dns-hijack":            []string{"any:53", "tcp://any:53"},
			"mtu":                   mtu,
		}
		// Darwin sing-tun rejects non-utun<N> device names; omit device so Mihomo auto-allocates utunN.
		if runtime.GOOS != "darwin" {
			tunMap["device"] = "ClashFX"
		}
		if iface != "" {
			root["interface-name"] = iface
		}
		if len(excludeCIDRs) > 0 {
			tunMap["route-exclude-address"] = excludeCIDRs
		}
		root["tun"] = tunMap
		if !settings.CustomConfigAsIs && root["find-process-mode"] == nil {
			root["find-process-mode"] = "always"
		}
		tunCaptiveRules := captiveDirectRules
		switch runtime.GOOS {
		case "darwin":
			tunCaptiveRules = append([]string{
				"PROCESS-NAME,captiveagent,DIRECT",
				"PROCESS-NAME,Captive Network Assistant,DIRECT",
			}, captiveDirectRules...)
		case "windows":
			tunCaptiveRules = append([]string{
				"PROCESS-NAME,WWAHost.exe,DIRECT",
			}, captiveDirectRules...)
		}
		PrependUniqueRules(root, tunCaptiveRules)
		// Keep mihomo_core loop guard and configurable private/LAN/intranet direct rules
		// at the top (ahead of AI Proxy Lock) so intranet domains never get Fake-IP or remote proxied.
		profile.SetInlineClassicalRuleProvider(root, profile.InternalDirectRuleProviderName, []string{
			"PROCESS-NAME,mihomo_core.exe",
			"PROCESS-NAME,mihomo_core",
		})
		PrependUniqueRules(root, append([]string{
			"RULE-SET," + profile.InternalDirectRuleProviderName + ",DIRECT",
		}, leadingPrivateRules...))
	} else {
		root["tun"] = map[string]any{"enable": false}
		PrependUniqueRules(root, captiveDirectRules)
		PrependUniqueRules(root, leadingPrivateRules)
	}

	// Strip any legacy custom recovery key; stock Mihomo persists selections via profile.store-selected + REST replay.
	delete(root, profile.RecoverySelectionsKey)

	return state, nil
}

func (c *Controller) portProbeForSettings(settings profile.AppSettings) func(port int, checkUDP bool) (bool, bool) {
	if c.customPortProbe {
		return c.probePort
	}
	if settings.EnhancedMode && runtime.GOOS == "darwin" {
		return portprobe.ProbeLoopbackPortForPrivilegedCore
	}
	return c.probePort
}

func isPortUsableWith(probe func(port int, checkUDP bool) (bool, bool), port int, checkUDP bool) bool {
	if probe == nil {
		return true
	}
	ok, _ := probe(port, checkUDP)
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

type excludedDomainSpec struct {
	dnsPattern    string
	classicalRule string
}

func parseTunRouteExcludes(raw string) (cidrs []string, domains []excludedDomainSpec) {
	seenCIDR := map[string]bool{}
	seenDomain := map[string]bool{}
	for _, item := range splitTrimmedCSV(raw) {
		if pfx, err := netip.ParsePrefix(item); err == nil {
			norm := pfx.Masked().String()
			if !seenCIDR[norm] {
				seenCIDR[norm] = true
				cidrs = append(cidrs, norm)
			}
			continue
		}
		if addr, err := netip.ParseAddr(item); err == nil {
			addr = addr.Unmap()
			bits := 128
			if addr.Is4() {
				bits = 32
			}
			norm := netip.PrefixFrom(addr, bits).String()
			if !seenCIDR[norm] {
				seenCIDR[norm] = true
				cidrs = append(cidrs, norm)
			}
			continue
		}
		if spec, ok := normalizeExcludeDomain(item); ok {
			key := strings.ToLower(spec.dnsPattern)
			if !seenDomain[key] {
				seenDomain[key] = true
				domains = append(domains, spec)
			}
		}
	}
	return cidrs, domains
}

func normalizeExcludeDomain(raw string) (excludedDomainSpec, bool) {
	s := strings.TrimSpace(raw)
	s = strings.TrimRight(s, ".")
	if s == "" || s == "*" || s == "+" || strings.ContainsAny(s, " \t\r\n,/:") || strings.Contains(s, "..") {
		return excludedDomainSpec{}, false
	}
	if strings.HasPrefix(s, "*.") {
		rest := strings.TrimLeft(strings.TrimPrefix(s, "*."), ".")
		if rest == "" {
			return excludedDomainSpec{}, false
		}
		if !strings.Contains(rest, "*") {
			return excludedDomainSpec{
				dnsPattern:    "+." + rest,
				classicalRule: "DOMAIN-SUFFIX," + rest,
			}, true
		}
		return excludedDomainSpec{
			dnsPattern:    s,
			classicalRule: "DOMAIN-WILDCARD," + s,
		}, true
	}
	if strings.HasPrefix(s, "+.") || strings.HasPrefix(s, ".") {
		rest := strings.TrimLeft(s, "+.")
		if rest == "" || strings.Contains(rest, "*") {
			return excludedDomainSpec{}, false
		}
		return excludedDomainSpec{
			dnsPattern:    "+." + rest,
			classicalRule: "DOMAIN-SUFFIX," + rest,
		}, true
	}
	if strings.Contains(s, "*") {
		return excludedDomainSpec{
			dnsPattern:    s,
			classicalRule: "DOMAIN-WILDCARD," + s,
		}, true
	}
	return excludedDomainSpec{
		dnsPattern:    s,
		classicalRule: "DOMAIN," + s,
	}, true
}

func buildTunExcludesPayload(excludeCIDRs []string, excludeDomains []excludedDomainSpec) []string {
	if len(excludeCIDRs) == 0 && len(excludeDomains) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(excludeCIDRs)+len(excludeDomains))
	out := make([]string, 0, len(excludeCIDRs)+len(excludeDomains))
	add := func(line string) {
		key := strings.ToLower(strings.TrimSpace(line))
		if key == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, line)
	}
	for _, c := range excludeCIDRs {
		if strings.Contains(c, ":") {
			add("IP-CIDR6," + c + ",no-resolve")
		} else {
			add("IP-CIDR," + c + ",no-resolve")
		}
	}
	for _, d := range excludeDomains {
		add(d.classicalRule)
	}
	return out
}

func mergeUniqueStringsFold(existing any, additions []string) []any {
	var cur []string
	switch v := existing.(type) {
	case []string:
		cur = v
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				cur = append(cur, s)
			}
		}
	}
	seen := make(map[string]bool, len(cur)+len(additions))
	out := make([]any, 0, len(cur)+len(additions))
	add := func(s string) {
		trimmed := strings.TrimSpace(s)
		if trimmed == "" {
			return
		}
		key := strings.ToLower(trimmed)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, trimmed)
	}
	for _, s := range cur {
		add(s)
	}
	for _, s := range additions {
		add(s)
	}
	return out
}

func hasMapKeyFold(m map[string]any, key string) bool {
	for k := range m {
		if strings.EqualFold(k, key) {
			return true
		}
	}
	return false
}
