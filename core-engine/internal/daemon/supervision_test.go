package daemon

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/clashfx/core-engine/internal/profile"
)

const daemonCrashFakeCoreSrc = `package main

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
	for _, a := range os.Args[1:] {
		if a == "-v" {
			fmt.Println("Mihomo Meta v1.19.34 darwin arm64")
			return
		}
		if a == "-t" {
			fmt.Println("configuration file test is successful")
			return
		}
	}
	ctrl := os.Getenv("CLASHFX_DAEMON_TEST_CTRL")
	mixed := os.Getenv("CLASHFX_DAEMON_TEST_MIXED")
	marker := os.Getenv("CLASHFX_DAEMON_TEST_MARKER")
	if mixed != "" {
		if mixLn, mErr := net.Listen("tcp", mixed); mErr == nil {
			defer mixLn.Close()
			go func() {
				for {
					c, aErr := mixLn.Accept()
					if aErr != nil {
						return
					}
					_ = c.Close()
				}
			}()
		}
	}
	if ctrl != "" {
		mux := http.NewServeMux()
		mux.HandleFunc("/version", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("{\"meta\":true,\"version\":\"v1.19.34\"}"))
		})
		mux.HandleFunc("/configs", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPut {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("{\"mode\":\"rule\"}"))
		})
		mux.HandleFunc("/proxies", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("{\"proxies\":{}}"))
		})
		mux.HandleFunc("/providers/proxies", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("{\"providers\":{}}"))
		})
		ln, err := net.Listen("tcp", ctrl)
		if err != nil {
			fmt.Fprintf(os.Stderr, "listen %s: %v\n", ctrl, err)
			os.Exit(1)
		}
		defer ln.Close()
		srv := &http.Server{Handler: mux}
		go func() { _ = srv.Serve(ln) }()
	}

	fmt.Printf("time=\"now\" level=info msg=\"RESTful API listening at: %s\"\n", ctrl)
	if marker != "" {
		if _, err := os.Stat(marker); os.IsNotExist(err) {
			_ = os.WriteFile(marker, []byte("1"), 0600)
			time.Sleep(180 * time.Millisecond)
			fmt.Fprintln(os.Stderr, "simulated mihomo crash after startup")
			os.Exit(77)
		}
	}
	// Keep running until stopped
	_ = strings.TrimSpace("ready")
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh
}
`

func compileDaemonFakeCore(t *testing.T, outPath string) {
	t.Helper()
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "go.mod"), []byte("module fake_dcore\n\ngo 1.21\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "main.go"), []byte(daemonCrashFakeCoreSrc), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-ldflags=-s -w", "-o", outPath, ".")
	cmd.Dir = srcDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile daemon fake core: %v\n%s", err, string(out))
	}
}

func TestEnginePersistentLogsAutoRecoveryAndSystemProxyFailClosedPolicy(t *testing.T) {
	dataDir := t.TempDir()
	binDir := t.TempDir()
	exeName := "mihomo_core"
	if runtime.GOOS == "windows" {
		exeName += ".exe"
	}
	compileDaemonFakeCore(t, filepath.Join(binDir, exeName))

	lnAPI, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	apiPort := lnAPI.Addr().(*net.TCPAddr).Port
	_ = lnAPI.Close()

	lnMix, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mixedPort := lnMix.Addr().(*net.TCPAddr).Port
	_ = lnMix.Close()

	marker := filepath.Join(t.TempDir(), "crashed")
	t.Setenv("CLASHFX_DAEMON_TEST_CTRL", fmt.Sprintf("127.0.0.1:%d", apiPort))
	t.Setenv("CLASHFX_DAEMON_TEST_MIXED", fmt.Sprintf("127.0.0.1:%d", mixedPort))
	t.Setenv("CLASHFX_DAEMON_TEST_MARKER", marker)

	var pushes int32
	eng, err := NewEngine(Config{
		AppDataDir: dataDir,
		BinDir:     binDir,
	}, func() {
		atomic.AddInt32(&pushes, 1)
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer eng.Shutdown()

	eng.localRunner.SetSupervisionTimingForTest([]time.Duration{80 * time.Millisecond, 100 * time.Millisecond}, 500*time.Millisecond, 10*time.Second, 3)

	if _, err := eng.store.UpdateSettings(func(s *profile.AppSettings) error {
		s.APIPort = apiPort
		s.PortOverride = mixedPort
		s.EnhancedMode = false
		s.SystemProxyEnabled = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("eng.Start: %v", err)
	}

	// Wait for simulated first crash + automatic supervision restart.
	deadline := time.Now().Add(6 * time.Second)
	recovered := false
	for time.Now().Before(deadline) {
		snap := eng.Snapshot()
		if snap.CoreRunning && snap.CoreRestartCount >= 1 && snap.CoreHealth == "healthy" {
			recovered = true
			break
		}
		time.Sleep(60 * time.Millisecond)
	}
	if !recovered {
		t.Fatalf("expected Engine core auto-recovery after crash, got snapshot: %+v", eng.Snapshot())
	}

	// Verify syncFromLiveCore detects and notifies transitions cleanly.
	pushesBefore := atomic.LoadInt32(&pushes)
	eng.syncFromLiveCore(context.Background(), 1)
	if atomic.LoadInt32(&pushes) <= pushesBefore {
		t.Fatalf("expected syncFromLiveCore to push state transition after restart")
	}

	// Verify complete rotating logs exist on disk for both daemon and core.
	snap := eng.Snapshot()
	for label, p := range map[string]string{
		"daemon": snap.DaemonLogFile,
		"core":   snap.CoreLogFile,
	} {
		data, rErr := os.ReadFile(p)
		if rErr != nil || len(data) == 0 {
			t.Fatalf("expected non-empty %s log at %s (err=%v)", label, p, rErr)
		}
	}
	diag := eng.DiagnosticInfo()
	if !strings.Contains(diag, "Core Log:") || !strings.Contains(diag, "Daemon Log:") || !strings.Contains(diag, "simulated mihomo crash after startup") {
		t.Fatalf("expected DiagnosticInfo to contain log file paths and crash tail, got:\n%s", diag)
	}

	// Verify SeedActiveStateFromDisk restores runtime ports from .runtime-config.yaml cleanly.
	eng.ctrl.ResetActiveState()
	if !eng.ctrl.SeedActiveStateFromDisk(eng.ctrl.RuntimeConfigPath()) {
		t.Fatal("expected SeedActiveStateFromDisk to succeed")
	}
	if seeded := eng.ctrl.LastState(); seeded.APIPort != apiPort || seeded.MixedPort != mixedPort {
		t.Fatalf("unexpected seeded state: %+v (want api=%d mixed=%d)", seeded, apiPort, mixedPort)
	}

	// Verify fail-closed vs release-dead-proxy policy when core is stopped for >15s.
	_ = eng.localRunner.Stop()
	// Add a dummy profile file so effectiveRuntimeSettings keeps SystemProxyEnabled active.
	_ = os.WriteFile(eng.store.ProfilePath("TestSub"), []byte(profile.DefaultStarterConfigYAML), 0600)
	_, _ = eng.subMgr.AddOrUpdate("TestSub", "https://example.com/sub.yaml", "")
	_, _ = eng.store.UpdateSettings(func(s *profile.AppSettings) error {
		s.SelectedProfile = "TestSub"
		s.SystemProxyEnabled = true
		s.AILockEnabled = false
		return nil
	})
	eng.statusMu.Lock()
	eng.coreDownSince = time.Now().Add(-20 * time.Second)
	eng.statusMu.Unlock()

	eng.reconcileSystemProxy()
	if st, _ := eng.sysProxy.CurrentOSState(); st.ProxyEnable != 0 {
		t.Fatalf("expected system proxy released when AI Lock is OFF and core dead >15s, got %+v", st)
	}

	_, _ = eng.store.UpdateSettings(func(s *profile.AppSettings) error {
		s.AILockEnabled = true
		s.AILockTarget = "US-Node"
		return nil
	})
	eng.reconcileSystemProxy()
	if st, _ := eng.sysProxy.CurrentOSState(); st.ProxyEnable != 1 {
		t.Fatalf("expected system proxy KEPT fail-closed when AI Lock is ON even while core is dead, got %+v", st)
	}
}

const daemonConfigDrivenFakeCoreSrc = `package main

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

func parseSimpleYAML(path string) (ctrl, secret, mixed string) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "external-controller:") {
			ctrl = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "external-controller:")), "\"'")
		} else if strings.HasPrefix(line, "secret:") {
			secret = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "secret:")), "\"'")
		} else if strings.HasPrefix(line, "mixed-port:") {
			mixed = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "mixed-port:")), "\"'")
		}
	}
	return ctrl, secret, mixed
}

func main() {
	var cfgPath string
	for i := 1; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "-v":
			fmt.Println("Mihomo Meta v1.19.34 darwin arm64")
			return
		case "-t":
			fmt.Println("configuration file test is successful")
			return
		case "-f":
			if i+1 < len(os.Args) {
				cfgPath = os.Args[i+1]
				i++
			}
		}
	}
	ctrl, secret, mixed := parseSimpleYAML(cfgPath)
	if mixed != "" {
		if mixLn, err := net.Listen("tcp", "127.0.0.1:"+mixed); err == nil {
			defer mixLn.Close()
			go func() {
				for {
					c, aErr := mixLn.Accept()
					if aErr != nil {
						return
					}
					_ = c.Close()
				}
			}()
		}
	}
	if ctrl == "" {
		fmt.Fprintln(os.Stderr, "missing external-controller")
		os.Exit(2)
	}
	checkAuth := func(w http.ResponseWriter, r *http.Request) bool {
		if secret == "" {
			return true
		}
		if r.Header.Get("Authorization") != "Bearer "+secret {
			w.WriteHeader(http.StatusUnauthorized)
			return false
		}
		return true
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{\"meta\":true,\"version\":\"v1.19.34\"}"))
	})
	mux.HandleFunc("/configs", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(w, r) {
			return
		}
		// Match real Mihomo PUT /configs behavior: reload config WITHOUT recreating external-controller or changing secret.
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{\"mode\":\"rule\"}"))
	})
	mux.HandleFunc("/proxies", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{\"proxies\":{}}"))
	})
	mux.HandleFunc("/providers/proxies", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{\"providers\":{}}"))
	})

	ln, err := net.Listen("tcp", ctrl)
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen %s: %v\n", ctrl, err)
		os.Exit(1)
	}
	defer ln.Close()
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()

	fmt.Printf("time=\"now\" level=info msg=\"RESTful API listening at: %s\"\n", ctrl)
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh
}
`

func compileConfigDrivenFakeCore(t *testing.T, outPath string) {
	t.Helper()
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "go.mod"), []byte("module fake_cfg_core\n\ngo 1.21\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "main.go"), []byte(daemonConfigDrivenFakeCoreSrc), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-ldflags=-s -w", "-o", outPath, ".")
	cmd.Dir = srcDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile config-driven fake core: %v\n%s", err, string(out))
	}
}

func TestEngineHotReloadFallbackAPIPortNoDesyncAndExplicitControllerChangeRestarts(t *testing.T) {
	dataDir := t.TempDir()
	binDir := t.TempDir()
	exeName := "mihomo_core"
	if runtime.GOOS == "windows" {
		exeName += ".exe"
	}
	compileConfigDrivenFakeCore(t, filepath.Join(binDir, exeName))

	// Hold an external listener on preferredAPIPort so startup must choose a fallback API port.
	blockerLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blockerLn.Close()
	preferredAPIPort := blockerLn.Addr().(*net.TCPAddr).Port

	lnMix, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mixedPort := lnMix.Addr().(*net.TCPAddr).Port
	_ = lnMix.Close()

	lnNewAPI, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	explicitNewAPIPort := lnNewAPI.Addr().(*net.TCPAddr).Port
	_ = lnNewAPI.Close()

	eng, err := NewEngine(Config{
		AppDataDir: dataDir,
		BinDir:     binDir,
	}, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer eng.Shutdown()

	if _, err := eng.store.UpdateSettings(func(s *profile.AppSettings) error {
		s.APIPort = preferredAPIPort
		s.APISecret = ""
		s.PortOverride = mixedPort
		s.EnhancedMode = false
		s.SystemProxyEnabled = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("eng.Start: %v", err)
	}

	snap1 := eng.Snapshot()
	if !snap1.CoreRunning {
		t.Fatal("expected core running after Start")
	}
	fallbackAPIPort := snap1.Runtime.APIPort
	if fallbackAPIPort <= 0 || fallbackAPIPort == preferredAPIPort {
		t.Fatalf("expected startup fallback APIPort != occupied %d, got %d", preferredAPIPort, fallbackAPIPort)
	}
	pid1 := eng.localRunner.Status().PID
	if pid1 <= 0 {
		t.Fatalf("expected valid running PID, got %d", pid1)
	}

	// Release preferredAPIPort (analogous to TIME_WAIT expiring on 19090), then run a routine reload.
	_ = blockerLn.Close()
	if _, err := eng.applyAndSync(context.Background(), false, false); err != nil {
		t.Fatalf("routine reload applyAndSync failed: %v", err)
	}

	snap2 := eng.Snapshot()
	if snap2.Runtime.APIPort != fallbackAPIPort {
		t.Fatalf("routine reload desynced APIPort: got %d, want preserved fallback %d", snap2.Runtime.APIPort, fallbackAPIPort)
	}
	if pid2 := eng.localRunner.Status().PID; pid2 != pid1 {
		t.Fatalf("routine reload unexpectedly restarted core PID %d -> %d", pid1, pid2)
	}
	// Deep health check (tick%4 == 0) must pass cleanly against fallbackAPIPort!
	eng.syncFromLiveCore(context.Background(), 4)
	eng.statusMu.RLock()
	fails := eng.healthFailCount
	eng.statusMu.RUnlock()
	if fails != 0 {
		t.Fatalf("expected 0 deep health check failures after routine reload, got %d", fails)
	}

	// Explicit user settings.APIPort change MUST restart core cleanly (NOT PUT /configs desync!).
	nextSettings := eng.store.Settings()
	nextSettings.APIPort = explicitNewAPIPort
	if err := eng.UpdateGeneralSettings(context.Background(), nextSettings); err != nil {
		t.Fatalf("UpdateGeneralSettings(APIPort) failed: %v", err)
	}
	snap3 := eng.Snapshot()
	if snap3.Runtime.APIPort != explicitNewAPIPort {
		t.Fatalf("expected Runtime.APIPort = %d after explicit change, got %d", explicitNewAPIPort, snap3.Runtime.APIPort)
	}
	pid3 := eng.localRunner.Status().PID
	if pid3 == pid1 || pid3 <= 0 {
		t.Fatalf("expected explicit APIPort change to restart core with new PID (prev=%d, got=%d)", pid1, pid3)
	}
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/version", explicitNewAPIPort))
	if err != nil {
		t.Fatalf("GET /version on new APIPort %d failed: %v", explicitNewAPIPort, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /version on new APIPort %d status = %d, want 200", explicitNewAPIPort, resp.StatusCode)
	}

	// Explicit user settings.APISecret change MUST also restart core so new secret takes effect.
	nextSettings = eng.store.Settings()
	nextSettings.APISecret = "clashfx-secret-check"
	if err := eng.UpdateGeneralSettings(context.Background(), nextSettings); err != nil {
		t.Fatalf("UpdateGeneralSettings(APISecret) failed: %v", err)
	}
	pid4 := eng.localRunner.Status().PID
	if pid4 == pid3 || pid4 <= 0 {
		t.Fatalf("expected explicit APISecret change to restart core with new PID (prev=%d, got=%d)", pid3, pid4)
	}
	unauthResp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/version", explicitNewAPIPort))
	if err != nil {
		t.Fatalf("unauthenticated GET /version failed: %v", err)
	}
	_ = unauthResp.Body.Close()
	if unauthResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without secret after APISecret change, got %d", unauthResp.StatusCode)
	}
	eng.syncFromLiveCore(context.Background(), 4)
	eng.statusMu.RLock()
	failsAfterSecret := eng.healthFailCount
	eng.statusMu.RUnlock()
	if failsAfterSecret != 0 {
		t.Fatalf("expected 0 deep health check failures after APISecret restart, got %d", failsAfterSecret)
	}
}
