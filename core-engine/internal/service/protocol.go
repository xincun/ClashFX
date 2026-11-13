package service

import (
	"bufio"
	"context"
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

	"gopkg.in/yaml.v3"
)

const (
	ServiceName     = "ClashFXService"
	ServicePipeName = `\\.\pipe\ClashFXService`
)

type Request struct {
	Action     string `json:"action"` // "start_core", "stop_core", "status"
	HomeDir    string `json:"home_dir,omitempty"`
	ConfigFile string `json:"config_file,omitempty"`
	ExtUI      string `json:"ext_ui,omitempty"`
}

type Status struct {
	Running    bool     `json:"running"`
	PID        int      `json:"pid,omitempty"`
	ConfigFile string   `json:"config_file,omitempty"`
	HomeDir    string   `json:"home_dir,omitempty"`
	RecentLogs []string `json:"recent_logs,omitempty"`
}

type Response struct {
	OK      bool    `json:"ok"`
	Message string  `json:"message,omitempty"`
	Status  *Status `json:"status,omitempty"`
}

// Runner supervises a single stock upstream mihomo_core binary located strictly inside
// trustedDir (the service executable's own directory). Callers can never override the
// executable path over IPC, and zero non-standard CLI flags are passed so any official
// Mihomo release (or in-place online core upgrade) runs natively.
type Runner struct {
	mu           sync.Mutex
	trustedDir   string
	coreExeName  string
	readyTimeout time.Duration
	stopTimeout  time.Duration

	cmd        *exec.Cmd
	guard      *childGuard
	doneCh     chan error
	homeDir    string
	configFile string

	logMu      sync.Mutex
	recentLogs []string
}

func NewRunner(trustedDir string) *Runner {
	exeName := "mihomo_core"
	if runtime.GOOS == "windows" {
		exeName = "mihomo_core.exe"
	}
	return &Runner{
		trustedDir:   trustedDir,
		coreExeName:  exeName,
		readyTimeout: 12 * time.Second,
		stopTimeout:  6 * time.Second,
	}
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

func (r *Runner) Handle(req Request) Response {
	switch req.Action {
	case "status":
		st := r.Status()
		return Response{OK: true, Status: &st}
	case "start_core":
		st, err := r.Start(req.HomeDir, req.ConfigFile, req.ExtUI)
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
	default:
		return Response{OK: false, Message: fmt.Sprintf("unknown service action %q", req.Action)}
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

type configProbeEndpoints struct {
	controllerAddr string
	secret         string
	mixedPort      int
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
	return configProbeEndpoints{
		controllerAddr: ctrl,
		secret:         doc.Secret,
		mixedPort:      doc.MixedPort,
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

func (r *Runner) Start(homeDir, configFile, extUI string) (Status, error) {
	cleanHome, err := validateCleanAbsPath("home_dir", homeDir, true, true)
	if err != nil {
		return r.Status(), err
	}
	cleanConfig, err := validateCleanAbsPath("config_file", configFile, true, false)
	if err != nil {
		return r.Status(), err
	}
	var cleanUI string
	if strings.TrimSpace(extUI) != "" {
		cleanUI, err = validateCleanAbsPath("ext_ui", extUI, true, true)
		if err != nil {
			return r.Status(), err
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if err := r.stopLocked(); err != nil {
		return r.statusLocked(), fmt.Errorf("stop previous core: %w", err)
	}

	corePath := filepath.Join(r.trustedDir, r.coreExeName)
	info, err := os.Stat(corePath)
	if err != nil || info.IsDir() {
		return r.statusLocked(), fmt.Errorf("trusted core binary missing at %s", corePath)
	}

	// Standard stock upstream Mihomo CLI arguments only (-d, -f, optional -ext-ui).
	args := []string{"-d", cleanHome, "-f", cleanConfig}
	if cleanUI != "" {
		args = append(args, "-ext-ui", cleanUI)
	}

	cmd := exec.Command(corePath, args...)
	cmd.Dir = r.trustedDir
	if cleanUI != "" {
		env := make([]string, 0, len(os.Environ())+1)
		safeList := []string{cleanUI}
		if existing := strings.TrimSpace(os.Getenv("SAFE_PATHS")); existing != "" {
			safeList = append([]string{existing}, safeList...)
		}
		for _, kv := range os.Environ() {
			if strings.HasPrefix(strings.ToUpper(kv), "SAFE_PATHS=") {
				continue
			}
			env = append(env, kv)
		}
		env = append(env, "SAFE_PATHS="+strings.Join(safeList, string(os.PathListSeparator)))
		cmd.Env = env
	}
	configureChildProcess(cmd)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return r.statusLocked(), fmt.Errorf("create core stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return r.statusLocked(), fmt.Errorf("create core stderr pipe: %w", err)
	}

	r.logMu.Lock()
	r.recentLogs = nil
	r.logMu.Unlock()

	endpoints := extractConfigEndpoints(cleanConfig)

	if err := cmd.Start(); err != nil {
		return r.statusLocked(), fmt.Errorf("start core process: %w", err)
	}
	guard := attachChildGuard(cmd)

	readyCh := make(chan struct{})
	var readyOnce sync.Once
	doneCh := make(chan error, 1)

	streamLines := func(reader io.Reader) {
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 0, 16*1024), 256*1024)
		for scanner.Scan() {
			line := scanner.Text()
			r.appendLog(line)
			if endpoints.controllerAddr == "" && isStockMihomoReadyLogLine(line) {
				readyOnce.Do(func() { close(readyCh) })
			}
		}
	}
	go streamLines(stdoutPipe)
	go streamLines(stderrPipe)
	go func() {
		doneCh <- cmd.Wait()
	}()

	probeCtx, cancelProbe := context.WithCancel(context.Background())
	defer cancelProbe()
	if endpoints.controllerAddr != "" {
		go probeHTTPReadiness(probeCtx, endpoints, &readyOnce, readyCh)
	}

	r.cmd = cmd
	r.guard = guard
	r.doneCh = doneCh
	r.homeDir = cleanHome
	r.configFile = cleanConfig

	select {
	case <-readyCh:
		return r.statusLocked(), nil
	case waitErr := <-doneCh:
		guard.closeHandles()
		r.cmd = nil
		r.guard = nil
		r.doneCh = nil
		logs := strings.Join(r.snapshotLogs(), " | ")
		if waitErr != nil {
			return r.statusLocked(), fmt.Errorf("core exited during startup (%v): %s", waitErr, logs)
		}
		return r.statusLocked(), fmt.Errorf("core exited unexpectedly before readiness: %s", logs)
	case <-time.After(r.readyTimeout):
		_ = r.stopLocked()
		logs := strings.Join(r.snapshotLogs(), " | ")
		return r.statusLocked(), fmt.Errorf("core timed out waiting for readiness: %s", logs)
	}
}

func (r *Runner) Stop() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopLocked()
}

func (r *Runner) stopLocked() error {
	if r.cmd == nil && r.guard == nil {
		return nil
	}
	running, _ := r.guard.activePID(r.cmd, r.doneCh)
	if !running {
		if r.guard != nil {
			r.guard.closeHandles()
		}
		r.cmd = nil
		r.guard = nil
		r.doneCh = nil
		return nil
	}

	stopErr := r.guard.terminate(r.cmd, r.doneCh, r.stopTimeout)
	r.cmd = nil
	r.guard = nil
	r.doneCh = nil
	return stopErr
}

func (r *Runner) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.statusLocked()
}

func (r *Runner) statusLocked() Status {
	running, pid := false, 0
	if r.guard != nil || r.cmd != nil {
		running, pid = r.guard.activePID(r.cmd, r.doneCh)
		if !running {
			if r.guard != nil {
				r.guard.closeHandles()
			}
			r.cmd = nil
			r.guard = nil
			r.doneCh = nil
		}
	}
	st := Status{
		RecentLogs: r.snapshotLogs(),
	}
	if running {
		st.Running = true
		st.PID = pid
		st.HomeDir = r.homeDir
		st.ConfigFile = r.configFile
	}
	return st
}

func (r *Runner) appendLog(line string) {
	r.logMu.Lock()
	defer r.logMu.Unlock()
	const maxLines = 40
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
