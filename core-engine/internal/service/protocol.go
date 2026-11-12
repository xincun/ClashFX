package service

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	ServiceName     = "ClashFXService"
	ServicePipeName = `\\.\pipe\ClashFXService`
	ReadyMarker     = "mihomo-bin started, config:"
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

// Runner supervises a single child mihomo_core binary located strictly inside
// trustedDir (the service executable's own directory). Callers can never
// override the executable path over IPC.
type Runner struct {
	mu           sync.Mutex
	trustedDir   string
	coreExeName  string
	readyTimeout time.Duration
	stopTimeout  time.Duration

	cmd        *exec.Cmd
	stdin      io.WriteCloser
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
		readyTimeout: 10 * time.Second,
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

	args := []string{"-d", cleanHome, "-f", cleanConfig, "-watch-stdin"}
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

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return r.statusLocked(), fmt.Errorf("create core stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdinPipe.Close()
		return r.statusLocked(), fmt.Errorf("create core stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		_ = stdinPipe.Close()
		return r.statusLocked(), fmt.Errorf("create core stderr pipe: %w", err)
	}

	r.logMu.Lock()
	r.recentLogs = nil
	r.logMu.Unlock()

	if err := cmd.Start(); err != nil {
		_ = stdinPipe.Close()
		return r.statusLocked(), fmt.Errorf("start core process: %w", err)
	}

	readyCh := make(chan struct{})
	var readyOnce sync.Once
	doneCh := make(chan error, 1)

	streamLines := func(reader io.Reader) {
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 0, 16*1024), 256*1024)
		for scanner.Scan() {
			line := scanner.Text()
			r.appendLog(line)
			if strings.Contains(line, ReadyMarker) {
				readyOnce.Do(func() { close(readyCh) })
			}
		}
	}
	go streamLines(stdoutPipe)
	go streamLines(stderrPipe)
	go func() {
		doneCh <- cmd.Wait()
	}()

	r.cmd = cmd
	r.stdin = stdinPipe
	r.doneCh = doneCh
	r.homeDir = cleanHome
	r.configFile = cleanConfig

	select {
	case <-readyCh:
		return r.statusLocked(), nil
	case waitErr := <-doneCh:
		_ = stdinPipe.Close()
		r.cmd = nil
		r.stdin = nil
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
	if r.cmd == nil || r.doneCh == nil {
		return nil
	}
	// Check if already exited
	select {
	case <-r.doneCh:
		if r.stdin != nil {
			_ = r.stdin.Close()
		}
		r.cmd = nil
		r.stdin = nil
		r.doneCh = nil
		return nil
	default:
	}

	if r.stdin != nil {
		_ = r.stdin.Close()
		r.stdin = nil
	}

	var stopErr error
	select {
	case <-r.doneCh:
	case <-time.After(r.stopTimeout):
		if r.cmd.Process != nil {
			if killErr := r.cmd.Process.Kill(); killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
				stopErr = fmt.Errorf("kill unresponsive core: %w", killErr)
			}
		}
		select {
		case <-r.doneCh:
		case <-time.After(2 * time.Second):
		}
	}

	r.cmd = nil
	r.doneCh = nil
	return stopErr
}

func (r *Runner) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.statusLocked()
}

func (r *Runner) statusLocked() Status {
	if r.cmd != nil && r.doneCh != nil {
		select {
		case <-r.doneCh:
			if r.stdin != nil {
				_ = r.stdin.Close()
			}
			r.cmd = nil
			r.stdin = nil
			r.doneCh = nil
		default:
		}
	}
	st := Status{
		RecentLogs: r.snapshotLogs(),
	}
	if r.cmd != nil && r.cmd.Process != nil {
		st.Running = true
		st.PID = r.cmd.Process.Pid
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
