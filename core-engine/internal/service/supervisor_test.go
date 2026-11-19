package service

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const supervisorFakeCoreSource = `package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	mode := os.Getenv("CLASHFX_SUP_MODE")
	ctrlAddr := os.Getenv("CLASHFX_SUP_CTRL_ADDR")
	marker := os.Getenv("CLASHFX_SUP_MARKER")

	var ln net.Listener
	if ctrlAddr != "" {
		mux := http.NewServeMux()
		mux.HandleFunc("/version", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("{\"meta\":true,\"version\":\"v1.19.34\"}"))
		})
		var err error
		ln, err = net.Listen("tcp", ctrlAddr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "listen %s: %v\n", ctrlAddr, err)
			os.Exit(1)
		}
		defer ln.Close()
		srv := &http.Server{Handler: mux}
		go func() { _ = srv.Serve(ln) }()
	}

	switch mode {
	case "crash_once":
		if _, err := os.Stat(marker); os.IsNotExist(err) {
			_ = os.WriteFile(marker, []byte("crashed-once"), 0600)
			fmt.Printf("time=\"now\" level=info msg=\"RESTful API listening at: %s\"\n", ctrlAddr)
			time.Sleep(150 * time.Millisecond)
			fmt.Fprintln(os.Stderr, "fatal simulated wintun surprise removal")
			os.Exit(42)
		}
		fmt.Printf("time=\"now\" level=info msg=\"RESTful API listening at: %s\"\n", ctrlAddr)
	case "crash_loop":
		fmt.Printf("time=\"now\" level=info msg=\"RESTful API listening at: %s\"\n", ctrlAddr)
		time.Sleep(80 * time.Millisecond)
		fmt.Fprintln(os.Stderr, "fatal simulated persistent crash")
		os.Exit(99)
	case "huge_line":
		fmt.Printf("time=\"now\" level=info msg=\"RESTful API listening at: %s\"\n", ctrlAddr)
		// Emit >300 KiB single line (exceeding old 256 KiB bufio.Scanner limit) followed by marker.
		fmt.Println("HUGE:" + strings.Repeat("A", 320*1024))
		fmt.Println("AFTER_HUGE_LINE_OK")
	default:
		fmt.Printf("time=\"now\" level=info msg=\"RESTful API listening at: %s\"\n", ctrlAddr)
	}

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh
}
`

type mockAIGuard struct {
	mu      sync.Mutex
	calls   []bool
	armed   bool
	cfg     AIGuardConfig
	tunUp   bool
	tunLUID uint64
}

func (m *mockAIGuard) Sync(cfg AIGuardConfig, coreRunning bool) AIGuardStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg = cfg
	m.calls = append(m.calls, coreRunning)
	if !cfg.Enabled {
		m.armed = false
		return AIGuardStatus{Mode: AIGuardModeInactive, Backend: "mock"}
	}
	m.armed = true
	mode := AIGuardModeBlockingLeaks
	if coreRunning && m.tunUp {
		mode = AIGuardModeArmedNormal
	}
	return AIGuardStatus{
		Enabled:        true,
		Armed:          true,
		Mode:           mode,
		Backend:        "mock",
		ProtectedCount: len(cfg.ProtectedExePaths),
		TunLUID:        m.tunLUID,
	}
}

func (m *mockAIGuard) Disarm() AIGuardStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.armed = false
	m.cfg = AIGuardConfig{}
	return AIGuardStatus{Mode: AIGuardModeInactive, Backend: "mock"}
}

func (m *mockAIGuard) Status() AIGuardStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.armed {
		return AIGuardStatus{Mode: AIGuardModeInactive, Backend: "mock"}
	}
	mode := AIGuardModeBlockingLeaks
	if len(m.calls) > 0 && m.calls[len(m.calls)-1] && m.tunUp {
		mode = AIGuardModeArmedNormal
	}
	return AIGuardStatus{
		Enabled:        true,
		Armed:          true,
		Mode:           mode,
		Backend:        "mock",
		ProtectedCount: len(m.cfg.ProtectedExePaths),
		TunLUID:        m.tunLUID,
	}
}

func (m *mockAIGuard) IsTunHealthy(_ string) (uint64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tunLUID, m.tunUp
}

func buildSupervisorFakeCore(t *testing.T, outPath string) {
	t.Helper()
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "go.mod"), []byte("module fake_sup\n\ngo 1.21\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "main.go"), []byte(supervisorFakeCoreSource), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-ldflags=-s -w", "-o", outPath, ".")
	cmd.Dir = srcDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile supervisor fake core: %v\n%s", err, string(out))
	}
}

func pickFreeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func TestRunnerAutoRestartCrashLogsAndAIGuardTightening(t *testing.T) {
	trustedDir := t.TempDir()
	homeDir := t.TempDir()
	logDir := filepath.Join(t.TempDir(), "logs")
	svcDataDir := t.TempDir()
	ctrlAddr := pickFreeAddr(t)

	cfgPath := filepath.Join(homeDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(fmt.Sprintf("external-controller: %s\ntun:\n  enable: true\n  device: ClashFX\n", ctrlAddr)), 0600); err != nil {
		t.Fatal(err)
	}

	exeName := "fake_sup_core"
	if runtime.GOOS == "windows" {
		exeName += ".exe"
	}
	buildSupervisorFakeCore(t, filepath.Join(trustedDir, exeName))

	runner := NewRunner(trustedDir)
	mockGuard := &mockAIGuard{tunUp: true, tunLUID: 777}
	runner.SetCoreBinaryForTest(exeName, 8*time.Second, 3*time.Second)
	runner.SetSupervisionTimingForTest([]time.Duration{100 * time.Millisecond, 150 * time.Millisecond}, 500*time.Millisecond, 10*time.Second, 3)
	runner.SetAIGuardForTest(mockGuard, svcDataDir)
	defer func() { _ = runner.ShutdownService() }()

	// Arm AI Guard before core start and verify persisted state file exists.
	syncResp := runner.Handle(Request{
		Action: "sync_ai_guard",
		AIGuard: &AIGuardConfig{
			Enabled:           true,
			ProtectedExePaths: []string{`C:\Users\alice\Antigravity\language_server_windows_x64.exe`},
			AllowLAN:          true,
		},
	})
	if !syncResp.OK || syncResp.Status == nil || syncResp.Status.AIGuard == nil || syncResp.Status.AIGuard.Mode != AIGuardModeBlockingLeaks {
		t.Fatalf("expected AI Guard in blocking_leaks before core starts, got %+v", syncResp)
	}
	if _, err := os.Stat(aiGuardStateFilePath(svcDataDir)); err != nil {
		t.Fatalf("expected persisted ai-guard-state.json: %v", err)
	}

	marker := filepath.Join(t.TempDir(), "crashed.flag")
	t.Setenv("CLASHFX_SUP_CTRL_ADDR", ctrlAddr)
	t.Setenv("CLASHFX_SUP_MODE", "crash_once")
	t.Setenv("CLASHFX_SUP_MARKER", marker)

	startResp := runner.Handle(Request{
		Action:     "start_core",
		HomeDir:    homeDir,
		ConfigFile: cfgPath,
		LogDir:     logDir,
	})
	if !startResp.OK || startResp.Status == nil || !startResp.Status.Running {
		t.Fatalf("start_core failed: %+v", startResp)
	}
	firstPID := startResp.Status.PID

	// Wait for simulated crash + automatic supervision restart.
	deadline := time.Now().Add(6 * time.Second)
	var recovered Status
	for time.Now().Before(deadline) {
		st := runner.Status()
		if st.Running && st.PID != firstPID && st.RestartCount >= 1 && st.Health == CoreHealthHealthy {
			recovered = st
			break
		}
		time.Sleep(60 * time.Millisecond)
	}
	if !recovered.Running || recovered.RestartCount < 1 || recovered.PID == firstPID {
		t.Fatalf("expected automatic crash recovery with new PID (first=%d), got %+v", firstPID, runner.Status())
	}
	if recovered.LastExitCode != 42 || !strings.Contains(recovered.LastCrashReason, "wintun surprise removal") {
		t.Fatalf("expected crash diagnostics captured, got exit=%d reason=%q", recovered.LastExitCode, recovered.LastCrashReason)
	}
	if recovered.AIGuard == nil || recovered.AIGuard.Mode != AIGuardModeArmedNormal {
		t.Fatalf("expected AI Guard back in armed_normal after recovery, got %+v", recovered.AIGuard)
	}

	// Verify complete persistent logs on disk (mihomo-core.log & clashfx-service-events.log).
	coreLogBytes, err := os.ReadFile(filepath.Join(logDir, "mihomo-core.log"))
	if err != nil {
		t.Fatalf("read persisted mihomo-core.log: %v", err)
	}
	coreLogText := string(coreLogBytes)
	if !strings.Contains(coreLogText, "wintun surprise removal") || !strings.Contains(coreLogText, "mihomo_core exited unexpectedly") {
		t.Fatalf("expected full crash logs persisted to mihomo-core.log, got:\n%s", coreLogText)
	}
	if _, err := os.Stat(filepath.Join(logDir, "clashfx-service-events.log")); err != nil {
		t.Fatalf("expected clashfx-service-events.log on disk: %v", err)
	}

	// Verify explicit stop_core halts supervision and does not auto-restart.
	stopResp := runner.Handle(Request{Action: "stop_core"})
	if !stopResp.OK || stopResp.Status.Running || stopResp.Status.Health != CoreHealthStopped {
		t.Fatalf("expected cleanly stopped status, got %+v", stopResp)
	}
	time.Sleep(300 * time.Millisecond)
	if st := runner.Status(); st.Running {
		t.Fatalf("explicitly stopped core should not auto-restart: %+v", st)
	}
}

func TestRunnerCrashLoopCircuitBreakerAndHugeLogLine(t *testing.T) {
	trustedDir := t.TempDir()
	homeDir := t.TempDir()
	logDir := filepath.Join(t.TempDir(), "logs")
	ctrlAddr := pickFreeAddr(t)

	cfgPath := filepath.Join(homeDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(fmt.Sprintf("external-controller: %s\n", ctrlAddr)), 0600); err != nil {
		t.Fatal(err)
	}

	exeName := "fake_sup_core"
	if runtime.GOOS == "windows" {
		exeName += ".exe"
	}
	buildSupervisorFakeCore(t, filepath.Join(trustedDir, exeName))

	runner := NewRunner(trustedDir)
	runner.SetCoreBinaryForTest(exeName, 8*time.Second, 3*time.Second)
	runner.SetSupervisionTimingForTest([]time.Duration{60 * time.Millisecond, 60 * time.Millisecond}, 2*time.Second, 10*time.Second, 3)
	defer func() { _ = runner.ShutdownService() }()

	// 1. Huge single log line (>300 KiB) must not stall pipe or drop subsequent lines.
	t.Setenv("CLASHFX_SUP_CTRL_ADDR", ctrlAddr)
	t.Setenv("CLASHFX_SUP_MODE", "huge_line")
	resp := runner.Handle(Request{Action: "start_core", HomeDir: homeDir, ConfigFile: cfgPath, LogDir: logDir})
	if !resp.OK {
		t.Fatalf("start huge_line core failed: %+v", resp)
	}
	deadline := time.Now().Add(3 * time.Second)
	sawAfter := false
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(filepath.Join(logDir, "mihomo-core.log")); err == nil && strings.Contains(string(data), "AFTER_HUGE_LINE_OK") {
			sawAfter = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !sawAfter {
		t.Fatal("expected AFTER_HUGE_LINE_OK to be logged after >300 KiB line without pipe stall")
	}
	_ = runner.Stop()

	// 2. Persistent crashes trip crash_loop circuit breaker.
	t.Setenv("CLASHFX_SUP_MODE", "crash_loop")
	resp = runner.Handle(Request{Action: "start_core", HomeDir: homeDir, ConfigFile: cfgPath, LogDir: logDir})
	if !resp.OK {
		t.Fatalf("start crash_loop core failed: %+v", resp)
	}
	deadline = time.Now().Add(6 * time.Second)
	hitCrashLoop := false
	for time.Now().Before(deadline) {
		st := runner.Status()
		if st.Health == CoreHealthCrashLoop && st.ConsecutiveCrashes >= 3 {
			hitCrashLoop = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !hitCrashLoop {
		t.Fatalf("expected crash_loop circuit breaker to trip, got %+v", runner.Status())
	}
}
