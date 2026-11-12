package nodeservices

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/clashfx/core-engine/internal/ailock"
	"github.com/clashfx/core-engine/internal/profile"
	"github.com/clashfx/core-engine/internal/service"
	"github.com/clashfx/core-engine/internal/subscription"
	"github.com/clashfx/core-engine/internal/transaction"
)

func TestNodePortRegistryReconcileReorderAndRenameStability(t *testing.T) {
	reg := NewRegistry()
	avail := func(port int) bool { return port != 21001 } // simulate 21001 busy on initial allocation

	initialProxies := []map[string]any{
		{"name": "HK-01", "type": "socks5", "server": "hk1.example.com"},
		{"name": "JP-01", "type": "socks5", "server": "jp1.example.com"},
		{"name": "US-01", "type": "socks5", "server": "us1.example.com"},
	}

	b1, err := reg.Reconcile("local:sub", initialProxies, 21000, map[int]bool{7890: true}, avail)
	if err != nil {
		t.Fatal(err)
	}
	// Expect 21000, 21002 (skipped 21001), 21003
	if b1[0].Port != 21000 || b1[1].Port != 21002 || b1[2].Port != 21003 {
		t.Fatalf("unexpected initial bindings: %+v", b1)
	}

	// Reorder + rename unambiguous JP-01 -> JP-Tokyo while keeping jp1.example.com
	reordered := []map[string]any{
		{"name": "US-01", "type": "socks5", "server": "us1.example.com"},
		{"name": "JP-Tokyo", "type": "socks5", "server": "jp1.example.com"},
		{"name": "HK-01", "type": "socks5", "server": "hk1.example.com"},
	}
	b2, err := reg.Reconcile("local:sub", reordered, 21000, map[int]bool{7890: true}, avail)
	if err != nil {
		t.Fatal(err)
	}
	if b2[0].Name != "US-01" || b2[0].Port != 21003 ||
		b2[1].Name != "JP-Tokyo" || b2[1].Port != 21002 ||
		b2[2].Name != "HK-01" || b2[2].Port != 21000 {
		t.Fatalf("ports drifted across reorder/rename: %+v", b2)
	}

	// Batch port edit + disable check
	if err := reg.SetPorts("local:sub", map[string]int{"HK-01": 21500}); err != nil {
		t.Fatal(err)
	}
	if err := reg.SetEnabled("local:sub", map[string]bool{"US-01": false}); err != nil {
		t.Fatal(err)
	}
	dp, err := reg.PrepareDirectPort(map[int]bool{20999: true}, false, func(int) bool { return true })
	if err != nil || dp != 20998 {
		t.Fatalf("PrepareDirectPort = (%d, %v), want (20998, nil)", dp, err)
	}
}

func TestRealMihomoCoreMultiNodePortsAILockSubRulesWatchdogAndSpeedTest(t *testing.T) {
	coreExe := strings.TrimSpace(os.Getenv("CLASHFX_CORE_EXE"))
	if coreExe == "" {
		t.Skip("CLASHFX_CORE_EXE not set; skipping live mihomo_core P1-2 verification")
	}

	// Local target server handling /whoami, /generate_204, /speedblob, /slowblob
	targetLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer targetLn.Close()
	targetPort := targetLn.Addr().(*net.TCPAddr).Port

	payloadChunk := bytes.Repeat([]byte("X"), 64*1024)
	targetSrv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/generate_204":
				w.WriteHeader(http.StatusNoContent)
			case "/speedblob":
				w.Header().Set("Content-Type", "application/octet-stream")
				w.WriteHeader(http.StatusOK)
				// Stream ~3 MiB rapidly
				for i := 0; i < 48; i++ {
					if _, err := w.Write(payloadChunk); err != nil {
						return
					}
				}
			case "/slowblob":
				w.Header().Set("Content-Type", "application/octet-stream")
				w.WriteHeader(http.StatusOK)
				flusher, _ := w.(http.Flusher)
				for i := 0; i < 40; i++ {
					if _, err := w.Write(payloadChunk[:4096]); err != nil {
						return
					}
					if flusher != nil {
						flusher.Flush()
					}
					time.Sleep(150 * time.Millisecond)
				}
			default:
				_, _ = fmt.Fprintf(w, "host=%s via=%s", r.Host, r.Header.Get("X-Node-Tag"))
			}
		}),
	}
	go func() { _ = targetSrv.Serve(targetLn) }()
	defer targetSrv.Close()

	alphaPort, stopAlpha := startNodeSOCKS5Mock(t, targetPort, "NODE_ALPHA")
	defer stopAlpha()
	betaPort, stopBeta := startNodeSOCKS5Mock(t, targetPort, "NODE_BETA")
	defer stopBeta()
	aiPort, stopAI := startNodeSOCKS5Mock(t, targetPort, "LOCKED_AI_NODE")
	defer stopAI()

	trustedDir := filepath.Dir(coreExe)
	runner := service.NewRunner(trustedDir)
	runner.SetCoreBinaryForTest(filepath.Base(coreExe), 12*time.Second, 6*time.Second)
	defer func() { _ = runner.Stop() }()

	helper := subscription.BinaryCoreHelper{CoreBinaryPath: coreExe}
	dataDir := t.TempDir()
	profStore, err := profile.NewStore(dataDir, "")
	if err != nil {
		t.Fatal(err)
	}
	defer profStore.Close()

	subMgr, err := subscription.NewManager(profStore, helper)
	if err != nil {
		t.Fatal(err)
	}

	// Serve a managed subscription over HTTP so direct-refresh through 127.0.0.1:<directPort> can pull it!
	var directSubHits atomic.Int32
	buildSubYAML := func() string {
		return fmt.Sprintf(`mixed-port: 17994
proxies:
  - name: NodeAlpha
    type: socks5
    server: 127.0.0.1
    port: %d
  - name: NodeBeta
    type: socks5
    server: 127.0.0.1
    port: %d
  - name: LockedAINode
    type: socks5
    server: 127.0.0.1
    port: %d
proxy-groups:
  - name: Proxy
    type: select
    proxies: [NodeAlpha, NodeBeta, LockedAINode]
rules:
  - MATCH,Proxy
`, alphaPort, betaPort, aiPort)
	}
	subSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		directSubHits.Add(1)
		_, _ = w.Write([]byte(buildSubYAML()))
	}))
	defer subSrv.Close()

	if _, err := subMgr.AddOrUpdate("managed_sub", subSrv.URL, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := subMgr.Refresh(context.Background(), "managed_sub", 0); err != nil {
		t.Fatal(err)
	}

	nodeStore := NewStore(dataDir)
	nsSettings := DefaultSettings()
	nsSettings.FirstPort = 21200
	nsSettings.FailureThreshold = 2
	if err := nodeStore.UpdateSettings(nsSettings); err != nil {
		t.Fatal(err)
	}

	_, err = profStore.UpdateSettings(func(s *profile.AppSettings) error {
		s.SelectedProfile = "managed_sub"
		s.PortOverride = 17994
		s.APIPort = 19294
		s.AILockEnabled = true
		s.AILockTarget = "LockedAINode"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	applier := &nsRunnerApplier{helper: helper, runner: runner}
	ctrl := transaction.NewController(
		profStore,
		applier,
		"",
		ailock.TransformHookForController(nil),
		nodeStore.TransformHookForController(func() bool { return runner.Status().Running }),
	)

	state, err := ctrl.Apply(context.Background(), false)
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	bindings, directPortPtr, _, err := nodeStore.ActiveBindings("managed_sub")
	if err != nil || len(bindings) != 3 || directPortPtr == nil {
		t.Fatalf("unexpected ActiveBindings: bindings=%+v direct=%v err=%v", bindings, directPortPtr, err)
	}
	portByName := map[string]int{}
	for _, b := range bindings {
		portByName[b.Name] = b.Port
	}

	// 1. Concurrent multi-port egress split + AI Lock sub-rules enforcement!
	var wg sync.WaitGroup
	errCh := make(chan error, 3)
	checkPort := func(port int, targetURL, expectedTag string) {
		defer wg.Done()
		body, reqErr := getThroughNodePort(port, targetURL)
		if reqErr != nil {
			errCh <- fmt.Errorf("port %d %s failed: %w", port, targetURL, reqErr)
			return
		}
		if !strings.Contains(body, "via="+expectedTag) {
			errCh <- fmt.Errorf("port %d %s got %q, want via=%s", port, targetURL, body, expectedTag)
		}
	}
	wg.Add(3)
	go checkPort(portByName["NodeAlpha"], "http://example.org/whoami", "NODE_ALPHA")
	go checkPort(portByName["NodeBeta"], "http://example.org/whoami", "NODE_BETA")
	// Protected AI domain sent into NodeAlpha's dedicated port MUST still route out LOCKED_AI_NODE!
	go checkPort(portByName["NodeAlpha"], "http://api.anthropic.com/v1/messages", "LOCKED_AI_NODE")
	wg.Wait()
	close(errCh)
	for e := range errCh {
		t.Fatal(e)
	}

	// 2. 204 Watchdog + Consecutive Failures -> Automatic DIRECT Subscription Refresh!
	hitsBeforeWatchdog := directSubHits.Load()
	coord := NewCoordinator(
		nodeStore,
		func() CoreAPIConfig {
			return CoreAPIConfig{
				ProfileName: "managed_sub",
				APIPort:     state.APIPort,
				APISecret:   state.APISecret,
				CoreRunning: runner.Status().Running,
			}
		},
		func(ctx context.Context, profileName string, dp int) error {
			if _, rErr := subMgr.Refresh(ctx, profileName, dp); rErr != nil {
				return rErr
			}
			_, rErr := ctrl.Apply(ctx, false)
			return rErr
		},
		nil,
	)
	defer coord.Stop()

	probeURL := fmt.Sprintf("http://127.0.0.1:%d/generate_204", targetPort)
	speedFastURL := fmt.Sprintf("http://127.0.0.1:%d/speedblob", targetPort)
	coord.SetEndpointsForTest(probeURL, probeURL, speedFastURL, 3*1024*1024, 5*time.Second)

	// Kill NodeBeta so it fails 2 consecutive checks (FailureThreshold=2) -> triggers DIRECT refresh!
	stopBeta()
	if err := coord.CheckConnectivityNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if directSubHits.Load() != hitsBeforeWatchdog {
		t.Fatal("should not refresh after only 1 failure when threshold=2")
	}
	if err := coord.CheckConnectivityNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if directSubHits.Load() <= hitsBeforeWatchdog {
		t.Fatalf("expected automatic DIRECT subscription refresh on 2nd consecutive failure, hits=%d", directSubHits.Load())
	}

	// 3. Disable dead NodeBeta port, run sequential download speed test on healthy ports, then test cancellation!
	if err := nodeStore.UpdateProfileBindings("managed_sub", nil, map[string]bool{
		"NodeBeta":     false,
		"LockedAINode": false,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := ctrl.Apply(context.Background(), false); err != nil {
		t.Fatal(err)
	}

	if err := coord.StartSpeedTest(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		snap, _ := coord.Snapshot("managed_sub")
		if !snap.IsSpeedTest {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	snap, err := coord.Snapshot("managed_sub")
	if err != nil {
		t.Fatal(err)
	}
	measuredOK := false
	for _, row := range snap.Rows {
		if row.Name == "NodeAlpha" && row.Speed.Kind == string(SpeedMeasured) && row.Speed.MBps > 0 {
			measuredOK = true
			t.Logf("NodeAlpha measured download speed: %.2f MB/s", row.Speed.MBps)
		}
	}
	if !measuredOK {
		t.Fatalf("expected NodeAlpha SpeedMeasured > 0, got rows=%+v", snap.Rows)
	}

	// Now start slow stream and cancel mid-flight!
	slowURL := fmt.Sprintf("http://127.0.0.1:%d/slowblob", targetPort)
	coord.SetEndpointsForTest(probeURL, probeURL, slowURL, 20*1024*1024, 8*time.Second)
	if err := coord.StartSpeedTest(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(250 * time.Millisecond)
	coord.CancelSpeedTest(false)
	snapCancelled, _ := coord.Snapshot("managed_sub")
	if snapCancelled.IsSpeedTest {
		t.Fatal("expected IsSpeedTest=false immediately after CancelSpeedTest")
	}
	cancelledOK := false
	for _, row := range snapCancelled.Rows {
		if row.Name == "NodeAlpha" && row.Speed.Kind == string(SpeedCancelled) {
			cancelledOK = true
		}
	}
	if !cancelledOK {
		t.Fatalf("expected NodeAlpha SpeedCancelled after CancelSpeedTest, got %+v", snapCancelled.Rows)
	}
}

type nsRunnerApplier struct {
	helper subscription.BinaryCoreHelper
	runner *service.Runner
}

func (a *nsRunnerApplier) ValidateConfig(homeDir, configPath string) error {
	return a.helper.ValidateConfig(homeDir, configPath)
}

func (a *nsRunnerApplier) ApplyRuntimeConfig(ctx context.Context, _ profile.AppSettings, homeDir, runtimeConfigPath string, apiPort int, secret string) error {
	if a.runner.Status().Running {
		return transaction.ReloadViaREST(ctx, apiPort, secret, runtimeConfigPath)
	}
	_, err := a.runner.Start(homeDir, runtimeConfigPath, "")
	return err
}

func getThroughNodePort(port int, targetURL string) (string, error) {
	proxyURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	tr := &http.Transport{
		Proxy:             http.ProxyURL(proxyURL),
		DisableKeepAlives: true,
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Timeout: 4 * time.Second, Transport: tr}
	resp, err := client.Get(targetURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}
	return string(b), nil
}

func startNodeSOCKS5Mock(t *testing.T, targetPort int, tag string) (int, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var closed atomic.Bool
	stop := func() {
		if closed.CompareAndSwap(false, true) {
			_ = ln.Close()
		}
	}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(8 * time.Second))
				hdr := make([]byte, 2)
				if _, err := io.ReadFull(c, hdr); err != nil || hdr[0] != 0x05 {
					return
				}
				if _, err := io.ReadFull(c, make([]byte, int(hdr[1]))); err != nil {
					return
				}
				_, _ = c.Write([]byte{0x05, 0x00})

				reqHdr := make([]byte, 4)
				if _, err := io.ReadFull(c, reqHdr); err != nil || reqHdr[1] != 0x01 {
					return
				}
				switch reqHdr[3] {
				case 0x01:
					if _, err := io.ReadFull(c, make([]byte, 6)); err != nil {
						return
					}
				case 0x03:
					dl := make([]byte, 1)
					if _, err := io.ReadFull(c, dl); err != nil {
						return
					}
					if _, err := io.ReadFull(c, make([]byte, int(dl[0])+2)); err != nil {
						return
					}
				case 0x04:
					if _, err := io.ReadFull(c, make([]byte, 18)); err != nil {
						return
					}
				default:
					return
				}

				up, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", targetPort), 2*time.Second)
				if err != nil {
					_, _ = c.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
					return
				}
				defer up.Close()

				reply := []byte{0x05, 0x00, 0x00, 0x01, 127, 0, 0, 1, 0, 0}
				binary.BigEndian.PutUint16(reply[8:10], uint16(targetPort))
				if _, err := c.Write(reply); err != nil {
					return
				}

				br := bufio.NewReader(c)
				httpReq, err := http.ReadRequest(br)
				if err != nil {
					return
				}
				httpReq.Header.Set("X-Node-Tag", tag)
				if err := httpReq.Write(up); err != nil {
					return
				}
				_, _ = io.Copy(c, up)
			}(conn)
		}
	}()

	return ln.Addr().(*net.TCPAddr).Port, stop
}
