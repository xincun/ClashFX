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
}

type ProxyGroupView struct {
	Name              string          `json:"name"`
	Type              string          `json:"type"`
	Now               string          `json:"now"`
	HasManualOverride bool            `json:"has_manual_override"`
	Items             []ProxyItemView `json:"items"`
}

type DaemonSnapshot struct {
	Version            string                              `json:"version"`
	Arch               string                              `json:"arch"`
	CoreRunning        bool                                `json:"core_running"`
	CoreViaService     bool                                `json:"core_via_service"`
	ServiceInstalled   bool                                `json:"service_installed"`
	Settings           profile.AppSettings                 `json:"settings"`
	Runtime            transaction.AppliedRuntimeState     `json:"runtime"`
	DashboardURL       string                              `json:"dashboard_url"`
	Profiles           []string                            `json:"profiles"`
	ProfileMixinPath   string                              `json:"profile_mixin_path"`
	ConfigDir          string                              `json:"config_dir"`
	LogsDir            string                              `json:"logs_dir"`
	ProxyGroups        []ProxyGroupView                    `json:"proxy_groups"`
	AILockCandidates   []ailock.CandidateNode              `json:"ai_lock_candidates"`
	AILockRelay        string                              `json:"ai_lock_relay,omitempty"`
	Subscriptions      []subscription.ManagedSubscription  `json:"subscriptions"`
	NodeServices       nodeservices.RuntimeSnapshot        `json:"node_services"`
	ActiveNetwork      features.ActiveNetworkSignature     `json:"active_network"`
	SuspendedByNetwork string                              `json:"suspended_by_network,omitempty"`
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

	opMu            sync.Mutex
	coreViaService  bool
	allowPromptUAC  bool
	suspendedByNet  string
	proxyGroupsMu   sync.RWMutex
	proxyGroups     []ProxyGroupView
	aiCandidates    []ailock.CandidateNode
	aiRelay         string

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
	e.opMu.Unlock()

	if err != nil {
		e.notifyStateChanged()
		return state, err
	}

	e.reconcileSystemProxy()
	e.refreshLiveCoreMetadata(ctx)
	e.notifyStateChanged()
	return state, nil
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
	netTicker := time.NewTicker(10 * time.Second)
	subTicker := time.NewTicker(60 * time.Second)
	defer netTicker.Stop()
	defer subTicker.Stop()

	var lastAutoSubAt time.Time
	for {
		select {
		case <-e.stopBgCh:
			return
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

func (e *Engine) Snapshot() DaemonSnapshot {
	settings := e.store.Settings()
	rt := e.ctrl.LastState()
	profiles, _ := e.store.ListProfiles()
	nsSnap, _ := e.nodeCoord.Snapshot(settings.SelectedProfile)
	sig := features.DetectActiveNetworkSignature()

	e.proxyGroupsMu.RLock()
	groups := append([]ProxyGroupView(nil), e.proxyGroups...)
	cands := append([]ailock.CandidateNode(nil), e.aiCandidates...)
	relay := e.aiRelay
	e.proxyGroupsMu.RUnlock()

	e.opMu.Lock()
	suspended := e.suspendedByNet
	viaSvc := e.coreViaService
	e.opMu.Unlock()

	logsDir := filepath.Join(e.store.AppDataDir(), "logs")
	_ = os.MkdirAll(logsDir, 0700)

	return DaemonSnapshot{
		Version:            AppVersion,
		Arch:               runtime.GOARCH,
		CoreRunning:        e.IsCoreRunning(),
		CoreViaService:     viaSvc,
		ServiceInstalled:   IsPrivilegedServiceInstalled(service.ServiceName),
		Settings:           settings,
		Runtime:            rt,
		DashboardURL:       buildDashboardURL(rt.APIPort, rt.APISecret),
		Profiles:           profiles,
		ProfileMixinPath:   e.store.ProfileMixinPath(),
		ConfigDir:          e.store.ActiveConfigDir(),
		LogsDir:            logsDir,
		ProxyGroups:        groups,
		AILockCandidates:   cands,
		AILockRelay:        relay,
		Subscriptions:      e.subMgr.List(),
		NodeServices:       nsSnap,
		ActiveNetwork:      sig,
		SuspendedByNetwork: suspended,
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
	if rawRoot != nil {
		cands = ailock.ListCandidateTargets(rawRoot, nil)
		if settings.AILockEnabled {
			relay = ailock.RelayName(rawRoot, settings.AILockTarget, nil)
		}
	}

	var groups []ProxyGroupView
	if rt.APIPort > 0 && e.IsCoreRunning() {
		groups = fetchProxyGroupsFromCore(ctx, rt.APIPort, rt.APISecret, rawRoot, e.store.RecoverySelectionsForProfile(settings.SelectedProfile))
	}

	e.proxyGroupsMu.Lock()
	e.proxyGroups = groups
	e.aiCandidates = cands
	e.aiRelay = relay
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

	delayOf := func(name string) int {
		p, ok := payload.Proxies[name]
		if !ok || len(p.History) == 0 {
			return 0
		}
		return p.History[len(p.History)-1].Delay
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
		if len(px.All) > 0 && name != "GLOBAL" && !seen[name] {
			extras = append(extras, name)
		}
	}
	sort.Strings(extras)
	orderedNames = append(orderedNames, extras...)
	if g, hasGlobal := payload.Proxies["GLOBAL"]; hasGlobal && len(g.All) > 0 && !seen["GLOBAL"] {
		orderedNames = append(orderedNames, "GLOBAL")
	}

	var out []ProxyGroupView
	for _, gName := range orderedNames {
		p, ok := payload.Proxies[gName]
		if !ok || len(p.All) == 0 {
			continue
		}
		gv := ProxyGroupView{
			Name:              gName,
			Type:              p.Type,
			Now:               p.Now,
			HasManualOverride: savedSel[gName] != "",
			Items:             make([]ProxyItemView, 0, len(p.All)),
		}
		for _, itemName := range p.All {
			itemMeta := payload.Proxies[itemName]
			gv.Items = append(gv.Items, ProxyItemView{
				Name:    itemName,
				Type:    itemMeta.Type,
				DelayMs: delayOf(itemName),
			})
		}
		out = append(out, gv)
	}
	return out
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
	_, err := e.applyAndSync(ctx, false, false)
	return err
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
	e.refreshLiveCoreMetadata(ctx)
	e.notifyStateChanged()
	return nil
}

func (e *Engine) RestoreAutoSelection(ctx context.Context, group string) error {
	if err := e.store.ClearProxySelection(e.store.Settings().SelectedProfile, group); err != nil {
		return err
	}
	_, err := e.applyAndSync(ctx, false, false)
	return err
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
		s.AILockEnabled = enabled
		if enabled {
			s.AILockTarget = strings.TrimSpace(target)
			ailock.EnforceProtectedSettings(s)
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
		_ = e.SelectProfile(ctx, updated.Name)
	} else if e.store.Settings().SelectedProfile == updated.Name {
		_, _ = e.applyAndSync(ctx, false, false)
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
		_, _ = e.applyAndSync(ctx, false, false)
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
