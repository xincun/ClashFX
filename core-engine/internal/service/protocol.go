package service

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/clashfx/core-engine/internal/applog"
	"gopkg.in/yaml.v3"
)

const (
	ServiceName            = "ClashFXService"
	ServicePipeName        = `\\.\pipe\ClashFXService`
	DarwinPrivilegedBinDir = "/Library/Application Support/ClashFX/bin"

	CoreHealthStopped    = "stopped"
	CoreHealthStarting   = "starting"
	CoreHealthHealthy    = "healthy"
	CoreHealthRestarting = "restarting"
	CoreHealthCrashLoop  = "crash_loop"
)

type Request struct {
	Action     string         `json:"action"` // "start_core", "stop_core", "status", "upgrade_core", "sync_ai_guard", "disarm_ai_guard", "power_resume"
	HomeDir    string         `json:"home_dir,omitempty"`
	ConfigFile string         `json:"config_file,omitempty"`
	ExtUI      string         `json:"ext_ui,omitempty"`
	LogDir     string         `json:"log_dir,omitempty"`
	Supervise  *bool          `json:"supervise,omitempty"`
	Channel    string         `json:"channel,omitempty"`
	ProxyPort  int            `json:"proxy_port,omitempty"`
	AIGuard    *AIGuardConfig `json:"ai_guard,omitempty"`
}

type Status struct {
	Running            bool           `json:"running"`
	PID                int            `json:"pid,omitempty"`
	Health             string         `json:"health,omitempty"`
	ConfigFile         string         `json:"config_file,omitempty"`
	HomeDir            string         `json:"home_dir,omitempty"`
	LogDir             string         `json:"log_dir,omitempty"`
	CoreLogFile        string         `json:"core_log_file,omitempty"`
	ServiceLogFile     string         `json:"service_log_file,omitempty"`
	RestartCount       int            `json:"restart_count,omitempty"`
	ConsecutiveCrashes int            `json:"consecutive_crashes,omitempty"`
	LastExitCode       int            `json:"last_exit_code,omitempty"`
	LastCrashAt        string         `json:"last_crash_at,omitempty"`
	LastCrashReason    string         `json:"last_crash_reason,omitempty"`
	TunHealthy         bool           `json:"tun_healthy,omitempty"`
	TunLUID            uint64         `json:"tun_luid,omitempty"`
	AIGuard            *AIGuardStatus `json:"ai_guard,omitempty"`
	RecentLogs         []string       `json:"recent_logs,omitempty"`
}

type CoreUpgradeInfo struct {
	Updated         bool   `json:"updated"`
	AlreadyLatest   bool   `json:"already_latest,omitempty"`
	PreviousVersion string `json:"previous_version,omitempty"`
	CurrentVersion  string `json:"current_version,omitempty"`
	LatestVersion   string `json:"latest_version,omitempty"`
}

type Response struct {
	OK      bool             `json:"ok"`
	Message string           `json:"message,omitempty"`
	Status  *Status          `json:"status,omitempty"`
	Upgrade *CoreUpgradeInfo `json:"upgrade,omitempty"`
}

// Runner supervises a single stock upstream mihomo_core binary located strictly inside
// trustedDir (the service executable's own directory). It provides:
//   - complete rotating log persistence for mihomo_core and service lifecycle events
//   - automatic crash supervision with exponential backoff and circuit breaker
//   - OS-level AI Proxy Lock fail-closed guard (WFP on Windows / pf on macOS)
//   - zero non-standard CLI flags so any official Mihomo release runs natively.
type Runner struct {
	mu           sync.Mutex
	upgradeMu    sync.Mutex
	trustedDir   string
	coreExeName  string
	readyTimeout time.Duration
	stopTimeout  time.Duration

	backoffSchedule    []time.Duration
	crashLoopDelay     time.Duration
	stableResetAfter   time.Duration
	crashLoopThreshold int

	testReleaseAPIURL   string
	testDownloadBaseURL string

	cmd            *exec.Cmd
	guard          *childGuard
	doneCh         chan error
	pipesDone      chan struct{}
	supStopCh      chan struct{}
	gen            uint64
	desiredRunning bool
	supervise      bool
	startedAt      time.Time

	homeDir    string
	configFile string
	extUI      string
	logDir     string
	tunDevice  string
	tunEnabled bool

	health             string
	restartCount       int
	consecutiveCrashes int
	lastExitCode       int
	lastCrashAt        time.Time
	lastCrashReason    string

	serviceDataDir string
	persistGuard   bool
	serviceLog     *applog.RotatingFile
	coreLog        *applog.RotatingFile
	mirrorSvcLog   *applog.RotatingFile

	aiGuard    aiGuardController
	aiGuardCfg AIGuardConfig

	logMu      sync.Mutex
	recentLogs []string
}

func NewRunner(trustedDir string) *Runner {
	exeName := "mihomo_core"
	if runtime.GOOS == "windows" {
		exeName = "mihomo_core.exe"
	}
	return &Runner{
		trustedDir:         trustedDir,
		coreExeName:        exeName,
		readyTimeout:       12 * time.Second,
		stopTimeout:        6 * time.Second,
		backoffSchedule:    []time.Duration{1 * time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second},
		crashLoopDelay:     60 * time.Second,
		stableResetAfter:   60 * time.Second,
		crashLoopThreshold: 5,
		health:             CoreHealthStopped,
		tunDevice:          "ClashFX",
		aiGuard:            newPlatformAIGuard(),
	}
}

// InitPrivilegedService initializes persistent machine-scoped service logging and
// restores any persisted AI Proxy Lock fail-closed guard (so boot-time protection
// is active before desktop login).
func (r *Runner) InitPrivilegedService(serviceDataDir string) {
	dir := strings.TrimSpace(serviceDataDir)
	if dir == "" {
		dir = DefaultServiceDataDir(r.TrustedDir())
	}

	r.mu.Lock()
	r.serviceDataDir = dir
	r.persistGuard = true
	svcLogPath := filepath.Join(dir, "logs", "clashfx-service.log")
	if rf, err := applog.NewRotatingFile(svcLogPath, applog.DefaultMaxBytes, applog.DefaultMaxBackups); err == nil {
		if r.serviceLog != nil {
			_ = r.serviceLog.Close()
		}
		r.serviceLog = rf
	}
	r.logServiceEventLocked("INFO", "ClashFXService initialized (trusted_dir=%s, data_dir=%s)", r.trustedDir, dir)

	if cfg, ok := loadPersistedAIGuardConfig(dir); ok && cfg.Enabled {
		r.aiGuardCfg = cfg
		st := r.syncAIGuardLocked(false)
		r.logServiceEventLocked("INFO", "restored persisted AI Guard on service startup (mode=%s, armed=%v, protected=%d, err=%q)",
			st.Mode, st.Armed, st.ProtectedCount, st.LastError)
	}
	r.mu.Unlock()
}

// ShutdownService cleanly terminates the core and releases live OS firewall handles
// while preserving persisted AI Guard policy for next OS boot.
func (r *Runner) ShutdownService() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.logServiceEventLocked("INFO", "ClashFXService shutting down")
	r.desiredRunning = false
	r.health = CoreHealthStopped
	err := r.stopLocked()
	if r.aiGuard != nil {
		_ = r.aiGuard.Disarm()
	}
	r.closeLogFilesLocked(true)
	return err
}

func (r *Runner) TrustedDir() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.trustedDir
}

func (r *Runner) SetTrustedDir(dir string) {
	cleaned := strings.TrimSpace(dir)
	if cleaned == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.trustedDir = filepath.Clean(cleaned)
}

func (r *Runner) SetCoreBinaryForTest(exeName string, readyTimeout, stopTimeout time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.coreExeName = exeName
	if readyTimeout > 0 {
		r.readyTimeout = readyTimeout
	}
	if stopTimeout > 0 {
		r.stopTimeout = stopTimeout
	}
}

func (r *Runner) SetSupervisionTimingForTest(backoff []time.Duration, crashLoopDelay, stableReset time.Duration, crashLoopThreshold int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(backoff) > 0 {
		r.backoffSchedule = append([]time.Duration(nil), backoff...)
	}
	if crashLoopDelay > 0 {
		r.crashLoopDelay = crashLoopDelay
	}
	if stableReset > 0 {
		r.stableResetAfter = stableReset
	}
	if crashLoopThreshold > 0 {
		r.crashLoopThreshold = crashLoopThreshold
	}
}

func (r *Runner) SetUpgradeEndpointsForTest(releaseAPIURL, downloadBaseURL string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.testReleaseAPIURL = strings.TrimSpace(releaseAPIURL)
	r.testDownloadBaseURL = strings.TrimSpace(downloadBaseURL)
}

func (r *Runner) SetAIGuardForTest(ctrl aiGuardController, dataDir string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ctrl != nil {
		r.aiGuard = ctrl
	}
	if strings.TrimSpace(dataDir) != "" {
		r.serviceDataDir = dataDir
		r.persistGuard = true
	}
}

func (r *Runner) Handle(req Request) Response {
	switch req.Action {
	case "status":
		st := r.Status()
		return Response{OK: true, Status: &st}
	case "start_core":
		supervise := true
		if req.Supervise != nil {
			supervise = *req.Supervise
		}
		st, err := r.StartWithOptions(req.HomeDir, req.ConfigFile, req.ExtUI, req.LogDir, supervise)
		if err != nil {
			return Response{OK: false, Message: err.Error(), Status: &st}
		}
		return Response{OK: true, Status: &st}
	case "stop_core":
		if err := r.Stop(); err != nil {
			st := r.Status()
			return Response{OK: false, Message: err.Error(), Status: &st}
		}
		st := r.Status()
		return Response{OK: true, Status: &st}
	case "upgrade_core":
		info, st, err := r.UpgradeCore(context.Background(), req.Channel, req.ProxyPort)
		if err != nil {
			return Response{OK: false, Message: err.Error(), Status: &st, Upgrade: &info}
		}
		return Response{OK: true, Status: &st, Upgrade: &info}
	case "sync_ai_guard":
		if req.AIGuard == nil {
			return Response{OK: false, Message: "ai_guard config payload is required"}
		}
		st := r.SyncAIGuard(*req.AIGuard)
		return Response{OK: true, Status: &st}
	case "disarm_ai_guard":
		st := r.DisarmAIGuard()
		return Response{OK: true, Status: &st}
	case "power_resume":
		r.HandlePowerResume()
		st := r.Status()
		return Response{OK: true, Status: &st}
	default:
		return Response{OK: false, Message: fmt.Sprintf("unknown service action %q", req.Action)}
	}
}

func (r *Runner) SyncAIGuard(cfg AIGuardConfig) Status {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !cfg.Enabled {
		r.aiGuardCfg = AIGuardConfig{}
		if r.persistGuard {
			clearPersistedAIGuardConfig(r.serviceDataDir)
		}
		st := r.disarmAIGuardLocked()
		r.logServiceEventLocked("INFO", "AI Guard disarmed via sync_ai_guard (mode=%s)", st.Mode)
		return r.statusLocked()
	}

	if strings.TrimSpace(cfg.TunDeviceName) == "" {
		if r.tunDevice != "" {
			cfg.TunDeviceName = r.tunDevice
		} else {
			cfg.TunDeviceName = "ClashFX"
		}
	}
	r.aiGuardCfg = cfg
	if r.persistGuard {
		_ = savePersistedAIGuardConfig(r.serviceDataDir, cfg)
	}
	running, _ := r.isCoreAliveLocked()
	st := r.syncAIGuardLocked(running)
	r.logServiceEventLocked("INFO", "AI Guard synced (core_running=%v, mode=%s, armed=%v, protected=%d, tun_luid=%d, err=%q)",
		running, st.Mode, st.Armed, st.ProtectedCount, st.TunLUID, st.LastError)
	return r.statusLocked()
}

func (r *Runner) DisarmAIGuard() Status {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.aiGuardCfg = AIGuardConfig{}
	if r.persistGuard {
		clearPersistedAIGuardConfig(r.serviceDataDir)
	}
	st := r.disarmAIGuardLocked()
	r.logServiceEventLocked("INFO", "AI Guard disarmed (mode=%s)", st.Mode)
	return r.statusLocked()
}

func (r *Runner) syncAIGuardLocked(coreRunning bool) AIGuardStatus {
	if r.aiGuard == nil {
		return AIGuardStatus{Mode: AIGuardModeInactive}
	}
	if !r.aiGuardCfg.Enabled {
		return r.aiGuard.Disarm()
	}
	return r.aiGuard.Sync(r.aiGuardCfg, coreRunning)
}

func (r *Runner) disarmAIGuardLocked() AIGuardStatus {
	if r.aiGuard == nil {
		return AIGuardStatus{Mode: AIGuardModeInactive}
	}
	return r.aiGuard.Disarm()
}

// HandlePowerResume schedules two staggered health checks after OS sleep/standby wake
// so Wintun / physical NIC recovery settles cleanly and dead or wedged cores are revived.
func (r *Runner) HandlePowerResume() {
	r.mu.Lock()
	if !r.desiredRunning {
		r.mu.Unlock()
		return
	}
	gen := r.gen
	stopCh := r.supStopCh
	r.logServiceEventLocked("INFO", "OS power resume event received; scheduling core & TUN verification")
	r.mu.Unlock()

	go func() {
		for _, wait := range []time.Duration{2 * time.Second, 4 * time.Second} {
			select {
			case <-stopCh:
				return
			case <-time.After(wait):
			}
			r.verifyAfterPowerResume(gen)
		}
	}()
}

func (r *Runner) verifyAfterPowerResume(expectedGen uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.desiredRunning || r.gen != expectedGen {
		return
	}
	running, pid := r.isCoreAliveLocked()
	tunOK := true
	if r.tunEnabled && r.aiGuard != nil {
		_, tunOK = r.aiGuard.IsTunHealthy(r.tunDevice)
	}
	if running && tunOK {
		if r.aiGuardCfg.Enabled {
			_ = r.syncAIGuardLocked(true)
		}
		return
	}
	r.logServiceEventLocked("WARN", "post-resume health check failed (running=%v, pid=%d, tun_enabled=%v, tun_ok=%v); restarting mihomo_core",
		running, pid, r.tunEnabled, tunOK)
	if r.aiGuardCfg.Enabled {
		_ = r.syncAIGuardLocked(false)
	}
	if _, err := r.startLocked(r.homeDir, r.configFile, r.extUI, r.logDir, r.supervise, true); err != nil {
		r.logServiceEventLocked("ERROR", "post-resume restart failed: %v", err)
	}
}

func validateCleanAbsPath(label, p string, mustExist bool, wantDir bool) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", fmt.Errorf("%s is required", label)
	}
	if strings.ContainsAny(p, "\x00\r\n") {
		return "", fmt.Errorf("%s contains invalid control characters", label)
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("%s must be an absolute path: %q", label, p)
	}
	if strings.HasPrefix(p, `\\`) {
		return "", fmt.Errorf("%s must be a local filesystem path, not UNC: %q", label, p)
	}
	cleaned := filepath.Clean(p)
	if mustExist {
		info, err := os.Stat(cleaned)
		if err != nil {
			return "", fmt.Errorf("%s unavailable: %w", label, err)
		}
		if wantDir && !info.IsDir() {
			return "", fmt.Errorf("%s is not a directory: %q", label, cleaned)
		}
		if !wantDir && info.IsDir() {
			return "", fmt.Errorf("%s must be a file, got directory: %q", label, cleaned)
		}
	}
	return cleaned, nil
}

func validateSafeLogDir(rawLogDir, cleanHome string) (string, error) {
	trimmed := strings.TrimSpace(rawLogDir)
	if trimmed == "" {
		return filepath.Join(cleanHome, "logs"), nil
	}
	cleaned, err := validateCleanAbsPath("log_dir", trimmed, false, true)
	if err != nil {
		return "", err
	}
	// Reject obvious OS root/system directories even from authenticated callers.
	low := strings.ToLower(strings.ReplaceAll(cleaned, `\`, "/"))
	if low == "/" || strings.HasSuffix(low, ":/") ||
		strings.Contains(low, "/windows/system32") ||
		strings.HasPrefix(low, "/etc") ||
		strings.HasPrefix(low, "/usr") ||
		strings.HasPrefix(low, "/system") {
		return "", fmt.Errorf("log_dir %q points to a restricted system directory", cleaned)
	}
	return cleaned, nil
}

type configProbeEndpoints struct {
	controllerAddr string
	secret         string
	mixedPort      int
	tunEnabled     bool
	tunDevice      string
}

func extractConfigEndpoints(configPath string) configProbeEndpoints {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return configProbeEndpoints{}
	}
	var doc struct {
		ExternalController string `yaml:"external-controller"`
		Secret             string `yaml:"secret"`
		MixedPort          int    `yaml:"mixed-port"`
		Tun                struct {
			Enable bool   `yaml:"enable"`
			Device string `yaml:"device"`
		} `yaml:"tun"`
	}
	if yaml.Unmarshal(raw, &doc) != nil {
		return configProbeEndpoints{}
	}
	ctrl := strings.TrimSpace(doc.ExternalController)
	if ctrl != "" {
		host, portStr, splitErr := net.SplitHostPort(ctrl)
		if splitErr == nil {
			p, pErr := strconv.Atoi(portStr)
			if pErr == nil && p > 0 {
				if host == "" || host == "0.0.0.0" || host == "::" || host == "*" {
					host = "127.0.0.1"
				}
				ctrl = net.JoinHostPort(host, strconv.Itoa(p))
			} else {
				ctrl = ""
			}
		} else {
			ctrl = ""
		}
	}
	tunDev := strings.TrimSpace(doc.Tun.Device)
	if tunDev == "" {
		tunDev = "ClashFX"
	}
	return configProbeEndpoints{
		controllerAddr: ctrl,
		secret:         doc.Secret,
		mixedPort:      doc.MixedPort,
		tunEnabled:     doc.Tun.Enable,
		tunDevice:      tunDev,
	}
}

func isStockMihomoReadyLogLine(line string) bool {
	return strings.Contains(line, "RESTful API listening at:") ||
		strings.Contains(line, "Mixed(http+socks) proxy listening at:") ||
		strings.Contains(line, "HTTP proxy listening at:") ||
		strings.Contains(line, "SOCKS proxy listening at:") ||
		strings.Contains(line, "Start initial compatible provider") ||
		strings.Contains(line, "mihomo-bin started, config:")
}

func probeHTTPReadiness(ctx context.Context, ep configProbeEndpoints, readyOnce *sync.Once, readyCh chan struct{}) {
	if ep.controllerAddr == "" {
		return
	}
	client := &http.Client{Timeout: 350 * time.Millisecond}
	url := "http://" + ep.controllerAddr + "/version"
	ticker := time.NewTicker(60 * time.Millisecond)
	defer ticker.Stop()

	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err == nil {
			if ep.secret != "" {
				req.Header.Set("Authorization", "Bearer "+ep.secret)
			}
			resp, doErr := client.Do(req)
			if doErr == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					portReady := true
					if ep.mixedPort > 0 {
						conn, dialErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", ep.mixedPort), 200*time.Millisecond)
						if dialErr != nil {
							portReady = false
						} else {
							_ = conn.Close()
						}
					}
					if portReady {
						readyOnce.Do(func() { close(readyCh) })
						return
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func isPathWithinDir(baseDir, targetPath string) bool {
	if resolvedBase, err := filepath.EvalSymlinks(baseDir); err == nil {
		baseDir = resolvedBase
	}
	if resolvedTarget, err := filepath.EvalSymlinks(targetPath); err == nil {
		targetPath = resolvedTarget
	}
	rel, err := filepath.Rel(baseDir, targetPath)
	if err != nil {
		return false
	}
	if rel == "." {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// Start launches mihomo_core with default logging under <homeDir>/logs and auto-supervision enabled.
func (r *Runner) Start(homeDir, configFile, extUI string) (Status, error) {
	return r.StartWithOptions(homeDir, configFile, extUI, "", true)
}

// StartWithOptions launches mihomo_core with explicit log directory and crash-supervision toggle.
func (r *Runner) StartWithOptions(homeDir, configFile, extUI, logDir string, supervise bool) (Status, error) {
	cleanHome, err := validateCleanAbsPath("home_dir", homeDir, true, true)
	if err != nil {
		return r.Status(), err
	}
	cleanConfig, err := validateCleanAbsPath("config_file", configFile, true, false)
	if err != nil {
		return r.Status(), err
	}
	if !isPathWithinDir(cleanHome, cleanConfig) {
		return r.Status(), fmt.Errorf("config_file %q must reside within home_dir %q", cleanConfig, cleanHome)
	}
	var cleanUI string
	if strings.TrimSpace(extUI) != "" {
		cleanUI, err = validateCleanAbsPath("ext_ui", extUI, true, true)
		if err != nil {
			return r.Status(), err
		}
	}
	cleanLogDir, err := validateSafeLogDir(logDir, cleanHome)
	if err != nil {
		return r.Status(), err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.consecutiveCrashes = 0
	return r.startLocked(cleanHome, cleanConfig, cleanUI, cleanLogDir, supervise, false)
}

func (r *Runner) ensureCoreLogLocked(cleanLogDir string) {
	if strings.TrimSpace(cleanLogDir) == "" {
		return
	}
	coreLogPath := filepath.Join(cleanLogDir, "mihomo-core.log")
	if r.coreLog == nil || !strings.EqualFold(r.coreLog.Path(), coreLogPath) {
		if r.coreLog != nil {
			_ = r.coreLog.Close()
			r.coreLog = nil
		}
		if rf, err := applog.NewRotatingFile(coreLogPath, applog.DefaultMaxBytes, applog.DefaultMaxBackups); err == nil {
			r.coreLog = rf
		}
	}
	mirrorPath := filepath.Join(cleanLogDir, "clashfx-service-events.log")
	if r.mirrorSvcLog == nil || !strings.EqualFold(r.mirrorSvcLog.Path(), mirrorPath) {
		if r.mirrorSvcLog != nil {
			_ = r.mirrorSvcLog.Close()
			r.mirrorSvcLog = nil
		}
		if rf, err := applog.NewRotatingFile(mirrorPath, applog.DefaultMaxBytes, applog.DefaultMaxBackups); err == nil {
			r.mirrorSvcLog = rf
		}
	}
}

func (r *Runner) closeLogFilesLocked(includeServiceLog bool) {
	if r.coreLog != nil {
		_ = r.coreLog.Close()
		r.coreLog = nil
	}
	if r.mirrorSvcLog != nil {
		_ = r.mirrorSvcLog.Close()
		r.mirrorSvcLog = nil
	}
	if includeServiceLog && r.serviceLog != nil {
		_ = r.serviceLog.Close()
		r.serviceLog = nil
	}
}

func (r *Runner) logServiceEventLocked(level, format string, args ...any) {
	if r.serviceLog != nil {
		r.serviceLog.Logf("clashfx-service", level, format, args...)
	}
	if r.mirrorSvcLog != nil {
		r.mirrorSvcLog.Logf("clashfx-service", level, format, args...)
	}
}

func (r *Runner) startLocked(cleanHome, cleanConfig, cleanUI, cleanLogDir string, supervise bool, isAutoRestart bool) (Status, error) {
	// Arm/keep AI Guard in blocking mode across the stop->start gap so zero packets leak.
	if r.aiGuardCfg.Enabled {
		_ = r.syncAIGuardLocked(false)
	}

	if err := r.stopLocked(); err != nil {
		return r.statusLocked(), fmt.Errorf("stop previous core: %w", err)
	}

	r.ensureCoreLogLocked(cleanLogDir)

	corePath := filepath.Join(r.trustedDir, r.coreExeName)
	info, err := os.Stat(corePath)
	if err != nil || info.IsDir() {
		if !isAutoRestart {
			r.closeLogFilesLocked(false)
		}
		return r.statusLocked(), fmt.Errorf("trusted core binary missing at %s", corePath)
	}

	// Standard stock upstream Mihomo CLI arguments only (-d, -f, optional -ext-ui).
	args := []string{"-d", cleanHome, "-f", cleanConfig}
	if cleanUI != "" {
		args = append(args, "-ext-ui", cleanUI)
	}

	cmd := exec.Command(corePath, args...)
	cmd.Dir = r.trustedDir
	// Always confine stock Mihomo file access via SAFE_PATHS so SYSTEM-launched
	// mihomo_core cannot read/write arbitrary provider paths outside home_dir / trustedDir.
	env := make([]string, 0, len(os.Environ())+1)
	safeList := []string{cleanHome, r.trustedDir}
	if cleanUI != "" {
		safeList = append(safeList, cleanUI)
	}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(strings.ToUpper(kv), "SAFE_PATHS=") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "SAFE_PATHS="+strings.Join(safeList, string(os.PathListSeparator)))
	cmd.Env = env
	configureChildProcess(cmd)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		if !isAutoRestart {
			r.closeLogFilesLocked(false)
		}
		return r.statusLocked(), fmt.Errorf("create core stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		if !isAutoRestart {
			r.closeLogFilesLocked(false)
		}
		return r.statusLocked(), fmt.Errorf("create core stderr pipe: %w", err)
	}

	r.logMu.Lock()
	r.recentLogs = nil
	r.logMu.Unlock()

	endpoints := extractConfigEndpoints(cleanConfig)
	repairUserHomeOwnership(cleanHome)

	r.health = CoreHealthStarting
	if r.coreLog != nil {
		r.coreLog.Logf("supervisor", "INFO", "starting %s (auto_restart=%v, config=%s, tun=%v, device=%s)",
			r.coreExeName, isAutoRestart, cleanConfig, endpoints.tunEnabled, endpoints.tunDevice)
	}
	r.logServiceEventLocked("INFO", "starting %s (auto_restart=%v, config=%s, tun=%v)",
		r.coreExeName, isAutoRestart, cleanConfig, endpoints.tunEnabled)

	if err := cmd.Start(); err != nil {
		r.health = CoreHealthStopped
		r.logServiceEventLocked("ERROR", "start core process failed: %v", err)
		if !isAutoRestart {
			r.closeLogFilesLocked(false)
		}
		return r.statusLocked(), fmt.Errorf("start core process: %w", err)
	}
	guard := attachChildGuard(cmd)
	coreLogRef := r.coreLog

	readyCh := make(chan struct{})
	var readyOnce sync.Once
	doneCh := make(chan error, 1)
	pipesDone := make(chan struct{})

	streamPipe := func(reader io.Reader, tag string) {
		readLinesBounded(reader, 32*1024, func(line string) {
			r.appendLog(line)
			if coreLogRef != nil {
				coreLogRef.WriteStampedLine(tag, line)
			}
			if endpoints.controllerAddr == "" && isStockMihomoReadyLogLine(line) {
				readyOnce.Do(func() { close(readyCh) })
			}
		})
	}
	var pipeWG sync.WaitGroup
	pipeWG.Add(2)
	go func() {
		defer pipeWG.Done()
		streamPipe(stdoutPipe, "core-out")
	}()
	go func() {
		defer pipeWG.Done()
		streamPipe(stderrPipe, "core-err")
	}()
	go func() {
		pipeWG.Wait()
		close(pipesDone)
	}()
	go func() {
		doneCh <- cmd.Wait()
	}()

	probeCtx, cancelProbe := context.WithCancel(context.Background())
	defer cancelProbe()
	if endpoints.controllerAddr != "" {
		go probeHTTPReadiness(probeCtx, endpoints, &readyOnce, readyCh)
	}

	r.gen++
	gen := r.gen
	supStopCh := make(chan struct{})

	r.cmd = cmd
	r.guard = guard
	r.doneCh = doneCh
	r.pipesDone = pipesDone
	r.supStopCh = supStopCh
	r.homeDir = cleanHome
	r.configFile = cleanConfig
	r.extUI = cleanUI
	r.logDir = cleanLogDir
	r.tunEnabled = endpoints.tunEnabled
	r.tunDevice = endpoints.tunDevice
	r.supervise = supervise
	r.desiredRunning = supervise

	select {
	case <-readyCh:
		r.startedAt = time.Now()
		r.health = CoreHealthHealthy
		if isAutoRestart {
			r.restartCount++
		}
		repairUserHomeOwnership(cleanHome)
		pid := 0
		if cmd.Process != nil {
			pid = cmd.Process.Pid
		}
		if r.coreLog != nil {
			r.coreLog.Logf("supervisor", "INFO", "core ready (pid=%d, restart_count=%d)", pid, r.restartCount)
		}
		r.logServiceEventLocked("INFO", "core ready (pid=%d, auto_restart=%v, restart_count=%d)", pid, isAutoRestart, r.restartCount)
		if r.aiGuardCfg.Enabled {
			if r.aiGuardCfg.TunDeviceName == "" {
				r.aiGuardCfg.TunDeviceName = endpoints.tunDevice
			}
			st := r.syncAIGuardLocked(true)
			if st.Mode != AIGuardModeArmedNormal {
				go r.waitForTunAndSyncAIGuard(gen, supStopCh, endpoints.tunDevice)
			}
		}
		if supervise {
			go r.superviseCoreExit(gen, cmd, doneCh, supStopCh)
		}
		return r.statusLocked(), nil
	case waitErr := <-doneCh:
		guard.closeHandles()
		select {
		case <-pipesDone:
		case <-time.After(250 * time.Millisecond):
		}
		r.cmd = nil
		r.guard = nil
		r.doneCh = nil
		r.pipesDone = nil
		r.closeSupStopLocked()
		r.health = CoreHealthStopped
		if !isAutoRestart {
			r.desiredRunning = false
		}
		logs := strings.Join(r.snapshotLogs(), " | ")
		if r.coreLog != nil {
			r.coreLog.Logf("supervisor", "ERROR", "core exited before readiness (err=%v): %s", waitErr, logs)
		}
		r.logServiceEventLocked("ERROR", "core exited before readiness (err=%v)", waitErr)
		if !isAutoRestart {
			r.closeLogFilesLocked(false)
		}
		if waitErr != nil {
			return r.statusLocked(), fmt.Errorf("core exited during startup (%v): %s", waitErr, logs)
		}
		return r.statusLocked(), fmt.Errorf("core exited unexpectedly before readiness: %s", logs)
	case <-time.After(r.readyTimeout):
		_ = r.stopLocked()
		r.health = CoreHealthStopped
		if !isAutoRestart {
			r.desiredRunning = false
		}
		logs := strings.Join(r.snapshotLogs(), " | ")
		if r.coreLog != nil {
			r.coreLog.Logf("supervisor", "ERROR", "core timed out waiting for readiness (%s): %s", r.readyTimeout, logs)
		}
		r.logServiceEventLocked("ERROR", "core timed out waiting for readiness (%s)", r.readyTimeout)
		if !isAutoRestart {
			r.closeLogFilesLocked(false)
		}
		return r.statusLocked(), fmt.Errorf("core timed out waiting for readiness: %s", logs)
	}
}

// waitForTunAndSyncAIGuard polls for the Wintun adapter LUID for up to 5 seconds after
// mihomo_core becomes ready so WFP permits the newly created TUN interface immediately.
func (r *Runner) waitForTunAndSyncAIGuard(gen uint64, stopCh <-chan struct{}, tunAlias string) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.Now().Add(5 * time.Second)

	for time.Now().Before(deadline) {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
		}
		r.mu.Lock()
		if r.gen != gen || !r.aiGuardCfg.Enabled {
			r.mu.Unlock()
			return
		}
		running, _ := r.isCoreAliveLocked()
		if !running {
			r.mu.Unlock()
			return
		}
		st := r.syncAIGuardLocked(true)
		if st.Mode == AIGuardModeArmedNormal {
			r.logServiceEventLocked("INFO", "post-start AI Guard locked onto TUN %q (luid=%d, protected=%d)",
				tunAlias, st.TunLUID, st.ProtectedCount)
			r.mu.Unlock()
			return
		}
		r.mu.Unlock()
	}
}

// superviseCoreExit waits for an active supervised mihomo_core process to exit.
// If it exits unexpectedly (while desiredRunning is still true and generation matches),
// it logs complete diagnostics to disk, immediately tightens the OS AI Guard into
// blocking_leaks mode, and restarts mihomo_core with exponential backoff + circuit breaker.
func (r *Runner) superviseCoreExit(gen uint64, exitedCmd *exec.Cmd, doneCh <-chan error, stopCh <-chan struct{}) {
	var waitErr error
	select {
	case waitErr = <-doneCh:
	case <-stopCh:
		return
	}

	r.mu.Lock()
	if r.gen != gen || !r.desiredRunning {
		r.mu.Unlock()
		return
	}

	pid := 0
	if exitedCmd != nil && exitedCmd.Process != nil {
		pid = exitedCmd.Process.Pid
	}
	exitCode := extractExitCode(waitErr)
	uptime := time.Since(r.startedAt)

	if r.guard != nil {
		r.guard.closeHandles()
	}
	r.cmd = nil
	r.guard = nil
	r.doneCh = nil

	if uptime >= r.stableResetAfter {
		r.consecutiveCrashes = 1
	} else {
		r.consecutiveCrashes++
	}
	r.lastExitCode = exitCode
	r.lastCrashAt = time.Now()
	tail := r.snapshotLogs()
	if len(tail) > 6 {
		tail = tail[len(tail)-6:]
	}
	reason := fmt.Sprintf("pid=%d exit_code=%d uptime=%s", pid, exitCode, uptime.Round(time.Millisecond))
	if waitErr != nil {
		reason += " err=" + waitErr.Error()
	}
	if len(tail) > 0 {
		reason += " | " + strings.Join(tail, " | ")
	}
	r.lastCrashReason = reason

	if r.consecutiveCrashes >= r.crashLoopThreshold {
		r.health = CoreHealthCrashLoop
	} else {
		r.health = CoreHealthRestarting
	}

	if r.coreLog != nil {
		r.coreLog.Logf("supervisor", "ERROR", "mihomo_core exited unexpectedly (crash#%d, %s)", r.consecutiveCrashes, reason)
	}
	r.logServiceEventLocked("ERROR", "mihomo_core exited unexpectedly (crash#%d, health=%s, pid=%d, exit=%d, uptime=%s)",
		r.consecutiveCrashes, r.health, pid, exitCode, uptime.Round(time.Millisecond))

	// Immediately tighten OS-level AI Guard (remove stale tunLUID on Windows / arm pf on macOS).
	if r.aiGuardCfg.Enabled {
		st := r.syncAIGuardLocked(false)
		r.logServiceEventLocked("WARN", "AI Guard tightened after crash (mode=%s, armed=%v, protected=%d, err=%q)",
			st.Mode, st.Armed, st.ProtectedCount, st.LastError)
	}

	// Retry loop owned by this supervisor until either restart succeeds or generation is cancelled.
	for {
		delay := r.backoffDelayLocked(r.consecutiveCrashes)
		r.logServiceEventLocked("INFO", "scheduling automatic mihomo_core restart in %s (attempt crash#%d, health=%s)",
			delay, r.consecutiveCrashes, r.health)
		r.mu.Unlock()

		select {
		case <-stopCh:
			return
		case <-time.After(delay):
		}

		r.mu.Lock()
		if r.gen != gen || !r.desiredRunning {
			r.mu.Unlock()
			return
		}

		_, startErr := r.startLocked(r.homeDir, r.configFile, r.extUI, r.logDir, r.supervise, true)
		if startErr == nil {
			// startLocked has spawned the new generation's watcher and updated state.
			r.mu.Unlock()
			return
		}

		// Restart failed — increment consecutive crash counter, update generation/stopCh so
		// stop_core can still interrupt us cleanly, and retry after next backoff.
		r.consecutiveCrashes++
		r.lastCrashAt = time.Now()
		r.lastCrashReason = fmt.Sprintf("auto-restart failed: %v", startErr)
		if r.consecutiveCrashes >= r.crashLoopThreshold {
			r.health = CoreHealthCrashLoop
		} else {
			r.health = CoreHealthRestarting
		}
		r.gen++
		gen = r.gen
		nextStop := make(chan struct{})
		r.supStopCh = nextStop
		stopCh = nextStop
		r.desiredRunning = true
		r.logServiceEventLocked("ERROR", "automatic mihomo_core restart failed (crash#%d, health=%s): %v",
			r.consecutiveCrashes, r.health, startErr)
	}
}

func (r *Runner) backoffDelayLocked(consecutiveCrashes int) time.Duration {
	if consecutiveCrashes >= r.crashLoopThreshold {
		if r.crashLoopDelay > 0 {
			return r.crashLoopDelay
		}
		return 60 * time.Second
	}
	if len(r.backoffSchedule) == 0 {
		return 2 * time.Second
	}
	idx := consecutiveCrashes - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(r.backoffSchedule) {
		idx = len(r.backoffSchedule) - 1
	}
	return r.backoffSchedule[idx]
}

func extractExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// readLinesBounded drains reader completely using bufio.Reader so arbitrarily long lines
// (>256 KiB) never stall the child process's stdout/stderr pipe, truncating only the
// in-memory/log line representation above maxLineBytes.
func readLinesBounded(reader io.Reader, maxLineBytes int, onLine func(string)) {
	br := bufio.NewReaderSize(reader, 32*1024)
	if maxLineBytes <= 0 {
		maxLineBytes = 32 * 1024
	}
	var buf []byte
	truncated := false
	for {
		chunk, isPrefix, err := br.ReadLine()
		if len(chunk) > 0 {
			if len(buf) < maxLineBytes {
				remain := maxLineBytes - len(buf)
				if len(chunk) > remain {
					buf = append(buf, chunk[:remain]...)
					truncated = true
				} else {
					buf = append(buf, chunk...)
				}
			} else {
				truncated = true
			}
		}
		if !isPrefix && (len(buf) > 0 || err == nil) {
			if len(buf) > 0 {
				line := string(buf)
				if truncated {
					line += " ...[truncated]"
				}
				onLine(line)
			}
			buf = buf[:0]
			truncated = false
		}
		if err != nil {
			if len(buf) > 0 {
				line := string(buf)
				if truncated {
					line += " ...[truncated]"
				}
				onLine(line)
			}
			return
		}
	}
}

func (r *Runner) closeSupStopLocked() {
	if r.supStopCh != nil {
		close(r.supStopCh)
		r.supStopCh = nil
	}
}

func (r *Runner) Stop() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.desiredRunning = false
	r.health = CoreHealthStopped
	r.logServiceEventLocked("INFO", "explicit stop_core requested")
	err := r.stopLocked()
	if r.aiGuardCfg.Enabled {
		_ = r.syncAIGuardLocked(false)
	}
	r.closeLogFilesLocked(false)
	return err
}

func (r *Runner) stopLocked() error {
	r.gen++
	r.closeSupStopLocked()

	if r.cmd == nil && r.guard == nil {
		return nil
	}
	pipesDone := r.pipesDone
	running, _ := r.guard.activePID(r.cmd, r.doneCh)
	if !running {
		if r.guard != nil {
			r.guard.closeHandles()
		}
		r.cmd = nil
		r.guard = nil
		r.doneCh = nil
		r.pipesDone = nil
		if pipesDone != nil {
			select {
			case <-pipesDone:
			case <-time.After(250 * time.Millisecond):
			}
		}
		if r.homeDir != "" {
			repairUserHomeOwnership(r.homeDir)
		}
		return nil
	}

	stopErr := r.guard.terminate(r.cmd, r.doneCh, r.stopTimeout)
	r.cmd = nil
	r.guard = nil
	r.doneCh = nil
	r.pipesDone = nil
	if pipesDone != nil {
		select {
		case <-pipesDone:
		case <-time.After(250 * time.Millisecond):
		}
	}
	if r.homeDir != "" {
		repairUserHomeOwnership(r.homeDir)
	}
	return stopErr
}

func (r *Runner) isCoreAliveLocked() (bool, int) {
	if r.guard == nil && r.cmd == nil {
		return false, 0
	}
	running, pid := r.guard.activePID(r.cmd, r.doneCh)
	if !running {
		if r.guard != nil {
			r.guard.closeHandles()
		}
		r.cmd = nil
		r.guard = nil
		r.doneCh = nil
		r.pipesDone = nil
	}
	return running, pid
}

func (r *Runner) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.statusLocked()
}

func (r *Runner) statusLocked() Status {
	running, pid := r.isCoreAliveLocked()
	st := Status{
		Health:             r.health,
		LogDir:             r.logDir,
		RestartCount:       r.restartCount,
		ConsecutiveCrashes: r.consecutiveCrashes,
		LastExitCode:       r.lastExitCode,
		LastCrashReason:    r.lastCrashReason,
		RecentLogs:         r.snapshotLogs(),
	}
	if !r.lastCrashAt.IsZero() {
		st.LastCrashAt = r.lastCrashAt.Format(time.RFC3339)
	}
	if r.coreLog != nil {
		st.CoreLogFile = r.coreLog.Path()
	} else if r.logDir != "" {
		st.CoreLogFile = filepath.Join(r.logDir, "mihomo-core.log")
	}
	if r.serviceLog != nil {
		st.ServiceLogFile = r.serviceLog.Path()
	} else if r.mirrorSvcLog != nil {
		st.ServiceLogFile = r.mirrorSvcLog.Path()
	} else if r.logDir != "" {
		st.ServiceLogFile = filepath.Join(r.logDir, "clashfx-service-events.log")
	}
	if r.aiGuard != nil {
		gs := r.aiGuard.Status()
		st.AIGuard = &gs
		if running && r.tunEnabled {
			luid, ok := r.aiGuard.IsTunHealthy(r.tunDevice)
			st.TunHealthy = ok
			st.TunLUID = luid
		}
	}
	if running {
		st.Running = true
		st.PID = pid
		st.HomeDir = r.homeDir
		st.ConfigFile = r.configFile
		if st.Health == "" || st.Health == CoreHealthStopped {
			st.Health = CoreHealthHealthy
		}
	} else if st.Health == "" {
		st.Health = CoreHealthStopped
	}
	return st
}

func (r *Runner) appendLog(line string) {
	r.logMu.Lock()
	defer r.logMu.Unlock()
	const maxLines = 80
	r.recentLogs = append(r.recentLogs, line)
	if len(r.recentLogs) > maxLines {
		r.recentLogs = append([]string(nil), r.recentLogs[len(r.recentLogs)-maxLines:]...)
	}
}

func (r *Runner) snapshotLogs() []string {
	r.logMu.Lock()
	defer r.logMu.Unlock()
	out := make([]string, len(r.recentLogs))
	copy(out, r.recentLogs)
	return out
}
