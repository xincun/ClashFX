package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/clashfx/core-engine/internal/ailock"
	"github.com/clashfx/core-engine/internal/features"
	"github.com/clashfx/core-engine/internal/nodeservices"
	"github.com/clashfx/core-engine/internal/profile"
	"github.com/clashfx/core-engine/internal/service"
	"github.com/clashfx/core-engine/internal/subscription"
	"github.com/clashfx/core-engine/internal/sysproxy"
	"github.com/clashfx/core-engine/internal/transaction"
)

const AppVersion = "1.19.24-win.1"

var ErrServiceInstallRequired = errors.New("ClashFXService is not installed yet")

type Config struct {
	AppDataDir         string
	BundledResourceDir string
	DashboardUIDir     string
	BinDir             string // directory containing mihomo_core.exe, clashfx-service.exe, clashfx-ui.exe
}

type ProxyItemView struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	DelayMs int    `json:"delay_ms"`
	Tested  bool   `json:"tested"`
}

type ProxyGroupView struct {
	Name              string          `json:"name"`
	Type              string          `json:"type"`
	Now               string          `json:"now"`
	TestURL           string          `json:"test_url,omitempty"`
	HasManualOverride bool            `json:"has_manual_override"`
	Items             []ProxyItemView `json:"items"`
}

type EffectiveRuleItem struct {
	Index     int    `json:"index"`
	Type      string `json:"type"`
	Payload   string `json:"payload"`
	Proxy     string `json:"proxy"`
	Size      int    `json:"size"`
	HitCount  uint64 `json:"hit_count"`
	MissCount uint64 `json:"miss_count"`
	Disabled  bool   `json:"disabled"`
	Source    string `json:"source,omitempty"`
}

type RuleProviderView struct {
	Name        string   `json:"name"`
	Behavior    string   `json:"behavior"`
	VehicleType string   `json:"vehicle_type"`
	RuleCount   int      `json:"rule_count"`
	Payload     []string `json:"payload,omitempty"`
}

type EffectiveRulesView struct {
	OutboundMode  string                      `json:"outbound_mode"`
	DefaultTarget string                      `json:"default_target"`
	TotalRules    int                         `json:"total_rules"`
	Rules         []EffectiveRuleItem         `json:"rules"`
	RuleProviders map[string]RuleProviderView `json:"rule_providers"`
}

type DaemonSnapshot struct {
	Version                string                             `json:"version"`
	Arch                   string                             `json:"arch"`
	CoreRunning            bool                               `json:"core_running"`
	CoreViaService         bool                               `json:"core_via_service"`
	ServiceInstalled       bool                               `json:"service_installed"`
	Settings               profile.AppSettings                `json:"settings"`
	Runtime                transaction.AppliedRuntimeState    `json:"runtime"`
	DashboardURL           string                             `json:"dashboard_url"`
	Profiles               []string                           `json:"profiles"`
	ProfileMixinPath       string                             `json:"profile_mixin_path"`
	ConfigDir              string                             `json:"config_dir"`
	LogsDir                string                             `json:"logs_dir"`
	ProxyGroups            []ProxyGroupView                   `json:"proxy_groups"`
	DefaultRuleTarget      string                             `json:"default_rule_target,omitempty"`
	AILockCandidates       []ailock.CandidateNode             `json:"ai_lock_candidates"`
	AILockRelay            string                             `json:"ai_lock_relay,omitempty"`
	Subscriptions          []subscription.ManagedSubscription `json:"subscriptions"`
	ActiveSubscriptionInfo *subscription.UserInfo             `json:"active_subscription_info,omitempty"`
	NodeServices           nodeservices.RuntimeSnapshot       `json:"node_services"`
	ActiveNetwork          features.ActiveNetworkSignature    `json:"active_network"`
	SuspendedByNetwork     string                             `json:"suspended_by_network,omitempty"`
	LastError              string                             `json:"last_error,omitempty"`
}

type Engine struct {
	cfg          Config
	store        *profile.Store
	subMgr       *subscription.Manager
	sysProxy     *sysproxy.Manager
	nodeStore    *nodeservices.Store
	nodeCoord    *nodeservices.Coordinator
	ctrl         *transaction.Controller
	localRunner  *service.Runner
	coreHelper   subscription.BinaryCoreHelper

	opMu              sync.Mutex
	coreViaService    bool
	allowPromptUAC    bool
	suspendedByNet    string
	lastApplyErr      string
	proxyGroupsMu     sync.RWMutex
	proxyGroups       []ProxyGroupView
	defaultRuleTarget string
	aiCandidates      []ailock.CandidateNode
	aiRelay           string
	providerSubInfo   *subscription.UserInfo

	stopBgCh   chan struct{}
	stopPower  func()
	onPush     func()
}

func NewEngine(cfg Config, onPush func()) (*Engine, error) {
	store, err := profile.NewStore(cfg.AppDataDir, cfg.BundledResourceDir)
	if err != nil {
		return nil, err
	}

	coreExeName := "mihomo_core"
	if runtime.GOOS == "windows" {
		coreExeName = "mihomo_core.exe"
	}
	coreExePath := filepath.Join(cfg.BinDir, coreExeName)
	var safePaths []string
	if strings.TrimSpace(cfg.DashboardUIDir) != "" {
		safePaths = append(safePaths, cfg.DashboardUIDir)
	}
	if strings.TrimSpace(cfg.BundledResourceDir) != "" {
		safePaths = append(safePaths, cfg.BundledResourceDir)
	}
	helper := subscription.BinaryCoreHelper{
		CoreBinaryPath: coreExePath,
		SafePaths:      safePaths,
	}

	subMgr, err := subscription.NewManager(store, helper)
	if err != nil {
		store.Close()
		return nil, err
	}

	sysProxy := sysproxy.NewManager(store.AppDataDir(), nil)
	nodeStore := nodeservices.NewStore(store.AppDataDir())
	localRunner := service.NewRunner(cfg.BinDir)

	eng := &Engine{
		cfg:         cfg,
		store:       store,
		subMgr:      subMgr,
		sysProxy:    sysProxy,
		nodeStore:   nodeStore,
		localRunner: localRunner,
		coreHelper:  helper,
		stopBgCh:    make(chan struct{}),
		onPush:      onPush,
	}

	eng.ctrl = transaction.NewController(
		store,
		eng,
		cfg.DashboardUIDir,
		ailock.TransformHookForController(nil),
		features.TransformHookForCNApps(),
		features.TransformHookForCustomRules(store.ActiveConfigDir),
		nodeStore.TransformHookForController(eng.IsCoreRunning),
	)

	eng.nodeCoord = nodeservices.NewCoordinator(
		nodeStore,
		func() nodeservices.CoreAPIConfig {
			st := eng.ctrl.LastState()
			return nodeservices.CoreAPIConfig{
				ProfileName: store.Settings().SelectedProfile,
				APIPort:     st.APIPort,
				APISecret:   st.APISecret,
				CoreRunning: eng.IsCoreRunning(),
			}
		},
		func(ctx context.Context, profileName string, directPort int) error {
			if _, err := eng.subMgr.Refresh(ctx, profileName, directPort); err != nil {
				return err
			}
			_, err := eng.applyAndSync(ctx, false, false)
			return err
		},
		eng.notifyStateChanged,
	)

	return eng, nil
}

// Start boots the proxy core, recovers any crashed system proxy snapshot, starts
// file watchers, power-resume self-healing, subscription timers, and node watchdog.
func (e *Engine) Start(ctx context.Context) error {
	settings := e.store.Settings()
	if settings.AILockEnabled {
		_, _ = e.store.UpdateSettings(func(s *profile.AppSettings) error {
			ailock.EnforceProtectedSettings(s)
			return nil
		})
		settings = e.store.Settings()
	}

	// Recover previous crashed session's system proxy if user doesn't want system proxy now
	_, _ = e.sysProxy.RecoverAfterUncleanShutdown(settings.SystemProxyEnabled)

	// If EnhancedMode was persisted from last session, verify service is still installed;
	// if uninstalled externally while offline (and AI Lock isn't forcing it), fall back cleanly to normal mode.
	if settings.EnhancedMode && runtime.GOOS == "windows" && !IsPrivilegedServiceInstalled(service.ServiceName) && !settings.AILockEnabled {
		_, _ = e.store.UpdateSettings(func(s *profile.AppSettings) error {
			s.EnhancedMode = false
			return nil
		})
	}

	if _, err := e.applyAndSync(ctx, true, false); err != nil {
		// Log/expose startup error in state without crashing daemon IPC so UI can still open & fix config!
		fmt.Fprintf(os.Stderr, "[clashfx-daemon] initial core apply warning: %v\n", err)
	}

	_ = e.store.WatchActiveConfigDir(func(_ string) {
		_, _ = e.applyAndSync(context.Background(), false, false)
	})
	e.stopPower = StartPowerResumeMonitor(func() {
		time.Sleep(1200 * time.Millisecond)
		e.handleWakeOrNetworkCheck()
	})
	e.nodeCoord.StartBackgroundLoop()
	go e.backgroundMaintenanceLoop()

	return nil
}

func (e *Engine) Shutdown() {
	select {
	case <-e.stopBgCh:
		return
	default:
		close(e.stopBgCh)
	}

	if e.stopPower != nil {
		e.stopPower()
	}
	e.nodeCoord.Stop()
	e.store.Close()

	settings := e.store.Settings()
	_ = e.sysProxy.Shutdown(settings.AILockEnabled)

	_ = e.localRunner.Stop()
	if e.coreViaService {
		_, _ = callService(5*time.Second, service.Request{Action: "stop_core"})
	}
}

func callService(timeout time.Duration, req service.Request) (service.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return service.Call(ctx, req)
}

// ValidateConfig implements transaction.CoreApplier.
func (e *Engine) ValidateConfig(homeDir, configPath string) error {
	return e.coreHelper.ValidateConfig(homeDir, configPath)
}

// ApplyRuntimeConfig implements transaction.CoreApplier, transparently routing
// normal mode through localRunner and Enhanced TUN mode through ClashFXService.
func (e *Engine) ApplyRuntimeConfig(ctx context.Context, settings profile.AppSettings, homeDir, runtimeConfigPath string, apiPort int, secret string) error {
	wantService := settings.EnhancedMode && runtime.GOOS == "windows"

	if wantService {
		_ = e.localRunner.Stop()
		if !IsPrivilegedServiceInstalled(service.ServiceName) {
			if !e.allowPromptUAC {
				return ErrServiceInstallRequired
			}
			svcExe := filepath.Join(e.cfg.BinDir, "clashfx-service.exe")
			if err := ElevateAndInstallService(svcExe); err != nil {
				return err
			}
		}
		if e.coreViaService && e.isServiceCoreRunning() {
			if err := transaction.ReloadViaREST(ctx, apiPort, secret, runtimeConfigPath); err == nil {
				return nil
			}
		}
		resp, err := callService(15*time.Second, service.Request{
			Action:     "start_core",
			HomeDir:    homeDir,
			ConfigFile: runtimeConfigPath,
			ExtUI:      e.cfg.DashboardUIDir,
		})
		if err != nil {
			return err
		}
		if !resp.OK {
			return fmt.Errorf("ClashFXService start_core failed: %s", resp.Message)
		}
		e.coreViaService = true
		return nil
	}

	// Normal (non-elevated) mode
	if e.coreViaService {
		_, _ = callService(6*time.Second, service.Request{Action: "stop_core"})
		e.coreViaService = false
	}
	if e.localRunner.Status().Running {
		if err := transaction.ReloadViaREST(ctx, apiPort, secret, runtimeConfigPath); err == nil {
			return nil
		}
	}
	_, err := e.localRunner.Start(homeDir, runtimeConfigPath, e.cfg.DashboardUIDir)
	return err
}

func (e *Engine) IsCoreRunning() bool {
	if e.coreViaService {
		return e.isServiceCoreRunning()
	}
	return e.localRunner.Status().Running
}

func (e *Engine) isServiceCoreRunning() bool {
	resp, err := callService(2*time.Second, service.Request{Action: "status"})
	return err == nil && resp.OK && resp.Status != nil && resp.Status.Running
}

func (e *Engine) applyAndSync(ctx context.Context, includeRecovery bool, allowUAC bool) (transaction.AppliedRuntimeState, error) {
	e.opMu.Lock()
	e.allowPromptUAC = allowUAC
	prevViaService := e.coreViaService
	wantService := e.store.Settings().EnhancedMode && runtime.GOOS == "windows"
	if prevViaService != wantService {
		e.ctrl.ResetActiveState()
	}
	state, err := e.ctrl.Apply(ctx, includeRecovery)
	e.allowPromptUAC = false
	if err != nil {
		e.lastApplyErr = err.Error()
	} else {
		e.lastApplyErr = ""
	}
	e.opMu.Unlock()

	if err != nil {
		e.notifyStateChanged()
		return state, err
	}

	if includeRecovery && state.APIPort > 0 && e.IsCoreRunning() {
		e.replaySavedSelections(ctx, state.APIPort, state.APISecret, e.store.Settings().SelectedProfile)
	}

	e.reconcileSystemProxy()
	e.refreshLiveCoreMetadata(ctx)
	e.notifyStateChanged()
	return state, nil
}

func (e *Engine) replaySavedSelections(ctx context.Context, apiPort int, secret string, profileName string) {
	saved := e.store.RecoverySelectionsForProfile(profileName)
	if len(saved) == 0 {
		return
	}
	client := &http.Client{Timeout: 3 * time.Second}
	for group, target := range saved {
		body, _ := json.Marshal(map[string]string{"name": target})
		req, err := http.NewRequestWithContext(
			ctx,
			http.MethodPut,
			fmt.Sprintf("http://127.0.0.1:%d/proxies/%s", apiPort, url.PathEscape(group)),
			bytes.NewReader(body),
		)
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		if secret != "" {
			req.Header.Set("Authorization", "Bearer "+secret)
		}
		if resp, doErr := client.Do(req); doErr == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}
}

func (e *Engine) reconcileSystemProxy() {
	settings := e.store.Settings()
	rt := e.ctrl.LastState()
	sig := features.DetectActiveNetworkSignature()
	matchedRule, shouldSuspend := features.MatchSuspendNetwork(sig, settings.SuspendNetworks)
	if settings.AILockEnabled {
		// AI Proxy Lock never suspends system proxy on network matches!
		shouldSuspend = false
		matchedRule = ""
	}

	e.opMu.Lock()
	e.suspendedByNet = matchedRule
	e.opMu.Unlock()

	if settings.SystemProxyEnabled && !shouldSuspend && rt.HTTPPort > 0 {
		_, _ = e.sysProxy.ReconcileIfExpected(true, rt.HTTPPort, settings.ProxyBypassList)
	} else {
		_ = e.sysProxy.Restore()
	}
}

func (e *Engine) handleWakeOrNetworkCheck() {
	settings := e.store.Settings()
	if !e.IsCoreRunning() {
		_, _ = e.applyAndSync(context.Background(), true, false)
		return
	}
	if settings.SystemProxyEnabled {
		e.reconcileSystemProxy()
	}
	e.notifyStateChanged()
}

func (e *Engine) backgroundMaintenanceLoop() {
	coreSyncTicker := time.NewTicker(3 * time.Second)
	netTicker := time.NewTicker(10 * time.Second)
	subTicker := time.NewTicker(60 * time.Second)
	defer coreSyncTicker.Stop()
	defer netTicker.Stop()
	defer subTicker.Stop()

	var lastAutoSubAt time.Time
	for {
		select {
		case <-e.stopBgCh:
			return
		case <-coreSyncTicker.C:
			e.syncFromLiveCore(context.Background())
		case <-netTicker.C:
			prevSuspended := e.suspendedByNet
			e.reconcileSystemProxy()
			if e.suspendedByNet != prevSuspended {
				e.notifyStateChanged()
			}
		case <-subTicker.C:
			st := e.store.Settings()
			if !st.AutoUpdateSubs || st.AutoUpdateIntervalSec < 60 {
				continue
			}
			if !lastAutoSubAt.IsZero() && time.Since(lastAutoSubAt) < time.Duration(st.AutoUpdateIntervalSec)*time.Second {
				continue
			}
			lastAutoSubAt = time.Now()
			for _, sub := range e.subMgr.List() {
				_, _ = e.subMgr.Refresh(context.Background(), sub.Name, 0)
			}
			_, _ = e.applyAndSync(context.Background(), false, false)
		}
	}
}

func (e *Engine) syncFromLiveCore(ctx context.Context) {
	if !e.opMu.TryLock() {
		return
	}
	e.opMu.Unlock()

	rt := e.ctrl.LastState()
	if rt.APIPort <= 0 || !e.IsCoreRunning() {
		return
	}

	settings := e.store.Settings()
	anyChanged := false

	// 1. Reconcile outbound mode if user changed it inside MetaCubeXD WebView
	if liveMode := fetchCoreOutboundMode(ctx, rt.APIPort, rt.APISecret); liveMode != "" {
		if settings.AILockEnabled && liveMode != "rule" {
			// Enforce AI Proxy Lock invariant if WebView tried to switch away from Rule mode
			patchCoreOutboundMode(ctx, rt.APIPort, rt.APISecret, "rule")
		} else if !settings.AILockEnabled && (liveMode == "rule" || liveMode == "global" || liveMode == "direct") &&
			liveMode != strings.ToLower(strings.TrimSpace(settings.OutboundMode)) {
			if _, err := e.store.UpdateSettings(func(s *profile.AppSettings) error {
				s.OutboundMode = liveMode
				return nil
			}); err == nil {
				settings = e.store.Settings()
				e.reconcileSystemProxy()
				anyChanged = true
			}
		}
	}

	// 2. Reconcile live proxy group selections & delays from WebView / auto-URLTest
	var rawRoot map[string]any
	if data, err := os.ReadFile(e.store.ProfilePath(settings.SelectedProfile)); err == nil {
		rawRoot, _ = profile.UnmarshalYAMLMap(data)
	}
	e.proxyGroupsMu.RLock()
	prevGroups := append([]ProxyGroupView(nil), e.proxyGroups...)
	prevProvInfo := e.providerSubInfo
	e.proxyGroupsMu.RUnlock()

	prevNowMap := make(map[string]string, len(prevGroups))
	for _, pg := range prevGroups {
		prevNowMap[pg.Name] = pg.Now
	}

	groups := fetchProxyGroupsFromCore(ctx, rt.APIPort, rt.APISecret, rawRoot, e.store.RecoverySelectionsForProfile(settings.SelectedProfile))
	if len(groups) > 0 {
		for i := range groups {
			g := &groups[i]
			prevNow := prevNowMap[g.Name]
			if strings.EqualFold(g.Type, "Selector") && g.Now != "" && prevNow != "" && g.Now != prevNow {
				if g.Name != "GLOBAL" || strings.EqualFold(settings.OutboundMode, "global") {
					_ = e.store.RememberProxySelection(settings.SelectedProfile, g.Name, g.Now)
					g.HasManualOverride = true
				}
			}
		}
	}
	provInfo := fetchProviderSubscriptionInfo(ctx, rt.APIPort, rt.APISecret)

	if len(groups) > 0 && (!reflect.DeepEqual(prevGroups, groups) || !reflect.DeepEqual(prevProvInfo, provInfo)) {
		e.proxyGroupsMu.Lock()
		e.proxyGroups = groups
		e.providerSubInfo = provInfo
		e.proxyGroupsMu.Unlock()
		anyChanged = true
	}

	if anyChanged {
		e.notifyStateChanged()
	}
}

func fetchCoreOutboundMode(ctx context.Context, apiPort int, secret string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/configs", apiPort), nil)
	if err != nil {
		return ""
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	resp, err := (&http.Client{Timeout: 1500 * time.Millisecond}).Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var doc struct {
		Mode string `json:"mode"`
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if json.Unmarshal(body, &doc) != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(doc.Mode))
}

func patchCoreOutboundMode(ctx context.Context, apiPort int, secret, mode string) {
	body, _ := json.Marshal(map[string]string{"mode": mode})
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, fmt.Sprintf("http://127.0.0.1:%d/configs", apiPort), bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	if resp, doErr := (&http.Client{Timeout: 2 * time.Second}).Do(req); doErr == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}

func fetchProviderSubscriptionInfo(ctx context.Context, apiPort int, secret string) *subscription.UserInfo {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/providers/proxies", apiPort), nil)
	if err != nil {
		return nil
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var payload struct {
		Providers map[string]struct {
			VehicleType      string `json:"vehicleType"`
			SubscriptionInfo *struct {
				Upload   int64 `json:"Upload"`
				Download int64 `json:"Download"`
				Total    int64 `json:"Total"`
				Expire   int64 `json:"Expire"`
			} `json:"subscriptionInfo"`
		} `json:"providers"`
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if json.Unmarshal(body, &payload) != nil {
		return nil
	}
	var names []string
	for k := range payload.Providers {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		prov := payload.Providers[k]
		if strings.EqualFold(prov.VehicleType, "Compatible") || prov.SubscriptionInfo == nil {
			continue
		}
		si := prov.SubscriptionInfo
		if si.Total <= 0 && si.Upload <= 0 && si.Download <= 0 && si.Expire <= 0 {
			continue
		}
		out := &subscription.UserInfo{}
		if si.Upload > 0 || si.Download > 0 || si.Total > 0 {
			u := si.Upload
			d := si.Download
			out.Upload = &u
			out.Download = &d
		}
		if si.Total > 0 {
			t := si.Total
			out.Total = &t
		}
		if si.Expire > 0 {
			ex := si.Expire
			out.Expire = &ex
		}
		if out.HasAny() {
			return out
		}
	}
	return nil
}

func (e *Engine) Snapshot() DaemonSnapshot {
	settings := e.store.Settings()
	rt := e.ctrl.LastState()
	profiles, _ := e.store.ListProfiles()
	nsSnap, _ := e.nodeCoord.Snapshot(settings.SelectedProfile)
	sig := features.DetectActiveNetworkSignature()

	e.proxyGroupsMu.RLock()
	groups := append([]ProxyGroupView(nil), e.proxyGroups...)
	defTarget := e.defaultRuleTarget
	cands := append([]ailock.CandidateNode(nil), e.aiCandidates...)
	relay := e.aiRelay
	provSubInfo := e.providerSubInfo
	e.proxyGroupsMu.RUnlock()

	var activeSubInfo *subscription.UserInfo
	if sub, ok := e.subMgr.FindByName(settings.SelectedProfile); ok && sub.UserInfo != nil && sub.UserInfo.HasAny() {
		copied := *sub.UserInfo
		activeSubInfo = &copied
	} else if provSubInfo != nil && provSubInfo.HasAny() {
		copied := *provSubInfo
		activeSubInfo = &copied
	}

	e.opMu.Lock()
	suspended := e.suspendedByNet
	viaSvc := e.coreViaService
	lastErr := e.lastApplyErr
	e.opMu.Unlock()

	logsDir := filepath.Join(e.store.AppDataDir(), "logs")
	_ = os.MkdirAll(logsDir, 0700)

	return DaemonSnapshot{
		Version:                AppVersion,
		Arch:                   runtime.GOARCH,
		CoreRunning:            e.IsCoreRunning(),
		CoreViaService:         viaSvc,
		ServiceInstalled:       IsPrivilegedServiceInstalled(service.ServiceName),
		Settings:               settings,
		Runtime:                rt,
		DashboardURL:           buildDashboardURL(rt.APIPort, rt.APISecret),
		Profiles:               profiles,
		ProfileMixinPath:       e.store.ProfileMixinPath(),
		ConfigDir:              e.store.ActiveConfigDir(),
		LogsDir:                logsDir,
		ProxyGroups:            groups,
		DefaultRuleTarget:      defTarget,
		AILockCandidates:       cands,
		AILockRelay:            relay,
		Subscriptions:          e.subMgr.List(),
		ActiveSubscriptionInfo: activeSubInfo,
		NodeServices:           nsSnap,
		ActiveNetwork:          sig,
		SuspendedByNetwork:     suspended,
		LastError:              lastErr,
	}
}

func buildDashboardURL(apiPort int, secret string) string {
	if apiPort <= 0 {
		return ""
	}
	base := fmt.Sprintf("http://127.0.0.1:%d/ui/#/setup?hostname=127.0.0.1&port=%d&http=true", apiPort, apiPort)
	if secret != "" {
		base += "&secret=" + url.QueryEscape(secret)
	}
	return base
}

func (e *Engine) refreshLiveCoreMetadata(ctx context.Context) {
	rt := e.ctrl.LastState()
	settings := e.store.Settings()

	// Load candidate targets + relay from selected profile YAML even if core isn't up
	var rawRoot map[string]any
	if data, err := os.ReadFile(e.store.ProfilePath(settings.SelectedProfile)); err == nil {
		rawRoot, _ = profile.UnmarshalYAMLMap(data)
	}
	var cands []ailock.CandidateNode
	var relay string
	defTarget := "DIRECT"
	if rawRoot != nil {
		cands = ailock.ListCandidateTargets(rawRoot, nil)
		defTarget = features.ResolveDefaultRuleTarget(rawRoot)
		if settings.AILockEnabled {
			relay = ailock.RelayName(rawRoot, settings.AILockTarget, nil)
		}
	}

	var groups []ProxyGroupView
	var provInfo *subscription.UserInfo
	if rt.APIPort > 0 && e.IsCoreRunning() {
		groups = fetchProxyGroupsFromCore(ctx, rt.APIPort, rt.APISecret, rawRoot, e.store.RecoverySelectionsForProfile(settings.SelectedProfile))
		provInfo = fetchProviderSubscriptionInfo(ctx, rt.APIPort, rt.APISecret)
	}

	e.proxyGroupsMu.Lock()
	e.proxyGroups = groups
	e.defaultRuleTarget = defTarget
	e.aiCandidates = cands
	e.aiRelay = relay
	e.providerSubInfo = provInfo
	e.proxyGroupsMu.Unlock()
}

func fetchProxyGroupsFromCore(
	ctx context.Context,
	apiPort int,
	secret string,
	rawRoot map[string]any,
	savedSel map[string]string,
) []ProxyGroupView {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/proxies", apiPort), nil)
	if err != nil {
		return nil
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var payload struct {
		Proxies map[string]struct {
			Name    string   `json:"name"`
			Type    string   `json:"type"`
			Now     string   `json:"now"`
			Hidden  bool     `json:"hidden"`
			TestURL string   `json:"testUrl"`
			All     []string `json:"all"`
			History []struct {
				Delay int `json:"delay"`
			} `json:"history"`
		} `json:"proxies"`
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if json.Unmarshal(body, &payload) != nil {
		return nil
	}

	delayOf := func(name string) (int, bool) {
		p, ok := payload.Proxies[name]
		if !ok || len(p.History) == 0 {
			return 0, false
		}
		return p.History[len(p.History)-1].Delay, true
	}

	// Preserve profile YAML declaration order for groups!
	var orderedNames []string
	seen := map[string]bool{}
	if rawRoot != nil {
		if rawGroups, ok := rawRoot["proxy-groups"].([]any); ok {
			for _, g := range rawGroups {
				if gm, ok := g.(map[string]any); ok {
					if n, _ := gm["name"].(string); n != "" && !seen[n] {
						seen[n] = true
						orderedNames = append(orderedNames, n)
					}
				}
			}
		}
	}
	var extras []string
	for name, px := range payload.Proxies {
		if len(px.All) > 0 && !px.Hidden && name != "GLOBAL" && !seen[name] {
			extras = append(extras, name)
		}
	}
	sort.Strings(extras)
	orderedNames = append(orderedNames, extras...)
	if g, hasGlobal := payload.Proxies["GLOBAL"]; hasGlobal && len(g.All) > 0 && !g.Hidden && !seen["GLOBAL"] {
		orderedNames = append(orderedNames, "GLOBAL")
	}

	var out []ProxyGroupView
	for _, gName := range orderedNames {
		p, ok := payload.Proxies[gName]
		if !ok || len(p.All) == 0 || p.Hidden {
			continue
		}
		gv := ProxyGroupView{
			Name:              gName,
			Type:              p.Type,
			Now:               p.Now,
			TestURL:           p.TestURL,
			HasManualOverride: savedSel[gName] != "",
			Items:             make([]ProxyItemView, 0, len(p.All)),
		}
		for _, itemName := range p.All {
			itemMeta := payload.Proxies[itemName]
			d, tested := delayOf(itemName)
			gv.Items = append(gv.Items, ProxyItemView{
				Name:    itemName,
				Type:    itemMeta.Type,
				DelayMs: d,
				Tested:  tested,
			})
		}
		out = append(out, gv)
	}
	return out
}

func (e *Engine) closeAllConnections(ctx context.Context) {
	rt := e.ctrl.LastState()
	if rt.APIPort <= 0 || !e.IsCoreRunning() {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, fmt.Sprintf("http://127.0.0.1:%d/connections", rt.APIPort), nil)
	if err != nil {
		return
	}
	if rt.APISecret != "" {
		req.Header.Set("Authorization", "Bearer "+rt.APISecret)
	}
	if resp, doErr := (&http.Client{Timeout: 2 * time.Second}).Do(req); doErr == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}

// --- High-level RPC Operations ---

func (e *Engine) SetOutboundMode(ctx context.Context, mode string) error {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != "rule" && mode != "global" && mode != "direct" {
		return fmt.Errorf("invalid outbound mode %q", mode)
	}
	cur := e.store.Settings()
	if cur.AILockEnabled && mode != "rule" {
		return fmt.Errorf("AI Proxy Lock requires Rule mode; disable AI Proxy Lock first")
	}
	if _, err := e.store.UpdateSettings(func(s *profile.AppSettings) error {
		s.OutboundMode = mode
		return nil
	}); err != nil {
		return err
	}
	if _, err := e.applyAndSync(ctx, false, false); err != nil {
		return err
	}
	e.closeAllConnections(ctx)
	return nil
}

func (e *Engine) SetSystemProxy(enabled bool) error {
	cur := e.store.Settings()
	if cur.AILockEnabled && !enabled {
		return fmt.Errorf("AI Proxy Lock requires System Proxy; disable AI Proxy Lock first")
	}
	if _, err := e.store.UpdateSettings(func(s *profile.AppSettings) error {
		s.SystemProxyEnabled = enabled
		return nil
	}); err != nil {
		return err
	}
	e.reconcileSystemProxy()
	e.notifyStateChanged()
	return nil
}

func (e *Engine) SetEnhancedMode(ctx context.Context, enabled bool, allowUAC bool) error {
	cur := e.store.Settings()
	if cur.AILockEnabled && !enabled {
		return fmt.Errorf("AI Proxy Lock requires Enhanced Mode (TUN); disable AI Proxy Lock first")
	}
	prev := cur.EnhancedMode
	if _, err := e.store.UpdateSettings(func(s *profile.AppSettings) error {
		s.EnhancedMode = enabled
		return nil
	}); err != nil {
		return err
	}
	if _, err := e.applyAndSync(ctx, false, allowUAC); err != nil {
		// Roll back EnhancedMode setting if UAC cancelled or TUN failed!
		_, _ = e.store.UpdateSettings(func(s *profile.AppSettings) error {
			s.EnhancedMode = prev
			return nil
		})
		_, _ = e.applyAndSync(ctx, false, false)
		return err
	}
	return nil
}

func (e *Engine) TurnOffAllProxies(ctx context.Context) error {
	cur := e.store.Settings()
	if cur.AILockEnabled {
		return fmt.Errorf("disable AI Proxy Lock first before turning off protected proxy modes")
	}
	hadEnhanced := cur.EnhancedMode
	if _, err := e.store.UpdateSettings(func(s *profile.AppSettings) error {
		s.SystemProxyEnabled = false
		s.EnhancedMode = false
		return nil
	}); err != nil {
		return err
	}
	_ = e.sysProxy.ForceClearAll()
	if hadEnhanced {
		_, err := e.applyAndSync(ctx, false, false)
		return err
	}
	e.notifyStateChanged()
	return nil
}

func (e *Engine) SelectProxy(ctx context.Context, group, target string) error {
	rt := e.ctrl.LastState()
	if rt.APIPort <= 0 || !e.IsCoreRunning() {
		return fmt.Errorf("proxy core is not running")
	}
	body, _ := json.Marshal(map[string]string{"name": target})
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPut,
		fmt.Sprintf("http://127.0.0.1:%d/proxies/%s", rt.APIPort, url.PathEscape(group)),
		bytes.NewReader(body),
	)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if rt.APISecret != "" {
		req.Header.Set("Authorization", "Bearer "+rt.APISecret)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("select proxy failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	_ = e.store.RememberProxySelection(e.store.Settings().SelectedProfile, group, target)
	e.closeAllConnections(ctx)
	e.refreshLiveCoreMetadata(ctx)
	e.notifyStateChanged()
	return nil
}

func (e *Engine) RestoreAutoSelection(ctx context.Context, group string) error {
	if err := e.store.ClearProxySelection(e.store.Settings().SelectedProfile, group); err != nil {
		return err
	}
	rt := e.ctrl.LastState()
	if rt.APIPort > 0 && e.IsCoreRunning() {
		req, err := http.NewRequestWithContext(
			ctx,
			http.MethodDelete,
			fmt.Sprintf("http://127.0.0.1:%d/proxies/%s", rt.APIPort, url.PathEscape(group)),
			nil,
		)
		if err == nil {
			if rt.APISecret != "" {
				req.Header.Set("Authorization", "Bearer "+rt.APISecret)
			}
			if resp, doErr := (&http.Client{Timeout: 4 * time.Second}).Do(req); doErr == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
		}
	}
	if _, err := e.applyAndSync(ctx, false, false); err != nil {
		return err
	}
	e.closeAllConnections(ctx)
	return nil
}

func (e *Engine) BenchmarkGroup(ctx context.Context, groupName string) error {
	groupName = strings.TrimSpace(groupName)
	if groupName == "" {
		return fmt.Errorf("group name is required")
	}
	rt := e.ctrl.LastState()
	if rt.APIPort <= 0 || !e.IsCoreRunning() {
		return fmt.Errorf("proxy core is not running")
	}
	benchURL := strings.TrimSpace(e.store.Settings().BenchmarkURL)
	e.proxyGroupsMu.RLock()
	for _, g := range e.proxyGroups {
		if g.Name == groupName && strings.TrimSpace(g.TestURL) != "" {
			benchURL = strings.TrimSpace(g.TestURL)
			break
		}
	}
	e.proxyGroupsMu.RUnlock()
	if benchURL == "" {
		benchURL = "https://cp.cloudflare.com/generate_204"
	}

	u := fmt.Sprintf("http://127.0.0.1:%d/group/%s/delay?url=%s&timeout=5000",
		rt.APIPort, url.PathEscape(groupName), url.QueryEscape(benchURL))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if rt.APISecret != "" {
		req.Header.Set("Authorization", "Bearer "+rt.APISecret)
	}
	if resp, doErr := (&http.Client{Timeout: 12 * time.Second}).Do(req); doErr == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}

	e.refreshLiveCoreMetadata(ctx)
	e.notifyStateChanged()
	return nil
}

func (e *Engine) BenchmarkAll(ctx context.Context) error {
	rt := e.ctrl.LastState()
	if rt.APIPort <= 0 || !e.IsCoreRunning() {
		return fmt.Errorf("proxy core is not running")
	}
	benchURL := e.store.Settings().BenchmarkURL
	if strings.TrimSpace(benchURL) == "" {
		benchURL = "https://cp.cloudflare.com/generate_204"
	}

	e.proxyGroupsMu.RLock()
	cands := append([]ailock.CandidateNode(nil), e.aiCandidates...)
	e.proxyGroupsMu.RUnlock()

	sem := make(chan struct{}, 5)
	var wg sync.WaitGroup
	for _, cand := range cands {
		wg.Add(1)
		go func(nodeName string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			_, _ = features.ProbeSingleNode(ctx, rt.APIPort, rt.APISecret, nodeName, "", benchURL, 5000, "en")
		}(cand.Name)
	}
	wg.Wait()
	e.refreshLiveCoreMetadata(ctx)
	e.notifyStateChanged()
	return nil
}

func (e *Engine) DiagnoseNode(ctx context.Context, nodeName, providerName, benchURL string) (features.SingleNodeProbeReport, error) {
	rt := e.ctrl.LastState()
	if rt.APIPort <= 0 || !e.IsCoreRunning() {
		return features.SingleNodeProbeReport{}, fmt.Errorf("proxy core is not running")
	}
	settings := e.store.Settings()
	if strings.TrimSpace(benchURL) == "" {
		benchURL = settings.BenchmarkURL
	}
	rep, err := features.ProbeSingleNode(ctx, rt.APIPort, rt.APISecret, nodeName, providerName, benchURL, 5000, settings.Language)
	if err == nil {
		e.refreshLiveCoreMetadata(ctx)
		e.notifyStateChanged()
	}
	return rep, err
}

func (e *Engine) SetAILock(ctx context.Context, enabled bool, target string, allowUAC bool) error {
	prev := e.store.Settings()
	if enabled && !ailock.IsValidTarget(target) {
		return fmt.Errorf("choose a valid concrete proxy node for AI Proxy Lock")
	}
	if _, err := e.store.UpdateSettings(func(s *profile.AppSettings) error {
		if enabled {
			if !prev.AILockEnabled {
				s.AILockSavedState = &profile.AILockPreLockState{
					OutboundMode:       prev.OutboundMode,
					EnhancedMode:       prev.EnhancedMode,
					SystemProxyEnabled: prev.SystemProxyEnabled,
				}
			}
			s.AILockEnabled = true
			s.AILockTarget = strings.TrimSpace(target)
			ailock.EnforceProtectedSettings(s)
		} else {
			s.AILockEnabled = false
			if s.AILockSavedState != nil {
				if s.AILockSavedState.OutboundMode != "" {
					s.OutboundMode = s.AILockSavedState.OutboundMode
				}
				s.EnhancedMode = s.AILockSavedState.EnhancedMode
				s.SystemProxyEnabled = s.AILockSavedState.SystemProxyEnabled
				s.AILockSavedState = nil
			}
		}
		return nil
	}); err != nil {
		return err
	}

	if _, err := e.applyAndSync(ctx, false, allowUAC); err != nil {
		_, _ = e.store.UpdateSettings(func(s *profile.AppSettings) error {
			*s = prev
			return nil
		})
		_, _ = e.applyAndSync(ctx, false, false)
		return err
	}
	e.closeAllConnections(ctx)
	return nil
}

func (e *Engine) SelectProfile(ctx context.Context, name string) error {
	name = strings.TrimSuffix(strings.TrimSpace(name), ".yaml")
	if name == "" {
		return fmt.Errorf("profile name is required")
	}
	prev := e.store.Settings().SelectedProfile
	if _, err := e.store.UpdateSettings(func(s *profile.AppSettings) error {
		s.SelectedProfile = name
		return nil
	}); err != nil {
		return err
	}
	if _, err := e.applyAndSync(ctx, true, false); err != nil {
		_, _ = e.store.UpdateSettings(func(s *profile.AppSettings) error {
			s.SelectedProfile = prev
			return nil
		})
		return err
	}
	e.closeAllConnections(ctx)
	return nil
}

func (e *Engine) UpsertSubscription(ctx context.Context, name, rawURL, userAgent string, selectAfter bool) (subscription.ManagedSubscription, error) {
	sub, err := e.subMgr.AddOrUpdate(name, rawURL, userAgent)
	if err != nil {
		return subscription.ManagedSubscription{}, err
	}
	updated, err := e.subMgr.Refresh(ctx, sub.Name, 0)
	if err != nil {
		return sub, err
	}
	if selectAfter {
		if err := e.SelectProfile(ctx, updated.Name); err != nil {
			e.notifyStateChanged()
			return updated, err
		}
	} else if e.store.Settings().SelectedProfile == updated.Name {
		if _, err := e.applyAndSync(ctx, false, false); err != nil {
			return updated, err
		}
	} else {
		e.notifyStateChanged()
	}
	return updated, nil
}

func (e *Engine) RefreshSubscription(ctx context.Context, name string) (subscription.ManagedSubscription, error) {
	updated, err := e.subMgr.Refresh(ctx, name, 0)
	if err != nil {
		return subscription.ManagedSubscription{}, err
	}
	if e.store.Settings().SelectedProfile == updated.Name {
		if _, err := e.applyAndSync(ctx, false, false); err != nil {
			return updated, err
		}
	} else {
		e.notifyStateChanged()
	}
	return updated, nil
}

func (e *Engine) RemoveSubscription(ctx context.Context, name string, deleteFile bool) error {
	if err := e.subMgr.Remove(name, deleteFile); err != nil {
		return err
	}
	if deleteFile && e.store.Settings().SelectedProfile == name {
		_ = e.SelectProfile(ctx, "config")
	} else {
		e.notifyStateChanged()
	}
	return nil
}

func (e *Engine) ImportURLScheme(ctx context.Context, rawURI string) (subscription.ManagedSubscription, error) {
	u, err := url.Parse(strings.TrimSpace(rawURI))
	if err != nil {
		return subscription.ManagedSubscription{}, fmt.Errorf("invalid URL scheme: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "clash" && scheme != "clashfx" {
		return subscription.ManagedSubscription{}, fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	q := u.Query()
	subURL := strings.TrimSpace(q.Get("url"))
	if subURL == "" {
		return subscription.ManagedSubscription{}, fmt.Errorf("missing url= parameter in %s", rawURI)
	}
	name := strings.TrimSpace(q.Get("name"))
	return e.UpsertSubscription(ctx, name, subURL, "", true)
}

func (e *Engine) UpdateGeoDatabases(ctx context.Context) error {
	settings := e.store.Settings()
	if err := e.subMgr.UpdateExternalResource(ctx, settings.MMDBDownloadURL, "Country.mmdb", 1024); err != nil {
		return fmt.Errorf("update Country.mmdb: %w", err)
	}
	if err := e.subMgr.UpdateExternalResource(ctx, settings.GeoSiteDownloadURL, "geosite.dat", 1024); err != nil {
		return fmt.Errorf("update geosite.dat: %w", err)
	}
	return nil
}

func (e *Engine) UpdateNodeServices(ctx context.Context, nextSettings *nodeservices.Settings, ports map[string]int, enabled map[string]bool) error {
	prof := e.store.Settings().SelectedProfile
	if nextSettings != nil {
		if err := e.nodeStore.UpdateSettings(*nextSettings); err != nil {
			return err
		}
	}
	if len(ports) > 0 || len(enabled) > 0 {
		if err := e.nodeStore.UpdateProfileBindings(prof, ports, enabled); err != nil {
			return err
		}
	}
	_, err := e.applyAndSync(ctx, false, false)
	return err
}

func (e *Engine) UpdateGeneralSettings(ctx context.Context, next profile.AppSettings) error {
	cur := e.store.Settings()
	if cur.AILockEnabled {
		next.AILockEnabled = cur.AILockEnabled
		next.AILockTarget = cur.AILockTarget
		ailock.EnforceProtectedSettings(&next)
	}
	next.AILockSavedState = cur.AILockSavedState
	next.CustomRules = cur.CustomRules
	next.SavedSelections = cur.SavedSelections

	if next.LaunchAtLogin != cur.LaunchAtLogin {
		uiExe := filepath.Join(e.cfg.BinDir, "clashfx-ui.exe")
		_ = SetLaunchAtLogin(next.LaunchAtLogin, uiExe)
	}

	if _, err := e.store.UpdateSettings(func(s *profile.AppSettings) error {
		*s = next
		return nil
	}); err != nil {
		return err
	}

	if next.CustomConfigDir != cur.CustomConfigDir {
		_ = e.store.WatchActiveConfigDir(func(_ string) {
			_, _ = e.applyAndSync(context.Background(), false, false)
		})
	}

	_, err := e.applyAndSync(ctx, false, true)
	return err
}

func (e *Engine) UpdateCustomRules(ctx context.Context, rules []profile.CustomRule) error {
	normalized, err := features.NormalizeAndValidateCustomRules(rules)
	if err != nil {
		return err
	}
	prev := e.store.Settings().CustomRules
	if _, err := e.store.UpdateSettings(func(s *profile.AppSettings) error {
		s.CustomRules = normalized
		return nil
	}); err != nil {
		return err
	}
	if _, err := e.applyAndSync(ctx, false, false); err != nil {
		_, _ = e.store.UpdateSettings(func(s *profile.AppSettings) error {
			s.CustomRules = prev
			return nil
		})
		_, _ = e.applyAndSync(ctx, false, false)
		return err
	}
	e.closeAllConnections(ctx)
	return nil
}

func (e *Engine) GetEffectiveRules(ctx context.Context) (EffectiveRulesView, error) {
	settings := e.store.Settings()
	rt := e.ctrl.LastState()

	var rawRoot map[string]any
	if data, err := os.ReadFile(e.ctrl.RuntimeConfigPath()); err == nil {
		rawRoot, _ = profile.UnmarshalYAMLMap(data)
	}
	if rawRoot == nil {
		if data, err := os.ReadFile(e.store.ProfilePath(settings.SelectedProfile)); err == nil {
			rawRoot, _ = profile.UnmarshalYAMLMap(data)
		}
	}

	defTarget := "DIRECT"
	if rawRoot != nil {
		defTarget = features.ResolveDefaultRuleTarget(rawRoot)
	}

	normRuleKey := func(tp, payload, proxy string) string {
		cleanType := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(tp)), "-", "")
		return cleanType + "|" + strings.TrimSpace(payload) + "|" + strings.TrimSpace(proxy)
	}
	customKeys := make(map[string]bool, len(settings.CustomRules))
	for _, cr := range settings.CustomRules {
		if !cr.Enabled {
			continue
		}
		tgt := strings.TrimSpace(cr.Target)
		if strings.EqualFold(tgt, profile.DefaultProxyTargetToken) {
			tgt = defTarget
		}
		customKeys[normRuleKey(cr.Type, cr.Payload, tgt)] = true
	}

	classifySource := func(tp, payload, proxy string) string {
		switch strings.TrimSpace(payload) {
		case profile.InternalDirectRuleProviderName:
			return "system"
		case profile.AILockRuleProviderName:
			return "ai_lock"
		case profile.CNAppsRuleProviderName:
			return "cn_apps"
		}
		if customKeys[normRuleKey(tp, payload, proxy)] {
			return "custom"
		}
		return "profile"
	}

	out := EffectiveRulesView{
		OutboundMode:  settings.OutboundMode,
		DefaultTarget: defTarget,
		RuleProviders: map[string]RuleProviderView{},
	}

	// Seed inline rule-providers from runtime YAML as fallback
	if rawRoot != nil {
		if rpMap, ok := rawRoot["rule-providers"].(map[string]any); ok {
			for name, rawDef := range rpMap {
				dm, ok := rawDef.(map[string]any)
				if !ok {
					continue
				}
				pList := profile.ExtractStringRules(dm["payload"])
				vType, _ := dm["type"].(string)
				beh, _ := dm["behavior"].(string)
				out.RuleProviders[name] = RuleProviderView{
					Name:        name,
					Behavior:    beh,
					VehicleType: vType,
					RuleCount:   len(pList),
					Payload:     pList,
				}
			}
		}
	}

	if rt.APIPort > 0 && e.IsCoreRunning() {
		client := &http.Client{Timeout: 3 * time.Second}
		doGet := func(path string) ([]byte, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d%s", rt.APIPort, path), nil)
			if err != nil {
				return nil, err
			}
			if rt.APISecret != "" {
				req.Header.Set("Authorization", "Bearer "+rt.APISecret)
			}
			resp, err := client.Do(req)
			if err != nil {
				return nil, err
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
			}
			return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		}

		if provBody, err := doGet("/providers/rules"); err == nil {
			var provDoc struct {
				Providers map[string]struct {
					Name        string   `json:"name"`
					Behavior    string   `json:"behavior"`
					VehicleType string   `json:"vehicleType"`
					RuleCount   int      `json:"ruleCount"`
					Payload     []string `json:"payload"`
				} `json:"providers"`
			}
			if json.Unmarshal(provBody, &provDoc) == nil {
				for k, p := range provDoc.Providers {
					payload := p.Payload
					if len(payload) == 0 {
						payload = out.RuleProviders[k].Payload
					}
					out.RuleProviders[k] = RuleProviderView{
						Name:        k,
						Behavior:    p.Behavior,
						VehicleType: p.VehicleType,
						RuleCount:   p.RuleCount,
						Payload:     payload,
					}
				}
			}
		}

		if rulesBody, err := doGet("/rules"); err == nil {
			var rulesDoc struct {
				Rules []struct {
					Index   int    `json:"index"`
					Type    string `json:"type"`
					Payload string `json:"payload"`
					Proxy   string `json:"proxy"`
					Size    int    `json:"size"`
					Extra   *struct {
						Disabled  bool   `json:"disabled"`
						HitCount  uint64 `json:"hitCount"`
						MissCount uint64 `json:"missCount"`
					} `json:"extra"`
				} `json:"rules"`
			}
			if json.Unmarshal(rulesBody, &rulesDoc) == nil {
				out.Rules = make([]EffectiveRuleItem, 0, len(rulesDoc.Rules))
				for _, r := range rulesDoc.Rules {
					sz := r.Size
					if sz < 0 {
						if rp, ok := out.RuleProviders[r.Payload]; ok {
							sz = rp.RuleCount
						}
					}
					item := EffectiveRuleItem{
						Index:   r.Index,
						Type:    r.Type,
						Payload: r.Payload,
						Proxy:   r.Proxy,
						Size:    sz,
						Source:  classifySource(r.Type, r.Payload, r.Proxy),
					}
					if r.Extra != nil {
						item.Disabled = r.Extra.Disabled
						item.HitCount = r.Extra.HitCount
						item.MissCount = r.Extra.MissCount
					}
					out.Rules = append(out.Rules, item)
				}
				out.TotalRules = len(out.Rules)
				return out, nil
			}
		}
	}

	// Offline fallback: parse rules list from runtime/profile YAML
	if rawRoot != nil {
		lines := profile.ExtractStringRules(rawRoot["rules"])
		out.Rules = make([]EffectiveRuleItem, 0, len(lines))
		for idx, line := range lines {
			parts := strings.SplitN(strings.TrimSpace(line), ",", 4)
			var tp, payload, proxy string
			if len(parts) >= 2 && strings.EqualFold(strings.TrimSpace(parts[0]), "MATCH") {
				tp = "MATCH"
				proxy = strings.TrimSpace(parts[1])
			} else if len(parts) >= 3 {
				tp = strings.TrimSpace(parts[0])
				payload = strings.TrimSpace(parts[1])
				proxy = strings.TrimSpace(parts[2])
			}
			sz := -1
			if rp, ok := out.RuleProviders[payload]; ok {
				sz = rp.RuleCount
			}
			out.Rules = append(out.Rules, EffectiveRuleItem{
				Index:   idx,
				Type:    tp,
				Payload: payload,
				Proxy:   proxy,
				Size:    sz,
				Source:  classifySource(tp, payload, proxy),
			})
		}
		out.TotalRules = len(out.Rules)
	}
	return out, nil
}

func (e *Engine) DiagnosticInfo() string {
	rt := e.ctrl.LastState()
	osState, _ := e.sysProxy.CurrentOSState()
	var recentLogs []string
	if e.coreViaService {
		if resp, err := callService(2*time.Second, service.Request{Action: "status"}); err == nil && resp.Status != nil {
			recentLogs = resp.Status.RecentLogs
		}
	} else {
		recentLogs = e.localRunner.Status().RecentLogs
	}
	return features.BuildRedactedDiagnosticReport(features.DiagnosticBundleInput{
		AppVersion:       AppVersion,
		CoreRunning:      e.IsCoreRunning(),
		CoreViaService:   e.coreViaService,
		ServiceInstalled: IsPrivilegedServiceInstalled(service.ServiceName),
		MixedPort:        rt.MixedPort,
		APIPort:          rt.APIPort,
		DNSPort:          rt.DNSPort,
		Settings:         e.store.Settings(),
		SysProxyServer:   osState.ProxyServer,
		SysProxyEnabled:  osState.ProxyEnable == 1,
		SuspendedNet:     e.suspendedByNet,
		RecentCoreLogs:   recentLogs,
	})
}

func (e *Engine) CheckUpdates(ctx context.Context) (features.UpdateCheckResult, error) {
	ch := e.store.Settings().UpdateChannel
	apiURL := os.Getenv("CLASHFX_UPDATE_API_URL")
	return features.CheckForUpdates(ctx, "v"+AppVersion, ch, apiURL)
}

func (e *Engine) notifyStateChanged() {
	if e.onPush != nil {
		e.onPush()
	}
}
