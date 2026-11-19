package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
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
	"github.com/clashfx/core-engine/internal/applog"
	"github.com/clashfx/core-engine/internal/features"
	"github.com/clashfx/core-engine/internal/nodeservices"
	"github.com/clashfx/core-engine/internal/profile"
	"github.com/clashfx/core-engine/internal/service"
	"github.com/clashfx/core-engine/internal/subscription"
	"github.com/clashfx/core-engine/internal/sysproxy"
	"github.com/clashfx/core-engine/internal/transaction"
	mihomoutils "github.com/metacubex/mihomo/common/utils"
)

var AppVersion = "1.19.24"

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
	Editable    bool     `json:"editable,omitempty"`
	CanReset    bool     `json:"can_reset,omitempty"`
	FilePath    string   `json:"file_path,omitempty"`
	RawText     string   `json:"raw_text,omitempty"`
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
	CoreVersion            string                             `json:"core_version,omitempty"`
	Platform               string                             `json:"platform"`
	Arch                   string                             `json:"arch"`
	CoreRunning            bool                               `json:"core_running"`
	CoreHealth             string                             `json:"core_health,omitempty"`
	CoreRestartCount       int                                `json:"core_restart_count,omitempty"`
	LastCrashAt            string                             `json:"last_crash_at,omitempty"`
	LastCrashReason        string                             `json:"last_crash_reason,omitempty"`
	CoreViaService         bool                               `json:"core_via_service"`
	ServiceInstalled       bool                               `json:"service_installed"`
	AIGuard                service.AIGuardStatus              `json:"ai_guard"`
	Settings               profile.AppSettings                `json:"settings"`
	Runtime                transaction.AppliedRuntimeState    `json:"runtime"`
	DashboardURL           string                             `json:"dashboard_url"`
	Profiles               []string                           `json:"profiles"`
	ProfileMixinPath       string                             `json:"profile_mixin_path"`
	ConfigDir              string                             `json:"config_dir"`
	LogsDir                string                             `json:"logs_dir"`
	CoreLogFile            string                             `json:"core_log_file,omitempty"`
	DaemonLogFile          string                             `json:"daemon_log_file,omitempty"`
	ServiceLogFile         string                             `json:"service_log_file,omitempty"`
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
	upgradeMu         sync.Mutex
	allowPromptUAC    bool
	statusMu          sync.RWMutex
	coreViaService    bool
	coreVersion       string
	suspendedByNet    string
	lastApplyErr      string
	desiredCoreActive bool
	lastCoreRunning   bool
	lastCoreHealth    string
	lastRestartCount  int
	lastAIGuardMode   string
	lastAIGuardSig    string
	coreDownSince     time.Time
	healthFailCount   int
	lastRecoveryTryAt time.Time
	cachedNetSig      features.ActiveNetworkSignature
	cachedNetSigAt    time.Time
	proxyGroupsMu     sync.RWMutex
	proxyGroups       []ProxyGroupView
	defaultRuleTarget string
	aiCandidates      []ailock.CandidateNode
	aiRelay           string
	providerSubInfo   *subscription.UserInfo
	logsDir           string
	daemonLog         *applog.RotatingFile

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
	logsDir := filepath.Join(store.AppDataDir(), "logs")
	_ = os.MkdirAll(logsDir, 0755)
	daemonLog, _ := applog.NewRotatingFile(filepath.Join(logsDir, "clashfx-daemon.log"), applog.DefaultMaxBytes, applog.DefaultMaxBackups)

	eng := &Engine{
		cfg:         cfg,
		store:       store,
		subMgr:      subMgr,
		sysProxy:    sysProxy,
		nodeStore:   nodeStore,
		localRunner: localRunner,
		coreHelper:  helper,
		logsDir:     logsDir,
		daemonLog:   daemonLog,
		stopBgCh:    make(chan struct{}),
		onPush:      onPush,
	}

	eng.ctrl = transaction.NewController(
		store,
		eng,
		cfg.DashboardUIDir,
		func(root map[string]any, settings profile.AppSettings) error {
			if !settings.AILockEnabled {
				return nil
			}
			outcome := ailock.Apply(root, settings.AILockTarget, eng.loadProviderSnapshots(root))
			if !outcome.Applied {
				return fmt.Errorf("AI Proxy Lock rejected target %q: %s", settings.AILockTarget, outcome.RejectionReason)
			}
			return nil
		},
		features.TransformHookForCNApps(),
		nodeStore.TransformHookForControllerWithProviderProxies(eng.IsCoreRunning, eng.loadProviderProxiesForNodeServices),
	)
	eng.ctrl.SetProfileSourceResolver(func(profileName string) ([]byte, bool) {
		return eng.resolveFallbackProfileBytes(profileName)
	})
	eng.ctrl.SetEffectiveSettingsResolver(eng.effectiveRuntimeSettings)

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

	eng.syncCoreBinarySelection()
	return eng, nil
}

func (e *Engine) logf(level, format string, args ...any) {
	if e.daemonLog != nil {
		e.daemonLog.Logf("clashfx-daemon", level, format, args...)
	}
	upper := strings.ToUpper(strings.TrimSpace(level))
	if upper == "ERROR" || upper == "WARN" {
		fmt.Fprintf(os.Stderr, "[clashfx-daemon] [%s] %s\n", upper, fmt.Sprintf(format, args...))
	}
}

// Start boots the proxy core, recovers any crashed system proxy snapshot, starts
// file watchers, power-resume self-healing, subscription timers, and node watchdog.
func (e *Engine) Start(ctx context.Context) error {
	e.logf("INFO", "starting clashfx-daemon %s (%s/%s, appData=%s, logs=%s)",
		AppVersion, runtime.GOOS, runtime.GOARCH, e.store.AppDataDir(), e.logsDir)

	settings := e.store.Settings()
	if settings.AILockEnabled {
		_, _ = e.store.UpdateSettings(func(s *profile.AppSettings) error {
			ailock.EnforceProtectedSettings(s)
			return nil
		})
		settings = e.store.Settings()
	}

	// Clean stale legacy 198.18.0.2 DNS left by pre-unified ClashFX Helper versions.
	_, _ = sysproxy.CleanLegacyTunDNS()

	// Recover previous crashed session's system proxy if effective runtime doesn't want system proxy now
	_, _ = e.sysProxy.RecoverAfterUncleanShutdown(e.effectiveRuntimeSettings(settings).SystemProxyEnabled)

	// Self-heal Login item / HKCU Run\ClashFX path if the user moved or reinstalled ClashFX
	if settings.LaunchAtLogin {
		if uiExe := uiLaunchExecutablePath(e.cfg.BinDir); uiExe != "" {
			_ = SetLaunchAtLogin(true, uiExe)
		}
	}

	// If EnhancedMode was persisted from last session, verify privileged service is installed;
	// if uninstalled externally while offline (and AI Lock isn't forcing it), fall back cleanly to normal mode.
	if settings.EnhancedMode && supportsPrivilegedService() && !IsPrivilegedServiceInstalled(service.ServiceName) && !settings.AILockEnabled {
		_, _ = e.store.UpdateSettings(func(s *profile.AppSettings) error {
			s.EnhancedMode = false
			return nil
		})
		settings = e.store.Settings()
	}

	if subs := e.subMgr.List(); len(subs) > 0 {
		if _, ok := e.subMgr.FindByName(settings.SelectedProfile); !ok {
			if _, err := e.store.UpdateSettings(func(s *profile.AppSettings) error {
				s.SelectedProfile = subs[0].Name
				return nil
			}); err == nil {
				settings = e.store.Settings()
			}
		}
	}

	// Arm OS-level AI Guard BEFORE touching core so there is zero boot/startup leak window.
	if settings.AILockEnabled {
		e.syncAIGuard(true)
	}

	// Adopt an already-running ClashFXService mihomo_core if present (e.g. daemon respawned)
	// so initial applyAndSync uses zero-downtime REST reload instead of killing TUN.
	eff := e.effectiveRuntimeSettings(settings)
	if eff.EnhancedMode && supportsPrivilegedService() && IsPrivilegedServiceInstalled(service.ServiceName) {
		if resp, err := callService(2*time.Second, service.Request{Action: "status"}); err == nil && resp.OK && resp.Status != nil && resp.Status.Running {
			if e.ctrl.SeedActiveStateFromDisk(resp.Status.ConfigFile) {
				e.setCoreViaService(true)
				e.logf("INFO", "adopted running ClashFXService mihomo_core (pid=%d, config=%s)", resp.Status.PID, resp.Status.ConfigFile)
			}
		}
	}

	if _, err := e.applyAndSync(ctx, true, false); err != nil {
		// Log/expose startup error in state without crashing daemon IPC so UI can still open & fix config!
		e.logf("WARN", "initial core apply warning: %v", err)
	}

	e.watchActiveConfigDir()
	e.stopPower = StartPowerResumeMonitor(func() {
		time.Sleep(1500 * time.Millisecond)
		e.handleWakeOrNetworkCheck()
	})
	e.nodeCoord.StartBackgroundLoop()
	go e.backgroundMaintenanceLoop()

	return nil
}

type ShutdownOptions struct {
	ForceRestoreProxy bool
	KillAIClients     bool
}

func (e *Engine) Shutdown() {
	e.ShutdownWithOptions(ShutdownOptions{})
}

func (e *Engine) ShutdownWithForceRestore(forceRestore bool) {
	e.ShutdownWithOptions(ShutdownOptions{ForceRestoreProxy: forceRestore})
}

func (e *Engine) ShutdownWithOptions(opts ShutdownOptions) {
	select {
	case <-e.stopBgCh:
		return
	default:
		close(e.stopBgCh)
	}

	e.logf("INFO", "shutting down clashfx-daemon (force_restore=%v, kill_ai_clients=%v)", opts.ForceRestoreProxy, opts.KillAIClients)
	e.statusMu.Lock()
	e.desiredCoreActive = false
	e.statusMu.Unlock()

	if e.stopPower != nil {
		e.stopPower()
	}
	e.nodeCoord.Stop()

	cfgDir := e.store.ActiveConfigDir()
	e.store.Close()

	// Terminate protected AI clients BEFORE disarming OS AI Guard, restoring OS proxy,
	// or stopping TUN so they cannot leak onto a direct network connection during teardown.
	if opts.KillAIClients {
		killed, _ := ailock.TerminateRunningAIClients(cfgDir)
		e.logf("INFO", "terminated %d protected AI client process(es) before teardown", killed)
	}

	// Clean user quit explicitly disarms OS-level AI Guard so normal network access resumes.
	e.disarmAIGuard()

	if opts.ForceRestoreProxy {
		_ = e.sysProxy.CleanupForUninstall()
		_ = SetLaunchAtLogin(false, "")
	} else {
		_ = e.sysProxy.Shutdown(false)
	}

	_ = e.localRunner.Stop()
	viaSvc := e.isCoreViaService()
	if viaSvc {
		_, _ = callService(5*time.Second, service.Request{Action: "stop_core"})
	}
	if e.daemonLog != nil {
		_ = e.daemonLog.Close()
	}
}

func (e *Engine) ListRunningAIClients() ailock.RunningAIClientsResult {
	return ailock.ListRunningAIClients(e.store.ActiveConfigDir())
}

// CleanupUninstallState unconditionally clears any ClashFX-managed system proxy snapshot,
// lingering loopback proxy settings, and HKCU auto-start registry entry during uninstall.
func CleanupUninstallState(appDataDir string) error {
	resolvedDir := strings.TrimSpace(appDataDir)
	if store, err := profile.NewStore(resolvedDir, ""); err == nil {
		resolvedDir = store.AppDataDir()
		store.Close()
	}
	var firstErr error
	if resolvedDir != "" {
		if err := sysproxy.NewManager(resolvedDir, nil).CleanupForUninstall(); err != nil {
			firstErr = err
		}
	}
	if err := SetLaunchAtLogin(false, ""); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func callService(timeout time.Duration, req service.Request) (service.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return service.Call(ctx, req)
}

// ValidateConfig implements transaction.CoreApplier.
func (e *Engine) ValidateConfig(homeDir, configPath string) error {
	e.statusMu.RLock()
	helper := e.coreHelper
	e.statusMu.RUnlock()
	return helper.ValidateConfig(homeDir, configPath)
}

func supportsPrivilegedService() bool {
	return runtime.GOOS == "windows" || runtime.GOOS == "darwin"
}

func serviceExecutablePath(binDir string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(binDir, "clashfx-service.exe")
	}
	return filepath.Join(binDir, "clashfx-service")
}

func uiLaunchExecutablePath(binDir string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(binDir, "clashfx-ui.exe")
	}
	if runtime.GOOS == "darwin" && strings.TrimSpace(binDir) != "" {
		appUI := filepath.Join(binDir, "ClashFX")
		if _, err := os.Stat(appUI); err == nil {
			return appUI
		}
	}
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	return ""
}

func (e *Engine) HandlePowerResume() {
	e.handleWakeOrNetworkCheck()
}

func (e *Engine) InstallPrivilegedService() error {
	if !supportsPrivilegedService() {
		return nil
	}
	e.opMu.Lock()
	svcExe := serviceExecutablePath(e.cfg.BinDir)
	err := ElevateAndInstallService(svcExe)
	e.statusMu.Lock()
	if err != nil {
		e.lastApplyErr = err.Error()
	} else {
		e.lastApplyErr = ""
	}
	e.statusMu.Unlock()
	enhanced := e.store.Settings().EnhancedMode
	e.opMu.Unlock()

	if err != nil {
		e.notifyStateChanged()
		return err
	}
	if enhanced {
		_, syncErr := e.applyAndSync(context.Background(), true, false)
		return syncErr
	}
	e.notifyStateChanged()
	return nil
}

// ApplyRuntimeConfig implements transaction.CoreApplier, transparently routing
// normal mode through localRunner and Enhanced TUN mode through ClashFXService.
func (e *Engine) ApplyRuntimeConfig(ctx context.Context, settings profile.AppSettings, homeDir, runtimeConfigPath string, apiPort int, secret string) error {
	wantService := settings.EnhancedMode && supportsPrivilegedService()

	if wantService {
		_ = e.localRunner.Stop()
		if !IsPrivilegedServiceInstalled(service.ServiceName) {
			if !e.allowPromptUAC {
				return ErrServiceInstallRequired
			}
			svcExe := serviceExecutablePath(e.cfg.BinDir)
			if err := ElevateAndInstallService(svcExe); err != nil {
				return err
			}
		}
		if e.isCoreViaService() && e.isServiceCoreRunning() {
			if err := transaction.ReloadViaREST(ctx, apiPort, secret, runtimeConfigPath); err == nil {
				return nil
			}
		}
		resp, err := callService(15*time.Second, service.Request{
			Action:     "start_core",
			HomeDir:    homeDir,
			ConfigFile: runtimeConfigPath,
			ExtUI:      e.cfg.DashboardUIDir,
			LogDir:     e.logsDir,
		})
		if err != nil {
			return err
		}
		if !resp.OK {
			return fmt.Errorf("ClashFXService start_core failed: %s", resp.Message)
		}
		e.setCoreViaService(true)
		return nil
	}

	// Normal (non-elevated) mode
	if e.isCoreViaService() {
		_, _ = callService(6*time.Second, service.Request{Action: "stop_core"})
		e.setCoreViaService(false)
	}
	if e.localRunner.Status().Running {
		if err := transaction.ReloadViaREST(ctx, apiPort, secret, runtimeConfigPath); err == nil {
			return nil
		}
	}
	_, err := e.localRunner.StartWithOptions(homeDir, runtimeConfigPath, e.cfg.DashboardUIDir, e.logsDir, true)
	return err
}

func (e *Engine) isCoreViaService() bool {
	e.statusMu.RLock()
	defer e.statusMu.RUnlock()
	return e.coreViaService
}

func (e *Engine) setCoreViaService(v bool) {
	e.statusMu.Lock()
	e.coreViaService = v
	e.statusMu.Unlock()
}

func (e *Engine) currentCoreStatus() service.Status {
	if e.isCoreViaService() {
		if resp, err := callService(2*time.Second, service.Request{Action: "status"}); err == nil && resp.OK && resp.Status != nil {
			return *resp.Status
		}
		return service.Status{Health: service.CoreHealthStopped}
	}
	return e.localRunner.Status()
}

func (e *Engine) IsCoreRunning() bool {
	return e.currentCoreStatus().Running
}

func (e *Engine) isServiceCoreRunning() bool {
	resp, err := callService(2*time.Second, service.Request{Action: "status"})
	return err == nil && resp.OK && resp.Status != nil && resp.Status.Running
}

func extractCIDRsFromRouteExcludes(raw string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r'
	}) {
		s := strings.TrimSpace(part)
		if s != "" && strings.Contains(s, "/") {
			out = append(out, s)
		}
	}
	return out
}

func (e *Engine) syncAIGuard(force bool) {
	settings := e.store.Settings()
	if !settings.AILockEnabled {
		e.disarmAIGuard()
		return
	}

	cfgDir := e.store.ActiveConfigDir()
	exes := ailock.DiscoverProtectedExecutables(cfgDir)
	cidrs := extractCIDRsFromRouteExcludes(settings.TunRouteExcludes)
	cfg := service.AIGuardConfig{
		Enabled:           true,
		ConfigDir:         cfgDir,
		ProtectedExePaths: exes,
		TunDeviceName:     "ClashFX",
		AllowCIDRs:        cidrs,
		AllowLAN:          true,
		ConsoleUID:        uint32(os.Getuid()),
	}

	sigBytes, _ := json.Marshal(cfg)
	sig := string(sigBytes)
	e.statusMu.RLock()
	prevSig := e.lastAIGuardSig
	e.statusMu.RUnlock()
	if !force && sig == prevSig {
		return
	}

	if supportsPrivilegedService() && IsPrivilegedServiceInstalled(service.ServiceName) {
		resp, err := callService(6*time.Second, service.Request{
			Action:  "sync_ai_guard",
			AIGuard: &cfg,
		})
		if err != nil {
			e.logf("WARN", "sync_ai_guard RPC failed: %v", err)
			return
		}
		if !resp.OK {
			e.logf("WARN", "sync_ai_guard rejected: %s", resp.Message)
			return
		}
		e.statusMu.Lock()
		e.lastAIGuardSig = sig
		e.statusMu.Unlock()
		if resp.Status != nil && resp.Status.AIGuard != nil {
			e.logf("INFO", "AI Guard synced via ClashFXService (mode=%s, armed=%v, protected=%d, tun_luid=%d)",
				resp.Status.AIGuard.Mode, resp.Status.AIGuard.Armed, resp.Status.AIGuard.ProtectedCount, resp.Status.AIGuard.TunLUID)
		}
		return
	}

	st := e.localRunner.SyncAIGuard(cfg)
	e.statusMu.Lock()
	e.lastAIGuardSig = sig
	e.statusMu.Unlock()
	if st.AIGuard != nil {
		e.logf("INFO", "AI Guard synced locally (mode=%s, armed=%v, protected=%d)",
			st.AIGuard.Mode, st.AIGuard.Armed, st.AIGuard.ProtectedCount)
	}
}

func (e *Engine) disarmAIGuard() {
	e.statusMu.Lock()
	e.lastAIGuardSig = ""
	e.statusMu.Unlock()
	if supportsPrivilegedService() && IsPrivilegedServiceInstalled(service.ServiceName) {
		_, _ = callService(4*time.Second, service.Request{Action: "disarm_ai_guard"})
	}
	_ = e.localRunner.DisarmAIGuard()
}

func (e *Engine) hasUsableManagedSubscription() bool {
	if e.subMgr == nil {
		return false
	}
	for _, sub := range e.subMgr.List() {
		if _, err := os.Stat(e.store.ProfilePath(sub.Name)); err == nil {
			return true
		}
	}
	return false
}

// effectiveRuntimeSettings keeps persisted preferences (System Proxy / TUN) checked
// while holding OS proxy and TUN in clean standby until at least one usable
// subscription exists (unless AI Lock explicitly forces active control).
func (e *Engine) effectiveRuntimeSettings(s profile.AppSettings) profile.AppSettings {
	if !e.hasUsableManagedSubscription() && !s.AILockEnabled {
		s.EnhancedMode = false
		s.SystemProxyEnabled = false
	}
	return s
}

func (e *Engine) applyAndSync(ctx context.Context, includeRecovery bool, allowUAC bool) (transaction.AppliedRuntimeState, error) {
	e.opMu.Lock()
	e.allowPromptUAC = allowUAC
	prevViaService := e.isCoreViaService()
	effSettings := e.effectiveRuntimeSettings(e.store.Settings())
	wantService := effSettings.EnhancedMode && supportsPrivilegedService()
	if prevViaService != wantService {
		e.ctrl.ResetActiveState()
	}
	state, err := e.ctrl.Apply(ctx, includeRecovery)
	e.allowPromptUAC = false
	e.statusMu.Lock()
	if err != nil {
		e.lastApplyErr = err.Error()
	} else {
		e.lastApplyErr = ""
	}
	e.statusMu.Unlock()
	e.opMu.Unlock()

	if err != nil {
		e.logf("ERROR", "applyAndSync failed: %v", err)
		e.refreshLiveCoreMetadata(ctx)
		e.notifyStateChanged()
		return state, err
	}

	e.statusMu.Lock()
	e.desiredCoreActive = true
	e.coreDownSince = time.Time{}
	e.healthFailCount = 0
	e.lastCoreRunning = true
	e.lastCoreHealth = service.CoreHealthHealthy
	e.statusMu.Unlock()

	if includeRecovery && state.APIPort > 0 && e.IsCoreRunning() {
		e.replaySavedSelections(ctx, state.APIPort, state.APISecret, e.store.Settings().SelectedProfile)
	}

	e.syncAIGuard(true)
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

func cloneNetworkSignature(sig features.ActiveNetworkSignature) features.ActiveNetworkSignature {
	return features.ActiveNetworkSignature{
		ProfileNames: append([]string(nil), sig.ProfileNames...),
		Interfaces:   append([]string(nil), sig.Interfaces...),
		DNSSuffixes:  append([]string(nil), sig.DNSSuffixes...),
		Gateways:     append([]string(nil), sig.Gateways...),
	}
}

func (e *Engine) currentNetworkSignature(forceRefresh bool) features.ActiveNetworkSignature {
	if !forceRefresh {
		e.statusMu.RLock()
		if !e.cachedNetSigAt.IsZero() && time.Since(e.cachedNetSigAt) < 15*time.Second {
			cached := cloneNetworkSignature(e.cachedNetSig)
			e.statusMu.RUnlock()
			return cached
		}
		e.statusMu.RUnlock()
	}

	sig := features.DetectActiveNetworkSignature()
	e.statusMu.Lock()
	e.cachedNetSig = cloneNetworkSignature(sig)
	e.cachedNetSigAt = time.Now()
	e.statusMu.Unlock()
	return sig
}

func (e *Engine) reconcileSystemProxy() {
	settings := e.store.Settings()
	eff := e.effectiveRuntimeSettings(settings)
	rt := e.ctrl.LastState()
	sig := e.currentNetworkSignature(true)
	matchedRule, shouldSuspend := features.MatchSuspendNetwork(sig, settings.SuspendNetworks)
	if settings.AILockEnabled {
		// AI Proxy Lock never suspends system proxy on network matches!
		shouldSuspend = false
		matchedRule = ""
	}

	coreRunning := e.IsCoreRunning()
	e.statusMu.Lock()
	e.suspendedByNet = matchedRule
	downSince := e.coreDownSince
	e.statusMu.Unlock()

	// If AI Lock is OFF and the core has been continuously down for >=15s, temporarily
	// release OS system proxy so non-AI-Lock users are not left with a dead 127.0.0.1 proxy.
	// When AI Lock is ON, ALWAYS keep system proxy pointing at 127.0.0.1 so browsers/Electron
	// remain strictly fail-closed even while mihomo_core is restarting or crashed.
	releaseDeadProxy := !settings.AILockEnabled && !coreRunning && !downSince.IsZero() && time.Since(downSince) >= 15*time.Second

	if eff.SystemProxyEnabled && !shouldSuspend && !releaseDeadProxy && rt.HTTPPort > 0 {
		_, _ = e.sysProxy.ReconcileIfExpected(true, rt.HTTPPort, settings.ProxyBypassList)
	} else {
		_ = e.sysProxy.Restore()
	}
}

func (e *Engine) handleWakeOrNetworkCheck() {
	e.logf("INFO", "power resume / wake check triggered")
	if e.isCoreViaService() {
		_, _ = callService(3*time.Second, service.Request{Action: "power_resume"})
	}
	e.verifyCoreAfterWake()
	go func() {
		select {
		case <-e.stopBgCh:
			return
		case <-time.After(5 * time.Second):
		}
		e.verifyCoreAfterWake()
	}()
}

func (e *Engine) verifyCoreAfterWake() {
	settings := e.store.Settings()
	if !e.IsCoreRunning() {
		e.logf("WARN", "core not running after power resume; triggering applyAndSync recovery")
		_, _ = e.applyAndSync(context.Background(), true, false)
		return
	}
	if settings.AILockEnabled {
		e.syncAIGuard(true)
	}
	if e.effectiveRuntimeSettings(settings).SystemProxyEnabled {
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
	var syncTick uint64
	for {
		select {
		case <-e.stopBgCh:
			return
		case <-coreSyncTicker.C:
			syncTick++
			e.syncFromLiveCore(context.Background(), syncTick)
		case <-netTicker.C:
			e.statusMu.RLock()
			prevSuspended := e.suspendedByNet
			e.statusMu.RUnlock()
			e.reconcileSystemProxy()
			e.statusMu.RLock()
			currSuspended := e.suspendedByNet
			e.statusMu.RUnlock()
			if currSuspended != prevSuspended {
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
				_, _ = e.refreshSubscriptionSmart(context.Background(), sub.Name)
			}
			_, _ = e.applyAndSync(context.Background(), false, false)
		}
	}
}

func (e *Engine) syncFromLiveCore(ctx context.Context, tick uint64) {
	if !e.opMu.TryLock() {
		return
	}

	rt := e.ctrl.LastState()
	coreSt := e.currentCoreStatus()
	coreRunning := coreSt.Running
	coreHealth := coreSt.Health
	aiGuardMode := ""
	if coreSt.AIGuard != nil {
		aiGuardMode = coreSt.AIGuard.Mode
	}

	e.statusMu.Lock()
	prevRunning := e.lastCoreRunning
	prevHealth := e.lastCoreHealth
	prevRestarts := e.lastRestartCount
	prevGuardMode := e.lastAIGuardMode
	desiredActive := e.desiredCoreActive
	lastTry := e.lastRecoveryTryAt

	stateTransition := (coreRunning != prevRunning) || (coreHealth != prevHealth) || (coreSt.RestartCount != prevRestarts) || (aiGuardMode != prevGuardMode)
	e.lastCoreRunning = coreRunning
	e.lastCoreHealth = coreHealth
	e.lastRestartCount = coreSt.RestartCount
	e.lastAIGuardMode = aiGuardMode

	if !coreRunning {
		if e.coreDownSince.IsZero() {
			e.coreDownSince = time.Now()
		}
	} else {
		e.coreDownSince = time.Time{}
	}
	e.statusMu.Unlock()

	if stateTransition {
		e.logf("INFO", "core state transition: running=%v->%v health=%s->%s restarts=%d->%d ai_guard=%s->%s",
			prevRunning, coreRunning, prevHealth, coreHealth, prevRestarts, coreSt.RestartCount, prevGuardMode, aiGuardMode)
	}

	if !coreRunning {
		e.opMu.Unlock()
		if stateTransition {
			e.reconcileSystemProxy()
			e.notifyStateChanged()
		}
		// If the core is expected to run and the privileged service itself is not already
		// running its own backoff supervisor (e.g. SCM cold-restarted ClashFXService),
		// proactively re-apply runtime config after a 5s cooldown.
		if desiredActive &&
			coreHealth != service.CoreHealthRestarting &&
			coreHealth != service.CoreHealthCrashLoop &&
			time.Since(lastTry) >= 5*time.Second {
			e.statusMu.Lock()
			e.lastRecoveryTryAt = time.Now()
			e.statusMu.Unlock()
			e.logf("WARN", "core unexpectedly down (health=%s); triggering daemon recovery applyAndSync", coreHealth)
			_, _ = e.applyAndSync(ctx, true, false)
		}
		return
	}

	if rt.APIPort <= 0 {
		e.opMu.Unlock()
		if stateTransition {
			e.notifyStateChanged()
		}
		return
	}

	settings := e.store.Settings()
	anyChanged := stateTransition
	needReconcileProxy := false

	// If core recovered from an auto-restart inside ClashFXService, replay saved group selections.
	recoveredFromRestart := (!prevRunning && coreRunning) || (coreSt.RestartCount > prevRestarts)

	// 1. Reconcile outbound mode under opMu so concurrent user applyAndSync calls never race
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
				needReconcileProxy = true
				anyChanged = true
			}
		}
	}

	// 2. Deep data-plane health check every 4 ticks (~12s): verify controller /version
	// AND (on Windows Enhanced Mode) Wintun "ClashFX" adapter liveness to catch wedged cores.
	triggerForcedRestart := false
	if tick%4 == 0 {
		verOK := fetchCoreVersionFromCore(ctx, rt.APIPort, rt.APISecret) != ""
		tunOK := true
		eff := e.effectiveRuntimeSettings(settings)
		if eff.EnhancedMode && e.isCoreViaService() && runtime.GOOS == "windows" {
			tunOK = coreSt.TunHealthy
		}
		e.statusMu.Lock()
		if verOK && tunOK {
			e.healthFailCount = 0
		} else {
			e.healthFailCount++
			fails := e.healthFailCount
			e.logf("WARN", "deep health check failed (count=%d/3, api_ok=%v, tun_ok=%v)", fails, verOK, tunOK)
			if fails >= 3 {
				e.healthFailCount = 0
				triggerForcedRestart = true
			}
		}
		e.statusMu.Unlock()
	}
	e.opMu.Unlock()

	if triggerForcedRestart {
		e.logf("ERROR", "core failed 3 consecutive deep health checks (wedged API or missing Wintun); forcing clean core restart")
		_ = e.RestartCore(ctx, false)
		return
	}

	if recoveredFromRestart {
		e.replaySavedSelections(ctx, rt.APIPort, rt.APISecret, settings.SelectedProfile)
		if settings.AILockEnabled {
			e.syncAIGuard(true)
		}
		needReconcileProxy = true
		anyChanged = true
	} else if settings.AILockEnabled && tick%10 == 0 {
		// Periodically rescan running processes & install roots so newly updated/launched AI apps enter WFP.
		e.syncAIGuard(false)
	}

	if needReconcileProxy {
		e.reconcileSystemProxy()
	}

	// 2. Reconcile live proxy group selections & delays from WebView / auto-URLTest
	var rawRoot map[string]any
	if data, err := e.readSelectedProfileBytes(settings.SelectedProfile); err == nil {
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

func (e *Engine) resolveFallbackProfileBytes(prof string) ([]byte, bool) {
	trimmed := strings.TrimSpace(prof)
	if trimmed == "" {
		return []byte(profile.DefaultStarterConfigYAML), true
	}
	if strings.EqualFold(trimmed, "config") {
		if _, ok := e.subMgr.FindByName(prof); !ok {
			return []byte(profile.DefaultStarterConfigYAML), true
		}
	}
	return nil, false
}

func (e *Engine) readSelectedProfileBytes(prof string) ([]byte, error) {
	if raw, ok := e.resolveFallbackProfileBytes(prof); ok {
		return raw, nil
	}
	return os.ReadFile(e.store.ProfilePath(prof))
}

func (e *Engine) watchActiveConfigDir() {
	_ = e.store.WatchActiveConfigDir(func(changedFile string) {
		base := filepath.Base(changedFile)
		ext := filepath.Ext(base)
		stem := strings.TrimSuffix(base, ext)
		selected := e.store.Settings().SelectedProfile
		isSelectedManagedSub := false
		if strings.EqualFold(stem, selected) {
			_, isSelectedManagedSub = e.subMgr.FindByName(stem)
		}
		if profile.IsProfileMixinFileName(base) || isSelectedManagedSub {
			_, _ = e.applyAndSync(context.Background(), false, false)
		}
	})
}

func (e *Engine) Snapshot() DaemonSnapshot {
	settings := e.store.Settings()
	rt := e.ctrl.LastState()
	subs := e.subMgr.List()
	profiles := make([]string, 0, len(subs))
	for _, sub := range subs {
		profiles = append(profiles, sub.Name)
	}
	nsSnap, _ := e.nodeCoord.Snapshot(settings.SelectedProfile)
	sig := e.currentNetworkSignature(false)

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

	e.statusMu.RLock()
	suspended := e.suspendedByNet
	viaSvc := e.coreViaService
	coreVer := e.coreVersion
	lastErr := e.lastApplyErr
	logsDir := e.logsDir
	e.statusMu.RUnlock()
	if logsDir == "" {
		logsDir = filepath.Join(e.store.AppDataDir(), "logs")
		_ = os.MkdirAll(logsDir, 0700)
	}

	coreSt := e.currentCoreStatus()
	coreRunning := coreSt.Running
	svcInstalled := (viaSvc && coreRunning) || IsPrivilegedServiceInstalled(service.ServiceName)
	coreHealth := coreSt.Health
	if coreHealth == "" {
		if coreRunning {
			coreHealth = service.CoreHealthHealthy
		} else {
			coreHealth = service.CoreHealthStopped
		}
	}
	var aiGuardSt service.AIGuardStatus
	if coreSt.AIGuard != nil {
		aiGuardSt = *coreSt.AIGuard
	} else {
		aiGuardSt.Mode = service.AIGuardModeInactive
	}
	coreLogFile := coreSt.CoreLogFile
	if coreLogFile == "" {
		coreLogFile = filepath.Join(logsDir, "mihomo-core.log")
	}
	daemonLogFile := filepath.Join(logsDir, "clashfx-daemon.log")
	if e.daemonLog != nil && e.daemonLog.Path() != "" {
		daemonLogFile = e.daemonLog.Path()
	}
	svcLogFile := coreSt.ServiceLogFile
	if svcLogFile == "" {
		svcLogFile = filepath.Join(logsDir, "clashfx-service-events.log")
	}

	return DaemonSnapshot{
		Version:                AppVersion,
		CoreVersion:            coreVer,
		Platform:               runtime.GOOS,
		Arch:                   runtime.GOARCH,
		CoreRunning:            coreRunning,
		CoreHealth:             coreHealth,
		CoreRestartCount:       coreSt.RestartCount,
		LastCrashAt:            coreSt.LastCrashAt,
		LastCrashReason:        coreSt.LastCrashReason,
		CoreViaService:         viaSvc,
		ServiceInstalled:       svcInstalled,
		AIGuard:                aiGuardSt,
		Settings:               settings,
		Runtime:                rt,
		DashboardURL:           buildDashboardURL(rt.APIPort, rt.APISecret),
		Profiles:               profiles,
		ProfileMixinPath:       e.store.ProfileMixinPath(),
		ConfigDir:              e.store.ActiveConfigDir(),
		LogsDir:                logsDir,
		CoreLogFile:            coreLogFile,
		DaemonLogFile:          daemonLogFile,
		ServiceLogFile:         svcLogFile,
		ProxyGroups:            groups,
		DefaultRuleTarget:      defTarget,
		AILockCandidates:       cands,
		AILockRelay:            relay,
		Subscriptions:          subs,
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

	// Load candidate targets + relay from selected profile YAML (+ proxy-providers) even if core isn't up
	var rawRoot map[string]any
	if data, err := e.readSelectedProfileBytes(settings.SelectedProfile); err == nil {
		rawRoot, _ = profile.UnmarshalYAMLMap(data)
	}
	var cands []ailock.CandidateNode
	var relay string
	defTarget := "DIRECT"
	if rawRoot != nil {
		providers := e.loadProviderSnapshots(rawRoot)
		cands = ailock.ListCandidateTargets(rawRoot, providers)
		defTarget = profile.ResolveDefaultRuleTarget(rawRoot)
		if settings.AILockEnabled {
			relay = ailock.RelayName(rawRoot, settings.AILockTarget, providers)
		}
	}

	var groups []ProxyGroupView
	var provInfo *subscription.UserInfo
	var liveVer string
	if rt.APIPort > 0 && e.IsCoreRunning() {
		groups = fetchProxyGroupsFromCore(ctx, rt.APIPort, rt.APISecret, rawRoot, e.store.RecoverySelectionsForProfile(settings.SelectedProfile))
		provInfo = fetchProviderSubscriptionInfo(ctx, rt.APIPort, rt.APISecret)
		liveVer = fetchCoreVersionFromCore(ctx, rt.APIPort, rt.APISecret)
	}

	if liveVer != "" {
		e.statusMu.Lock()
		e.coreVersion = liveVer
		e.statusMu.Unlock()
	}

	e.proxyGroupsMu.Lock()
	e.proxyGroups = groups
	e.defaultRuleTarget = defTarget
	e.aiCandidates = cands
	e.aiRelay = relay
	e.providerSubInfo = provInfo
	e.proxyGroupsMu.Unlock()
}

func (e *Engine) activeLocalProxyPort() int {
	rt := e.ctrl.LastState()
	if rt.MixedPort > 0 && e.IsCoreRunning() {
		return rt.MixedPort
	}
	return 0
}

func (e *Engine) activeDirectRefreshPort() int {
	if !e.IsCoreRunning() {
		return 0
	}
	settings := e.store.Settings()
	if settings.EnhancedMode && settings.CustomConfigAsIs {
		return 0
	}
	_, directPtr, _, err := e.nodeStore.ActiveBindings(settings.SelectedProfile)
	if err == nil && directPtr != nil && *directPtr > 0 {
		return *directPtr
	}
	return 0
}

func (e *Engine) refreshSubscriptionSmart(ctx context.Context, name string) (subscription.ManagedSubscription, error) {
	return e.subMgr.RefreshWithFallback(ctx, name, e.activeLocalProxyPort(), e.activeDirectRefreshPort())
}

func (e *Engine) loadProviderProxiesForNodeServices(rawRoot map[string]any) []nodeservices.ProviderProxy {
	snaps := e.loadProviderSnapshots(rawRoot)
	var out []nodeservices.ProviderProxy
	for _, snap := range snaps {
		for _, px := range snap.Proxies {
			out = append(out, nodeservices.ProviderProxy{
				ProviderName: snap.Name,
				Proxy:        px,
			})
		}
	}
	return out
}

func (e *Engine) loadProviderSnapshots(rawRoot map[string]any) []ailock.ProviderSnapshot {
	if rawRoot == nil {
		return nil
	}
	defs, ok := rawRoot["proxy-providers"].(map[string]any)
	if !ok || len(defs) == 0 {
		return nil
	}

	var names []string
	for k := range defs {
		if strings.TrimSpace(k) != "" && k != "default" {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil
	}

	var liveMap map[string][]map[string]any
	if rt := e.ctrl.LastState(); rt.APIPort > 0 && e.IsCoreRunning() {
		liveMap = fetchLiveProviderProxies(rt.APIPort, rt.APISecret)
	}

	cfgDir := filepath.Clean(e.store.ActiveConfigDir())
	out := make([]ailock.ProviderSnapshot, 0, len(names))
	for _, name := range names {
		defMap, _ := defs[name].(map[string]any)
		var dialerProxy, overrideDialer string
		if defMap != nil {
			dialerProxy, _ = defMap["dialer-proxy"].(string)
			if ov, ok := defMap["override"].(map[string]any); ok {
				overrideDialer, _ = ov["dialer-proxy"].(string)
			}
		}

		// Prefer full outbound definitions from inline payload or on-disk provider cache
		// so server host & dialer-proxy materialization remain complete.
		proxies := loadProviderProxiesFromDef(cfgDir, defMap)
		if len(proxies) == 0 && liveMap != nil {
			proxies = liveMap[name]
		}
		if len(proxies) == 0 {
			continue
		}
		out = append(out, ailock.ProviderSnapshot{
			Name:                name,
			DialerProxy:         strings.TrimSpace(dialerProxy),
			OverrideDialerProxy: strings.TrimSpace(overrideDialer),
			Proxies:             proxies,
		})
	}
	return out
}

func loadProviderProxiesFromDef(cfgDir string, defMap map[string]any) []map[string]any {
	if defMap == nil {
		return nil
	}
	if inline := extractProxyMaps(defMap["payload"]); len(inline) > 0 {
		return inline
	}

	var candPath string
	relPath, _ := defMap["path"].(string)
	relPath = strings.TrimSpace(relPath)
	if relPath != "" {
		if filepath.IsAbs(relPath) {
			candPath = filepath.Clean(relPath)
		} else {
			candPath = filepath.Clean(filepath.Join(cfgDir, relPath))
		}
	} else if rawURL, _ := defMap["url"].(string); strings.TrimSpace(rawURL) != "" {
		hash := mihomoutils.MakeHash([]byte(strings.TrimSpace(rawURL))).String()
		candPath = filepath.Join(cfgDir, "proxies", hash)
	}
	if candPath == "" {
		return nil
	}
	if rel, err := filepath.Rel(cfgDir, candPath); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil
	}
	f, err := os.Open(candPath)
	if err != nil {
		return nil
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 16<<20))
	if err != nil || len(data) == 0 {
		return nil
	}
	if doc, err := profile.UnmarshalYAMLMap(data); err == nil {
		if list := extractProxyMaps(doc["proxies"]); len(list) > 0 {
			return list
		}
	}
	if converted, err := subscription.ConvertShareLinksPayload(data); err == nil {
		if doc, err := profile.UnmarshalYAMLMap(converted); err == nil {
			return extractProxyMaps(doc["proxies"])
		}
	}
	return nil
}

func extractProxyMaps(v any) []map[string]any {
	switch typed := v.(type) {
	case []map[string]any:
		return typed
	case []any:
		var out []map[string]any
		for _, item := range typed {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	default:
		return nil
	}
}

func fetchLiveProviderProxies(apiPort int, secret string) map[string][]map[string]any {
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/providers/proxies", apiPort), nil)
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
			VehicleType string           `json:"vehicleType"`
			Proxies     []map[string]any `json:"proxies"`
		} `json:"providers"`
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if json.Unmarshal(body, &payload) != nil {
		return nil
	}
	out := make(map[string][]map[string]any, len(payload.Providers))
	for k, prov := range payload.Providers {
		if k == "default" || strings.EqualFold(prov.VehicleType, "Compatible") || len(prov.Proxies) == 0 {
			continue
		}
		out[k] = prov.Proxies
	}
	return out
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
	if enabled && supportsPrivilegedService() && !IsPrivilegedServiceInstalled(service.ServiceName) {
		if !allowUAC {
			return ErrServiceInstallRequired
		}
		if err := ElevateAndInstallService(serviceExecutablePath(e.cfg.BinDir)); err != nil {
			return err
		}
	}
	prev := cur.EnhancedMode
	if _, err := e.store.UpdateSettings(func(s *profile.AppSettings) error {
		s.EnhancedMode = enabled
		return nil
	}); err != nil {
		return err
	}
	if _, err := e.applyAndSync(ctx, true, allowUAC); err != nil {
		// Roll back EnhancedMode setting if UAC cancelled or TUN failed!
		_, _ = e.store.UpdateSettings(func(s *profile.AppSettings) error {
			s.EnhancedMode = prev
			return nil
		})
		_, _ = e.applyAndSync(ctx, true, false)
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
	e.refreshLiveCoreMetadata(ctx)
	e.notifyStateChanged()
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

	e.refreshLiveCoreMetadata(ctx)
	e.proxyGroupsMu.RLock()
	groups := append([]ProxyGroupView(nil), e.proxyGroups...)
	cands := append([]ailock.CandidateNode(nil), e.aiCandidates...)
	e.proxyGroupsMu.RUnlock()

	covered := map[string]bool{}
	groupSem := make(chan struct{}, 6)
	var groupWG sync.WaitGroup
	var coveredMu sync.Mutex
	for _, g := range groups {
		if g.Name == "GLOBAL" && len(groups) > 1 {
			continue
		}
		gURL := strings.TrimSpace(g.TestURL)
		if gURL == "" {
			gURL = benchURL
		}
		groupWG.Add(1)
		go func(group ProxyGroupView, targetURL string) {
			defer groupWG.Done()
			groupSem <- struct{}{}
			defer func() { <-groupSem }()
			u := fmt.Sprintf("http://127.0.0.1:%d/group/%s/delay?url=%s&timeout=5000",
				rt.APIPort, url.PathEscape(group.Name), url.QueryEscape(targetURL))
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
			if err != nil {
				return
			}
			if rt.APISecret != "" {
				req.Header.Set("Authorization", "Bearer "+rt.APISecret)
			}
			resp, doErr := (&http.Client{Timeout: 12 * time.Second}).Do(req)
			if doErr != nil {
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				coveredMu.Lock()
				for _, it := range group.Items {
					covered[it.Name] = true
				}
				coveredMu.Unlock()
			}
		}(g, gURL)
	}
	groupWG.Wait()

	// Probe any standalone/provider nodes that weren't already covered by a group batch test
	sem := make(chan struct{}, 16)
	var wg sync.WaitGroup
	for _, cand := range cands {
		if covered[cand.Name] {
			continue
		}
		wg.Add(1)
		go func(node ailock.CandidateNode) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			_, _ = features.ProbeSingleNode(ctx, rt.APIPort, rt.APISecret, node.Name, node.Provider, benchURL, 5000, "en")
		}(cand)
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
			if s.EnhancedMode && supportsPrivilegedService() && !IsPrivilegedServiceInstalled(service.ServiceName) && !allowUAC {
				s.EnhancedMode = false
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
		e.syncAIGuard(true)
		_, _ = e.applyAndSync(ctx, false, false)
		return err
	}
	e.syncAIGuard(true)
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
	prevSubs := e.subMgr.List()
	sub, err := e.subMgr.AddOrUpdate(name, rawURL, userAgent)
	if err != nil {
		return subscription.ManagedSubscription{}, err
	}
	wasExisting := false
	for _, s := range prevSubs {
		if s.Name == sub.Name {
			wasExisting = true
			break
		}
	}
	updated, err := e.refreshSubscriptionSmart(ctx, sub.Name)
	if err != nil {
		if !wasExisting {
			if _, statErr := os.Stat(e.store.ProfilePath(sub.Name)); os.IsNotExist(statErr) {
				_ = e.subMgr.Remove(sub.Name, false)
			}
		}
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
	updated, err := e.refreshSubscriptionSmart(ctx, name)
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
	if strings.EqualFold(e.store.Settings().SelectedProfile, name) {
		fallback := "config"
		if subs := e.subMgr.List(); len(subs) > 0 {
			fallback = subs[0].Name
		}
		_ = e.SelectProfile(ctx, fallback)
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
	if scheme != "clash" && scheme != "clashfx" && scheme != "clash-verge" {
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
	proxyPort := e.activeLocalProxyPort()
	mmdbData, err := e.subMgr.DownloadExternalResourceViaProxy(ctx, settings.MMDBDownloadURL, "Country.mmdb", 1024, proxyPort)
	if err != nil {
		return fmt.Errorf("update Country.mmdb: %w", err)
	}
	geositeData, err := e.subMgr.DownloadExternalResourceViaProxy(ctx, settings.GeoSiteDownloadURL, "geosite.dat", 1024, proxyPort)
	if err != nil {
		return fmt.Errorf("update geosite.dat: %w", err)
	}

	cfgDir := e.store.ActiveConfigDir()
	writeBoth := func() error {
		if err := profile.WriteFileAtomic(filepath.Join(cfgDir, "Country.mmdb"), mmdbData, 0644); err != nil {
			return fmt.Errorf("write Country.mmdb: %w", err)
		}
		if err := profile.WriteFileAtomic(filepath.Join(cfgDir, "geosite.dat"), geositeData, 0644); err != nil {
			return fmt.Errorf("write geosite.dat: %w", err)
		}
		return nil
	}

	wasRunning := e.IsCoreRunning()
	if err := writeBoth(); err != nil {
		if !wasRunning {
			return err
		}
		// On Windows, mihomo_core.exe holds open file handles on Country.mmdb / geosite.dat.
		// Stop core briefly while replacing files atomically, then restart cleanly.
		_ = e.localRunner.Stop()
		if e.isCoreViaService() {
			_, _ = callService(6*time.Second, service.Request{Action: "stop_core"})
		}
		if retryErr := writeBoth(); retryErr != nil {
			_, _ = e.applyAndSync(ctx, true, false)
			return retryErr
		}
	}
	if wasRunning {
		_, err := e.applyAndSync(ctx, true, false)
		return err
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
		rt := e.ctrl.LastState()
		reserved := map[int]bool{7890: true}
		for _, p := range []int{rt.MixedPort, rt.HTTPPort, rt.SocksPort, rt.APIPort, rt.DNSPort} {
			if p > 0 {
				reserved[p] = true
			}
		}
		if err := e.nodeStore.UpdateProfileBindingsWithReserved(prof, ports, enabled, reserved); err != nil {
			return err
		}
	}
	_, err := e.applyAndSync(ctx, false, false)
	return err
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func coreOrProxySettingsChanged(a, b profile.AppSettings) bool {
	return a.SelectedProfile != b.SelectedProfile ||
		a.CustomConfigDir != b.CustomConfigDir ||
		a.OutboundMode != b.OutboundMode ||
		a.AllowLan != b.AllowLan ||
		a.IPv6 != b.IPv6 ||
		a.SystemProxyEnabled != b.SystemProxyEnabled ||
		a.EnhancedMode != b.EnhancedMode ||
		a.CustomConfigAsIs != b.CustomConfigAsIs ||
		a.BypassCNApps != b.BypassCNApps ||
		a.PortOverride != b.PortOverride ||
		a.APIPort != b.APIPort ||
		a.APISecret != b.APISecret ||
		a.LogLevel != b.LogLevel ||
		a.TunMTU != b.TunMTU ||
		a.TunStack != b.TunStack ||
		a.TunInterfaceName != b.TunInterfaceName ||
		a.TunRouteExcludes != b.TunRouteExcludes ||
		a.ProxyBypassList != b.ProxyBypassList ||
		a.AILockEnabled != b.AILockEnabled ||
		a.AILockTarget != b.AILockTarget ||
		!stringSlicesEqual(a.SuspendNetworks, b.SuspendNetworks)
}

func (e *Engine) UpdateGeneralSettings(ctx context.Context, next profile.AppSettings) error {
	cur := e.store.Settings()
	if strings.TrimSpace(next.SelectedProfile) == "" {
		next.SelectedProfile = cur.SelectedProfile
	}
	if cur.AILockEnabled {
		next.AILockEnabled = cur.AILockEnabled
		next.AILockTarget = cur.AILockTarget
		ailock.EnforceProtectedSettings(&next)
	}
	next.AILockSavedState = cur.AILockSavedState
	next.SavedSelections = cur.SavedSelections

	if next.LaunchAtLogin != cur.LaunchAtLogin {
		uiExe := uiLaunchExecutablePath(e.cfg.BinDir)
		if err := SetLaunchAtLogin(next.LaunchAtLogin, uiExe); err != nil {
			return fmt.Errorf("update Launch at Login: %w", err)
		}
	}

	if _, err := e.store.UpdateSettings(func(s *profile.AppSettings) error {
		*s = next
		return nil
	}); err != nil {
		return err
	}

	if !coreOrProxySettingsChanged(cur, next) {
		e.notifyStateChanged()
		return nil
	}

	if next.CustomConfigDir != cur.CustomConfigDir {
		e.watchActiveConfigDir()
	}

	_, err := e.applyAndSync(ctx, false, true)
	return err
}

func (e *Engine) currentProfileDefaultTarget() (string, bool) {
	settings := e.store.Settings()
	var rawRoot map[string]any
	if data, err := e.readSelectedProfileBytes(settings.SelectedProfile); err == nil {
		rawRoot, _ = profile.UnmarshalYAMLMap(data)
	}
	if rawRoot == nil {
		if data, err := os.ReadFile(e.ctrl.RuntimeConfigPath()); err == nil {
			rawRoot, _ = profile.UnmarshalYAMLMap(data)
		}
	}
	if rawRoot == nil {
		return "DIRECT", false
	}
	defTarget := profile.ResolveDefaultRuleTarget(rawRoot)
	literalPROXY := false
	for _, key := range []string{"proxy-groups", "proxies"} {
		if list, ok := rawRoot[key].([]any); ok {
			for _, item := range list {
				if m, ok := item.(map[string]any); ok {
					if n, _ := m["name"].(string); strings.TrimSpace(n) == profile.ProxyTargetPlaceholder {
						literalPROXY = true
					}
				}
			}
		}
	}
	return defTarget, literalPROXY
}

func (e *Engine) GetProfileMixin() (features.ProfileMixinDocument, error) {
	path, err := e.store.EnsureProfileMixinFile()
	if err != nil {
		return features.ProfileMixinDocument{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return features.ProfileMixinDocument{}, err
	}
	defTarget, _ := e.currentProfileDefaultTarget()
	doc, err := features.ParseProfileMixinDocument(raw, defTarget)
	if err != nil {
		// Still return raw YAML if file has syntax errors so user can fix it in Raw tab
		return features.ProfileMixinDocument{
			Path:          path,
			YAML:          string(raw),
			Rules:         []features.MixinRuleItem{},
			DefaultTarget: defTarget,
		}, nil
	}
	doc.Path = path
	return doc, nil
}

func (e *Engine) ConvertProfileMixin(direction, rawYAML string, bypassCNWeb, bypassLAN bool, rules []features.MixinRuleItem) (features.ProfileMixinDocument, error) {
	defTarget, _ := e.currentProfileDefaultTarget()
	dir := strings.TrimSpace(direction)
	if strings.EqualFold(dir, "from_yaml") || strings.EqualFold(dir, "to_structured") {
		return features.ParseProfileMixinDocument([]byte(rawYAML), defTarget)
	}
	built, err := features.BuildProfileMixinYAML([]byte(rawYAML), bypassCNWeb, bypassLAN, rules)
	if err != nil {
		return features.ProfileMixinDocument{}, err
	}
	return features.ParseProfileMixinDocument(built, defTarget)
}

func (e *Engine) SaveProfileMixin(ctx context.Context, mode, rawYAML, baseYAML string, bypassCNWeb, bypassLAN bool, rules []features.MixinRuleItem) (features.ProfileMixinDocument, error) {
	var finalYAML []byte
	var err error
	m := strings.TrimSpace(mode)
	if strings.EqualFold(m, "yaml") || strings.EqualFold(m, "raw") {
		sanitized := profile.SanitizeYAMLIndentation([]byte(rawYAML))
		if _, parseErr := profile.UnmarshalYAMLMap(sanitized); parseErr != nil {
			return features.ProfileMixinDocument{}, fmt.Errorf("YAML 格式错误: %w", parseErr)
		}
		finalYAML = sanitized
	} else {
		base := []byte(baseYAML)
		if len(strings.TrimSpace(baseYAML)) == 0 {
			base, _ = e.store.ReadProfileMixinBytes()
		}
		finalYAML, err = features.BuildProfileMixinYAML(base, bypassCNWeb, bypassLAN, rules)
		if err != nil {
			return features.ProfileMixinDocument{}, err
		}
	}

	path, err := e.store.EnsureProfileMixinFile()
	if err != nil {
		return features.ProfileMixinDocument{}, err
	}
	prevBytes, prevErr := os.ReadFile(path)

	e.store.MarkInternalWrite(path)
	if err := profile.WriteFileAtomic(path, finalYAML, 0600); err != nil {
		return features.ProfileMixinDocument{}, err
	}

	if _, err := e.applyAndSync(ctx, false, false); err != nil {
		if prevErr == nil {
			e.store.MarkInternalWrite(path)
			_ = profile.WriteFileAtomic(path, prevBytes, 0600)
			_, _ = e.applyAndSync(ctx, false, false)
		}
		return features.ProfileMixinDocument{}, err
	}
	e.closeAllConnections(ctx)
	return e.GetProfileMixin()
}

func (e *Engine) GetEffectiveRules(ctx context.Context) (EffectiveRulesView, error) {
	settings := e.store.Settings()
	rt := e.ctrl.LastState()

	var rawRoot map[string]any
	if data, err := os.ReadFile(e.ctrl.RuntimeConfigPath()); err == nil {
		rawRoot, _ = profile.UnmarshalYAMLMap(data)
	}
	if rawRoot == nil {
		if data, err := e.readSelectedProfileBytes(settings.SelectedProfile); err == nil {
			rawRoot, _ = profile.UnmarshalYAMLMap(data)
		}
	}

	defTarget, literalPROXY := e.currentProfileDefaultTarget()
	if rawRoot != nil && defTarget == "DIRECT" {
		defTarget = profile.ResolveDefaultRuleTarget(rawRoot)
	}

	mixinBytes, _ := e.store.ReadProfileMixinBytes()
	mixinKeys := features.ExtractMixinRuleKeys(mixinBytes, defTarget, literalPROXY)

	normRuleKey := func(tp, payload, proxy string) string {
		cleanType := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(tp)), "-", "")
		return cleanType + "|" + strings.ToLower(strings.TrimSpace(payload)) + "|" + strings.TrimSpace(proxy)
	}

	classifySource := func(tp, payload, proxy string) string {
		switch strings.TrimSpace(payload) {
		case profile.InternalDirectRuleProviderName, profile.PrivateDirectRuleProviderName:
			return "system"
		case profile.AILockRuleProviderName:
			return "ai_lock"
		case profile.CNAppsRuleProviderName:
			return "cn_apps"
		}
		if mixinKeys[normRuleKey(tp, payload, proxy)] {
			return "mixin"
		}
		return "profile"
	}

	_ = e.store.EnsureBuiltinRulesets()
	cfgDir := e.store.ActiveConfigDir()

	out := EffectiveRulesView{
		OutboundMode:  settings.OutboundMode,
		DefaultTarget: defTarget,
		RuleProviders: map[string]RuleProviderView{},
	}

	loadBuiltinView := func(name string) (RuleProviderView, bool) {
		absPath := profile.BuiltinRulesetAbsPath(cfgDir, name)
		rawBytes, err := os.ReadFile(absPath)
		if err != nil {
			return RuleProviderView{}, false
		}
		rawText := string(rawBytes)
		parsed, _ := profile.ParseAndValidateClassicalRulesetText(rawText)
		return RuleProviderView{
			Name:        name,
			Behavior:    "Classical",
			VehicleType: "File",
			RuleCount:   len(parsed),
			Payload:     parsed,
			Editable:    true,
			CanReset:    true,
			FilePath:    absPath,
			RawText:     rawText,
		}, true
	}

	for _, builtinName := range []string{profile.AILockRuleProviderName, profile.CNAppsRuleProviderName} {
		if v, ok := loadBuiltinView(builtinName); ok {
			out.RuleProviders[builtinName] = v
		}
	}

	// Seed inline & file rule-providers from runtime YAML as fallback
	if rawRoot != nil {
		if rpMap, ok := rawRoot["rule-providers"].(map[string]any); ok {
			for name, rawDef := range rpMap {
				if profile.IsEditableBuiltinRuleset(name) {
					if _, exists := out.RuleProviders[name]; exists {
						continue
					}
				}
				dm, ok := rawDef.(map[string]any)
				if !ok {
					continue
				}
				pList := profile.ExtractStringRules(dm["payload"])
				vType, _ := dm["type"].(string)
				beh, _ := dm["behavior"].(string)
				var rawText, filePath string
				if strings.EqualFold(vType, "file") {
					if relP, _ := dm["path"].(string); strings.TrimSpace(relP) != "" {
						candPath := filepath.Clean(filepath.Join(cfgDir, relP))
						if relCheck, relErr := filepath.Rel(cfgDir, candPath); relErr == nil && !strings.HasPrefix(relCheck, "..") {
							filePath = candPath
							if fb, rErr := os.ReadFile(candPath); rErr == nil {
								rawText = string(fb)
								if len(pList) == 0 {
									pList, _ = profile.ParseAndValidateClassicalRulesetText(rawText)
								}
							}
						}
					}
				}
				out.RuleProviders[name] = RuleProviderView{
					Name:        name,
					Behavior:    beh,
					VehicleType: vType,
					RuleCount:   len(pList),
					Payload:     pList,
					FilePath:    filePath,
					RawText:     rawText,
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
					seeded := out.RuleProviders[k]
					payload := p.Payload
					if len(payload) == 0 {
						payload = seeded.Payload
					}
					out.RuleProviders[k] = RuleProviderView{
						Name:        k,
						Behavior:    p.Behavior,
						VehicleType: p.VehicleType,
						RuleCount:   p.RuleCount,
						Payload:     payload,
						Editable:    seeded.Editable,
						CanReset:    seeded.CanReset,
						FilePath:    seeded.FilePath,
						RawText:     seeded.RawText,
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

func (e *Engine) refreshLiveRuleProvider(ctx context.Context, rt transaction.AppliedRuntimeState, providerName string) (found bool, err error) {
	if rt.APIPort <= 0 || !e.IsCoreRunning() {
		return false, nil
	}
	client := &http.Client{Timeout: 4 * time.Second}
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/providers/rules/%s", rt.APIPort, url.PathEscape(providerName))
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, nil)
	if err != nil {
		return false, err
	}
	if rt.APISecret != "" {
		req.Header.Set("Authorization", "Bearer "+rt.APISecret)
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return true, fmt.Errorf("Mihomo 热更新规则集失败 (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return true, nil
}

func (e *Engine) SaveRuleProvider(ctx context.Context, providerName, content string) (EffectiveRulesView, error) {
	e.opMu.Lock()
	defer e.opMu.Unlock()

	providerName = strings.TrimSpace(providerName)
	if !profile.IsEditableBuiltinRuleset(providerName) {
		return EffectiveRulesView{}, fmt.Errorf("规则集 %q 不支持直接编辑", providerName)
	}

	parsedRules, err := profile.ParseAndValidateClassicalRulesetText(content)
	if err != nil {
		return EffectiveRulesView{}, err
	}
	if len(parsedRules) == 0 {
		if providerName == profile.AILockRuleProviderName {
			return EffectiveRulesView{}, errors.New("AI 锁定规则集不能为空（至少保留一条匹配规则，否则 AI 锁定将失去保护）")
		}
		return EffectiveRulesView{}, errors.New("规则集不能为空（如需整体停用，请在设置中关闭「绕过常用国内应用」）")
	}

	if err := e.store.EnsureBuiltinRulesets(); err != nil {
		return EffectiveRulesView{}, err
	}
	absPath := e.store.BuiltinRulesetPath(providerName)
	prevBytes, prevErr := os.ReadFile(absPath)

	normalized := profile.NormalizeRulesetContent(content)
	if err := profile.WriteFileAtomic(absPath, []byte(normalized), 0600); err != nil {
		return EffectiveRulesView{}, fmt.Errorf("写入规则集文件失败: %w", err)
	}

	rt := e.ctrl.LastState()
	mounted, err := e.refreshLiveRuleProvider(ctx, rt, providerName)
	if err != nil {
		if prevErr == nil {
			_ = profile.WriteFileAtomic(absPath, prevBytes, 0600)
			_, _ = e.refreshLiveRuleProvider(ctx, rt, providerName)
		}
		return EffectiveRulesView{}, err
	}

	view, err := e.GetEffectiveRules(ctx)
	if err != nil {
		return EffectiveRulesView{}, err
	}
	if mounted {
		if prov, ok := view.RuleProviders[providerName]; ok && prov.RuleCount != len(parsedRules) {
			if prevErr == nil {
				_ = profile.WriteFileAtomic(absPath, prevBytes, 0600)
				_, _ = e.refreshLiveRuleProvider(ctx, rt, providerName)
			}
			return EffectiveRulesView{}, fmt.Errorf("内核规则集解析条数不一致（预期 %d 条，实际加载 %d 条），已自动回滚，请检查是否存在特殊语法错误", len(parsedRules), prov.RuleCount)
		}
	}
	if providerName == profile.AILockRuleProviderName && e.store.Settings().AILockEnabled {
		go e.syncAIGuard(true)
	}
	return view, nil
}

func (e *Engine) ResetRuleProvider(ctx context.Context, providerName string) (EffectiveRulesView, error) {
	providerName = strings.TrimSpace(providerName)
	defText, ok := profile.DefaultBuiltinRulesetText(providerName)
	if !ok {
		return EffectiveRulesView{}, fmt.Errorf("规则集 %q 没有可恢复的默认清单", providerName)
	}
	return e.SaveRuleProvider(ctx, providerName, defText)
}

func (e *Engine) SetRuleDisabled(ctx context.Context, indices []int, disabled bool) (EffectiveRulesView, error) {
	e.opMu.Lock()
	defer e.opMu.Unlock()

	if len(indices) == 0 {
		return e.GetEffectiveRules(ctx)
	}
	rt := e.ctrl.LastState()
	if rt.APIPort <= 0 || !e.IsCoreRunning() {
		return EffectiveRulesView{}, errors.New("内核当前未运行，无法动态开关顶层规则")
	}

	currentView, err := e.GetEffectiveRules(ctx)
	if err != nil {
		return EffectiveRulesView{}, err
	}
	byIndex := make(map[int]EffectiveRuleItem, len(currentView.Rules))
	for _, r := range currentView.Rules {
		byIndex[r.Index] = r
	}

	patchMap := make(map[string]bool, len(indices)+2)
	syncAILockPair := false
	for _, idx := range indices {
		r, exists := byIndex[idx]
		if !exists {
			return EffectiveRulesView{}, fmt.Errorf("规则索引 #%d 已失效，请先刷新规则列表", idx+1)
		}
		if r.Source == "system" || strings.TrimSpace(r.Payload) == profile.InternalDirectRuleProviderName || strings.TrimSpace(r.Payload) == profile.PrivateDirectRuleProviderName {
			return EffectiveRulesView{}, errors.New("系统守护规则 (clashfx-internal-direct / clashfx-private-direct) 不可禁用")
		}
		patchMap[fmt.Sprintf("%d", idx)] = disabled
		if strings.TrimSpace(r.Payload) == profile.AILockRuleProviderName {
			syncAILockPair = true
		}
	}
	if syncAILockPair {
		for _, r := range currentView.Rules {
			if strings.TrimSpace(r.Payload) == profile.AILockRuleProviderName {
				patchMap[fmt.Sprintf("%d", r.Index)] = disabled
			}
		}
	}

	rawBody, err := json.Marshal(patchMap)
	if err != nil {
		return EffectiveRulesView{}, err
	}
	client := &http.Client{Timeout: 4 * time.Second}
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/rules/disable", rt.APIPort)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, endpoint, bytes.NewReader(rawBody))
	if err != nil {
		return EffectiveRulesView{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if rt.APISecret != "" {
		req.Header.Set("Authorization", "Bearer "+rt.APISecret)
	}
	resp, err := client.Do(req)
	if err != nil {
		return EffectiveRulesView{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return EffectiveRulesView{}, fmt.Errorf("切换顶层规则状态失败 (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return e.GetEffectiveRules(ctx)
}

func (e *Engine) DiagnosticInfo() string {
	rt := e.ctrl.LastState()
	osState, _ := e.sysProxy.CurrentOSState()
	e.statusMu.RLock()
	viaSvc := e.coreViaService
	suspendedNet := e.suspendedByNet
	logsDir := e.logsDir
	e.statusMu.RUnlock()

	coreSt := e.currentCoreStatus()
	coreLogFile := coreSt.CoreLogFile
	if coreLogFile == "" && logsDir != "" {
		coreLogFile = filepath.Join(logsDir, "mihomo-core.log")
	}
	daemonLogFile := ""
	if e.daemonLog != nil {
		daemonLogFile = e.daemonLog.Path()
	} else if logsDir != "" {
		daemonLogFile = filepath.Join(logsDir, "clashfx-daemon.log")
	}
	svcLogFile := coreSt.ServiceLogFile
	if svcLogFile == "" && logsDir != "" {
		svcLogFile = filepath.Join(logsDir, "clashfx-service-events.log")
	}

	recentCoreLogs := applog.TailFile(coreLogFile, 80)
	if len(recentCoreLogs) == 0 {
		recentCoreLogs = coreSt.RecentLogs
	}
	recentDaemonLogs := applog.TailFile(daemonLogFile, 50)

	var guardMode, guardBackend, guardErr string
	var guardArmed bool
	var guardProtected int
	if coreSt.AIGuard != nil {
		guardMode = coreSt.AIGuard.Mode
		guardBackend = coreSt.AIGuard.Backend
		guardArmed = coreSt.AIGuard.Armed
		guardProtected = coreSt.AIGuard.ProtectedCount
		guardErr = coreSt.AIGuard.LastError
	}

	return features.BuildRedactedDiagnosticReport(features.DiagnosticBundleInput{
		AppVersion:       AppVersion,
		CoreRunning:      coreSt.Running,
		CoreHealth:       coreSt.Health,
		CoreRestartCount: coreSt.RestartCount,
		LastCrashAt:      coreSt.LastCrashAt,
		LastCrashReason:  coreSt.LastCrashReason,
		CoreViaService:   viaSvc,
		ServiceInstalled: IsPrivilegedServiceInstalled(service.ServiceName),
		MixedPort:        rt.MixedPort,
		APIPort:          rt.APIPort,
		DNSPort:          rt.DNSPort,
		Settings:         e.store.Settings(),
		SysProxyServer:   osState.ProxyServer,
		SysProxyEnabled:  osState.ProxyEnable == 1,
		SuspendedNet:     suspendedNet,
		AIGuardMode:      guardMode,
		AIGuardBackend:   guardBackend,
		AIGuardArmed:     guardArmed,
		AIGuardProtected: guardProtected,
		AIGuardError:     guardErr,
		LogsDir:          logsDir,
		CoreLogFile:      coreLogFile,
		DaemonLogFile:    daemonLogFile,
		ServiceLogFile:   svcLogFile,
		RecentCoreLogs:   recentCoreLogs,
		RecentDaemonLogs: recentDaemonLogs,
	})
}

func (e *Engine) CheckUpdates(ctx context.Context) (features.UpdateCheckResult, error) {
	ch := e.store.Settings().UpdateChannel
	apiURL := os.Getenv("CLASHFX_UPDATE_API_URL")
	curVer := "v" + strings.TrimLeft(strings.TrimSpace(AppVersion), "vV")
	return features.CheckForUpdatesWithProxy(ctx, curVer, ch, apiURL, e.activeLocalProxyPort())
}

func fetchCoreVersionFromCore(ctx context.Context, apiPort int, secret string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/version", apiPort), nil)
	if err != nil {
		return ""
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var payload struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.Version)
}

func coreExecutableName() string {
	if runtime.GOOS == "windows" {
		return "mihomo_core.exe"
	}
	return "mihomo_core"
}

func allowHostPrivilegedCoreLookup() bool {
	if strings.TrimSpace(os.Getenv("CLASHFX_DISABLE_PRIVILEGED_SERVICE")) == "1" {
		return false
	}
	return flag.Lookup("test.v") == nil || strings.TrimSpace(os.Getenv("CLASHFX_TEST_REAL_SERVICE")) == "1"
}

type coreBinaryCandidate struct {
	dir     string
	path    string
	ver     string
	modTime time.Time
}

func isPreferredCoreCandidate(c, best coreBinaryCandidate, isLab bool) bool {
	if isLab {
		cAlpha := strings.HasPrefix(c.ver, "alpha-")
		bAlpha := strings.HasPrefix(best.ver, "alpha-")
		if cAlpha != bAlpha {
			return cAlpha
		}
		if cAlpha && bAlpha {
			return !c.modTime.Before(best.modTime)
		}
	}
	cmp := service.CompareCoreVersions(c.ver, best.ver)
	if cmp != 0 {
		return cmp > 0
	}
	return runtime.GOOS == "darwin" && c.dir == service.DarwinPrivilegedBinDir
}

func (e *Engine) syncCoreBinarySelection() {
	exeName := coreExecutableName()
	var dirs []string
	seen := make(map[string]struct{})
	addDir := func(d string) {
		trimmed := strings.TrimSpace(d)
		if trimmed == "" {
			return
		}
		cleaned := filepath.Clean(trimmed)
		if _, ok := seen[cleaned]; ok {
			return
		}
		seen[cleaned] = struct{}{}
		dirs = append(dirs, cleaned)
	}
	addDir(e.cfg.BinDir)
	addDir(filepath.Join(e.store.AppDataDir(), "bin"))
	if runtime.GOOS == "darwin" && allowHostPrivilegedCoreLookup() {
		addDir(service.DarwinPrivilegedBinDir)
	}

	var candidates []coreBinaryCandidate
	for _, dir := range dirs {
		candPath := filepath.Join(dir, exeName)
		info, err := os.Stat(candPath)
		if err != nil || info.IsDir() {
			continue
		}
		ver := service.ProbeCoreVersion(candPath)
		if ver == "" {
			continue
		}
		candidates = append(candidates, coreBinaryCandidate{
			dir:     dir,
			path:    candPath,
			ver:     ver,
			modTime: info.ModTime(),
		})
	}
	if len(candidates) == 0 {
		return
	}

	isLab := strings.EqualFold(strings.TrimSpace(e.store.Settings().UpdateChannel), "lab")
	best := candidates[0]
	for _, c := range candidates[1:] {
		if isPreferredCoreCandidate(c, best, isLab) {
			best = c
		}
	}

	e.statusMu.Lock()
	e.coreHelper.CoreBinaryPath = best.path
	if best.ver != "" {
		e.coreVersion = best.ver
	}
	helper := e.coreHelper
	e.statusMu.Unlock()

	if e.localRunner != nil {
		e.localRunner.SetTrustedDir(best.dir)
	}
	if e.subMgr != nil {
		e.subMgr.SetCoreHelper(helper)
	}
}

func isDirWritable(dir string) bool {
	if strings.TrimSpace(dir) == "" {
		return false
	}
	f, err := os.CreateTemp(dir, ".cfx-write-check-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

func copyExecutableFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	return out.Close()
}

func (e *Engine) prepareWritableLocalCoreDir() error {
	curDir := ""
	if e.localRunner != nil {
		curDir = e.localRunner.TrustedDir()
	}
	if isDirWritable(curDir) {
		return nil
	}

	fallbackDir := filepath.Join(e.store.AppDataDir(), "bin")
	if err := os.MkdirAll(fallbackDir, 0755); err != nil {
		return fmt.Errorf("create local core directory: %w", err)
	}
	exeName := coreExecutableName()
	dstPath := filepath.Join(fallbackDir, exeName)
	if _, err := os.Stat(dstPath); os.IsNotExist(err) {
		var seedDirs []string
		if curDir != "" {
			seedDirs = append(seedDirs, curDir)
		}
		if strings.TrimSpace(e.cfg.BinDir) != "" {
			seedDirs = append(seedDirs, filepath.Clean(e.cfg.BinDir))
		}
		if runtime.GOOS == "darwin" && allowHostPrivilegedCoreLookup() {
			seedDirs = append(seedDirs, service.DarwinPrivilegedBinDir)
		}
		for _, sd := range seedDirs {
			srcPath := filepath.Join(sd, exeName)
			if info, statErr := os.Stat(srcPath); statErr == nil && !info.IsDir() {
				_ = copyExecutableFile(srcPath, dstPath)
				break
			}
		}
	}

	e.statusMu.Lock()
	e.coreHelper.CoreBinaryPath = dstPath
	helper := e.coreHelper
	e.statusMu.Unlock()

	if e.localRunner != nil {
		e.localRunner.SetTrustedDir(fallbackDir)
	}
	if e.subMgr != nil {
		e.subMgr.SetCoreHelper(helper)
	}
	return nil
}

// RestartCore cleanly restarts the active Mihomo core process (either ClashFXService
// or localRunner) and re-applies the active runtime configuration.
func (e *Engine) RestartCore(ctx context.Context, allowUAC bool) error {
	e.opMu.Lock()
	if e.isCoreViaService() || (supportsPrivilegedService() && IsPrivilegedServiceInstalled(service.ServiceName)) {
		_, _ = callService(8*time.Second, service.Request{Action: "stop_core"})
	}
	_ = e.localRunner.Stop()
	e.ctrl.ResetActiveState()
	e.opMu.Unlock()

	_, err := e.applyAndSync(ctx, true, allowUAC)
	return err
}

// UpgradeCore checks upstream MetaCubeX/mihomo releases on the active channel and
// atomically upgrades the active core binary via ClashFXService (when installed) or
// a writable local directory, rolling back automatically on startup failure.
func (e *Engine) UpgradeCore(ctx context.Context) (service.CoreUpgradeInfo, error) {
	e.upgradeMu.Lock()
	defer e.upgradeMu.Unlock()

	ch := e.store.Settings().UpdateChannel
	proxyPort := e.activeLocalProxyPort()
	useSvc := supportsPrivilegedService() && (e.isCoreViaService() || IsPrivilegedServiceInstalled(service.ServiceName))

	if useSvc {
		wasLocalRunning := !e.isCoreViaService() && e.localRunner.Status().Running
		stoppedLocalForSwap := false
		if wasLocalRunning && runtime.GOOS == "windows" {
			// On Windows, running local mihomo_core.exe in Program Files locks against rename.
			e.opMu.Lock()
			_ = e.localRunner.Stop()
			e.ctrl.ResetActiveState()
			e.opMu.Unlock()
			stoppedLocalForSwap = true
		}

		callCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
		resp, err := service.Call(callCtx, service.Request{
			Action:    "upgrade_core",
			Channel:   ch,
			ProxyPort: proxyPort,
		})
		cancel()

		var info service.CoreUpgradeInfo
		if resp.Upgrade != nil {
			info = *resp.Upgrade
		}
		if err != nil {
			if stoppedLocalForSwap {
				_, _ = e.applyAndSync(ctx, true, false)
			}
			return info, err
		}
		if !resp.OK {
			if stoppedLocalForSwap {
				_, _ = e.applyAndSync(ctx, true, false)
			}
			return info, errors.New(resp.Message)
		}

		e.syncCoreBinarySelection()
		if info.CurrentVersion != "" {
			e.statusMu.Lock()
			e.coreVersion = info.CurrentVersion
			e.statusMu.Unlock()
		}

		if info.Updated {
			if e.isCoreViaService() {
				_, _ = e.applyAndSync(ctx, true, false)
			} else if wasLocalRunning {
				e.opMu.Lock()
				_ = e.localRunner.Stop()
				e.ctrl.ResetActiveState()
				e.opMu.Unlock()
				_, _ = e.applyAndSync(ctx, true, false)
			} else {
				e.notifyStateChanged()
			}
		} else if stoppedLocalForSwap {
			_, _ = e.applyAndSync(ctx, true, false)
		} else {
			e.notifyStateChanged()
		}
		return info, nil
	}

	if err := e.prepareWritableLocalCoreDir(); err != nil {
		return service.CoreUpgradeInfo{}, err
	}
	info, _, err := e.localRunner.UpgradeCore(ctx, ch, proxyPort)
	if err != nil {
		return info, err
	}
	e.syncCoreBinarySelection()
	if info.CurrentVersion != "" {
		e.statusMu.Lock()
		e.coreVersion = info.CurrentVersion
		e.statusMu.Unlock()
	}
	if info.Updated && e.IsCoreRunning() {
		_, _ = e.applyAndSync(ctx, true, false)
	} else {
		e.notifyStateChanged()
	}
	return info, nil
}

func (e *Engine) notifyStateChanged() {
	if e.onPush != nil {
		e.onPush()
	}
}
