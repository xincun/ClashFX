package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/clashfx/core-engine/internal/ailock"
	"github.com/clashfx/core-engine/internal/features"
	"github.com/clashfx/core-engine/internal/profile"
	"github.com/clashfx/core-engine/internal/transaction"
)

func TestDaemonNamedPipeIPCSubscribeActionsAndDiagnostics(t *testing.T) {
	coreExe := strings.TrimSpace(os.Getenv("CLASHFX_CORE_EXE"))
	if coreExe == "" {
		t.Skip("CLASHFX_CORE_EXE not set; skipping live daemon IPC integration test")
	}

	// Serve a valid Clash YAML subscription for clashfx://install-config URL import test
	subYAML := `mixed-port: 17996
proxies:
  - name: Tokyo-01
    type: socks5
    server: 127.0.0.1
    port: 10801
  - name: Seattle-02
    type: socks5
    server: 127.0.0.1
    port: 10802
proxy-groups:
  - name: Proxy
    type: select
    proxies: [Tokyo-01, Seattle-02, DIRECT]
rules:
  - MATCH,Proxy
`
	subSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(subYAML))
	}))
	defer subSrv.Close()

	sockDir, err := os.MkdirTemp("", "cfx-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sockDir)
	endpoint := filepath.Join(sockDir, "d.sock")
	if runtime.GOOS == "windows" {
		endpoint = fmt.Sprintf(`\\.\pipe\ClashFXDaemonTest-%d`, time.Now().UnixNano())
	}

	appData := t.TempDir()
	extDashboardDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(extDashboardDir, "index.html"), []byte("<html>ClashFX Dashboard Ready</html>"), 0600); err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(endpoint, Config{
		AppDataDir:     appData,
		DashboardUIDir: extDashboardDir,
		BinDir:         filepath.Dir(coreExe),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	conn, err := DialIPC(endpoint)
	if err != nil {
		t.Fatalf("DialIPC failed: %v", err)
	}
	defer conn.Close()

	reader := bufio.NewReaderSize(conn, 256*1024)
	callRPC := func(id int64, method string, params any) RPCResponse {
		t.Helper()
		var rawParams json.RawMessage
		if params != nil {
			b, _ := json.Marshal(params)
			rawParams = b
		}
		frame, _ := json.Marshal(RPCRequest{ID: id, Method: method, Params: rawParams})
		frame = append(frame, '\n')
		if _, wErr := conn.Write(frame); wErr != nil {
			t.Fatalf("write RPC %s: %v", method, wErr)
		}
		for {
			_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
			line, rErr := reader.ReadBytes('\n')
			if rErr != nil {
				t.Fatalf("read RPC %s reply: %v", method, rErr)
			}
			// Skip async push events while waiting for our matching RPC response ID
			var probe map[string]any
			_ = json.Unmarshal(line, &probe)
			if probe["event"] == "state_changed" {
				continue
			}
			var resp RPCResponse
			if err := json.Unmarshal(line, &resp); err != nil {
				t.Fatalf("decode RPC %s reply: %v", method, err)
			}
			if resp.ID == id {
				return resp
			}
		}
	}

	// 1. Subscribe and verify core running
	subResp := callRPC(1, "subscribe", nil)
	if !subResp.OK {
		t.Fatalf("subscribe failed: %s", subResp.Error)
	}

	// 2. Import clashfx:// URL scheme -> fetches subscription, selects profile, reloads live core!
	schemeURI := "clashfx://install-config?url=" + url.QueryEscape(subSrv.URL) + "&name=imported_sub"
	impResp := callRPC(2, "import_url_scheme", map[string]string{"uri": schemeURI})
	if !impResp.OK {
		t.Fatalf("import_url_scheme failed: %s", impResp.Error)
	}

	// 3. Select proxy Seattle-02 in Proxy group and verify snapshot reflects it
	selResp := callRPC(3, "select_proxy", map[string]string{"group": "Proxy", "target": "Seattle-02"})
	if !selResp.OK {
		t.Fatalf("select_proxy failed: %s", selResp.Error)
	}
	rawSnap, _ := json.Marshal(selResp.Data)
	var snap DaemonSnapshot
	_ = json.Unmarshal(rawSnap, &snap)
	if !snap.CoreRunning || snap.Settings.SelectedProfile != "imported_sub" {
		t.Fatalf("unexpected snapshot after import+select: coreRunning=%v profile=%s", snap.CoreRunning, snap.Settings.SelectedProfile)
	}
	foundGroup := false
	for _, g := range snap.ProxyGroups {
		if g.Name == "Proxy" && g.Now == "Seattle-02" && g.HasManualOverride {
			foundGroup = true
		}
	}
	if !foundGroup {
		t.Fatalf("expected Proxy group Now=Seattle-02 HasManualOverride=true, got %+v", snap.ProxyGroups)
	}

	// Verify external DashboardUIDir outside AppDataDir is allowed via SAFE_PATHS after hot reload
	uiResp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/ui/", snap.Runtime.APIPort))
	if err != nil {
		t.Fatalf("GET /ui/ failed: %v", err)
	}
	defer uiResp.Body.Close()
	if uiResp.StatusCode != http.StatusOK {
		t.Fatalf("expected HTTP 200 on /ui/, got %d", uiResp.StatusCode)
	}

	// 4. Single-node diagnostic probe (Seattle-02 points to closed 10802 -> structured error!)
	diagNodeResp := callRPC(4, "diagnose_node", map[string]string{
		"node_name":     "Seattle-02",
		"benchmark_url": subSrv.URL,
	})
	if !diagNodeResp.OK {
		t.Fatalf("diagnose_node failed: %s", diagNodeResp.Error)
	}

	// 5. Redacted Copy Diagnostic Info
	diagBundle := callRPC(5, "get_diagnostic_info", nil)
	if !diagBundle.OK {
		t.Fatalf("get_diagnostic_info failed: %s", diagBundle.Error)
	}
	bundleStr := fmt.Sprint(diagBundle.Data)
	if !strings.Contains(bundleStr, "ClashFX ") || !strings.Contains(bundleStr, "Diagnostic Report") {
		t.Fatalf("unexpected diagnostic bundle: %s", bundleStr)
	}

	// 6. Profile Mixin visual save + live effective rules inspection via Mihomo REST API
	saveMixinResp := callRPC(6, "save_profile_mixin", map[string]any{
		"mode":          "visual",
		"bypass_cn_web": false,
		"bypass_lan":    false,
		"rules": []map[string]any{
			{
				"type":    "DOMAIN-SUFFIX",
				"payload": "https://scholar.org/search",
				"target":  "PROXY",
			},
			{
				"type":    "IP-CIDR",
				"payload": "10.99.0.1",
				"target":  "DIRECT",
			},
		},
	})
	if !saveMixinResp.OK {
		t.Fatalf("save_profile_mixin failed: %s", saveMixinResp.Error)
	}
	effResp := callRPC(7, "get_effective_rules", nil)
	if !effResp.OK {
		t.Fatalf("get_effective_rules failed: %s", effResp.Error)
	}
	rawEff, _ := json.Marshal(effResp.Data)
	var effView EffectiveRulesView
	_ = json.Unmarshal(rawEff, &effView)
	if effView.DefaultTarget != "Proxy" || len(effView.Rules) < 2 {
		t.Fatalf("unexpected effective rules view: %+v", effView)
	}
	if effView.Rules[0].Payload != "scholar.org" || effView.Rules[0].Proxy != "Proxy" || effView.Rules[0].Source != "mixin" {
		t.Fatalf("expected mixin rule #0 -> Proxy, got %+v", effView.Rules[0])
	}

	// 7. Live top-level rule disable/enable (PATCH /rules/disable) + editable ruleset save/reset
	disResp := callRPC(8, "set_rule_disabled", map[string]any{
		"indices":  []int{0},
		"disabled": true,
	})
	if !disResp.OK {
		t.Fatalf("set_rule_disabled(true) failed: %s", disResp.Error)
	}
	var disView EffectiveRulesView
	rawDis, _ := json.Marshal(disResp.Data)
	_ = json.Unmarshal(rawDis, &disView)
	if !disView.Rules[0].Disabled {
		t.Fatalf("expected rule #0 Disabled=true, got %+v", disView.Rules[0])
	}

	enResp := callRPC(9, "set_rule_disabled", map[string]any{
		"indices":  []int{0},
		"disabled": false,
	})
	if !enResp.OK {
		t.Fatalf("set_rule_disabled(false) failed: %s", enResp.Error)
	}

	badProvResp := callRPC(10, "save_rule_provider", map[string]string{
		"provider_name": "clashfx-ai-lock",
		"content":       "DOMAIN-SUFFIX,perplexity.ai,DIRECT\n",
	})
	if badProvResp.OK {
		t.Fatal("expected save_rule_provider with trailing ,DIRECT target to fail validation")
	}

	goodProvResp := callRPC(11, "save_rule_provider", map[string]string{
		"provider_name": "clashfx-ai-lock",
		"content":       "# custom test\nDOMAIN-SUFFIX,perplexity.ai\nDOMAIN-SUFFIX,claude.ai\n",
	})
	if !goodProvResp.OK {
		t.Fatalf("save_rule_provider failed: %s", goodProvResp.Error)
	}
	var provView EffectiveRulesView
	rawProv, _ := json.Marshal(goodProvResp.Data)
	_ = json.Unmarshal(rawProv, &provView)
	if provView.RuleProviders["clashfx-ai-lock"].RuleCount != 2 || !provView.RuleProviders["clashfx-ai-lock"].Editable {
		t.Fatalf("unexpected clashfx-ai-lock provider after save: %+v", provView.RuleProviders["clashfx-ai-lock"])
	}

	resetProvResp := callRPC(12, "reset_rule_provider", map[string]string{
		"provider_name": "clashfx-ai-lock",
	})
	if !resetProvResp.OK {
		t.Fatalf("reset_rule_provider failed: %s", resetProvResp.Error)
	}
	var resetView EffectiveRulesView
	rawReset, _ := json.Marshal(resetProvResp.Data)
	_ = json.Unmarshal(rawReset, &resetView)
	if resetView.RuleProviders["clashfx-ai-lock"].RuleCount != 16 {
		t.Fatalf("expected 16 default rules after reset, got %d", resetView.RuleProviders["clashfx-ai-lock"].RuleCount)
	}
}

func TestFetchProxyGroupsHiddenFilterTimeoutAndProviderQuota(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/proxies", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"proxies": map[string]any{
				"Proxy": map[string]any{
					"name":    "Proxy",
					"type":    "Selector",
					"now":     "NodeOK",
					"hidden":  false,
					"testUrl": "https://www.gstatic.com/generate_204",
					"all":     []string{"NodeOK", "NodeTimeout", "NodeUntested"},
				},
				"InternalHelper": map[string]any{
					"name":   "InternalHelper",
					"type":   "URLTest",
					"now":    "NodeOK",
					"hidden": true,
					"all":    []string{"NodeOK"},
				},
				"GLOBAL": map[string]any{
					"name":   "GLOBAL",
					"type":   "Selector",
					"now":    "DIRECT",
					"hidden": false,
					"all":    []string{"DIRECT", "Proxy"},
				},
				"NodeOK": map[string]any{
					"name":    "NodeOK",
					"type":    "Shadowsocks",
					"history": []map[string]any{{"delay": 88}},
				},
				"NodeTimeout": map[string]any{
					"name":    "NodeTimeout",
					"type":    "Shadowsocks",
					"history": []map[string]any{{"delay": 0}},
				},
				"NodeUntested": map[string]any{
					"name":    "NodeUntested",
					"type":    "Shadowsocks",
					"history": []map[string]any{},
				},
			},
		})
	})
	mux.HandleFunc("/providers/proxies", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"providers": map[string]any{
				"default": map[string]any{
					"vehicleType": "Compatible",
				},
				"airport": map[string]any{
					"vehicleType": "HTTP",
					"subscriptionInfo": map[string]any{
						"Upload":   int64(100),
						"Download": int64(200),
						"Total":    int64(1000),
						"Expire":   int64(1893456000),
					},
				},
			},
		})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port

	groups := fetchProxyGroupsFromCore(context.Background(), port, "", nil, map[string]string{"Proxy": "NodeOK"})
	if len(groups) != 2 {
		t.Fatalf("expected 2 non-hidden groups (Proxy + GLOBAL), got %d: %+v", len(groups), groups)
	}
	if groups[0].Name != "Proxy" || groups[0].TestURL != "https://www.gstatic.com/generate_204" || !groups[0].HasManualOverride {
		t.Fatalf("unexpected Proxy group metadata: %+v", groups[0])
	}
	items := groups[0].Items
	if len(items) != 3 || !items[0].Tested || items[0].DelayMs != 88 || !items[1].Tested || items[1].DelayMs != 0 || items[2].Tested {
		t.Fatalf("unexpected item delay/tested flags: %+v", items)
	}

	provInfo := fetchProviderSubscriptionInfo(context.Background(), port, "")
	if provInfo == nil || *provInfo.Upload != 100 || *provInfo.Download != 200 || *provInfo.Total != 1000 || *provInfo.Expire != 1893456000 {
		t.Fatalf("unexpected provider subscription info: %+v", provInfo)
	}
}

func TestLoadProviderSnapshotsFromDiskAndUninstallCleanup(t *testing.T) {
	appData := t.TempDir()
	eng, err := NewEngine(Config{AppDataDir: appData}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Shutdown()

	cfgDir := eng.store.ActiveConfigDir()
	provDir := filepath.Join(cfgDir, "proxies")
	if err := os.MkdirAll(provDir, 0700); err != nil {
		t.Fatal(err)
	}
	provYAML := `proxies:
  - name: ProviderNode-JP
    type: socks5
    server: 198.51.100.77
    port: 1080
`
	if err := os.WriteFile(filepath.Join(provDir, "airport.yaml"), []byte(provYAML), 0600); err != nil {
		t.Fatal(err)
	}

	rawRoot := map[string]any{
		"proxy-providers": map[string]any{
			"airport": map[string]any{
				"type": "http",
				"path": "./proxies/airport.yaml",
			},
		},
		"proxy-groups": []any{
			map[string]any{
				"name": "Proxy",
				"type": "select",
				"use":  []any{"airport"},
			},
		},
		"rules": []any{"MATCH,Proxy"},
	}

	snaps := eng.loadProviderSnapshots(rawRoot)
	if len(snaps) != 1 || snaps[0].Name != "airport" || len(snaps[0].Proxies) != 1 {
		t.Fatalf("expected 1 provider snapshot with 1 proxy, got %+v", snaps)
	}

	if rpcTimeoutForMethod("benchmark_all") < 120*time.Second || rpcTimeoutForMethod("update_geo_databases") < 120*time.Second {
		t.Fatal("expected >=120s RPC timeout for heavy network methods")
	}

	if err := CleanupUninstallState(appData); err != nil {
		t.Fatalf("CleanupUninstallState failed: %v", err)
	}
}

type noopApplier struct{}

func (noopApplier) ValidateConfig(_, _ string) error { return nil }
func (noopApplier) ApplyRuntimeConfig(_ context.Context, _ profile.AppSettings, _, _ string, _ int, _ string) error {
	return nil
}

func TestControllerHooksWithProxyProvidersNoDeadlockAndMultiNodeParity(t *testing.T) {
	appData := t.TempDir()
	eng, err := NewEngine(Config{AppDataDir: appData}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Shutdown()

	// Replace applier with noopApplier so this regression test always runs even without CLASHFX_CORE_EXE
	eng.ctrl = transaction.NewController(
		eng.store,
		noopApplier{},
		"",
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
		eng.nodeStore.TransformHookForControllerWithProviderProxies(eng.IsCoreRunning, eng.loadProviderProxiesForNodeServices),
	)

	cfgDir := eng.store.ActiveConfigDir()
	provDir := filepath.Join(cfgDir, "providers")
	if err := os.MkdirAll(provDir, 0700); err != nil {
		t.Fatal(err)
	}
	provYAML := `proxies:
  - name: "US-Claude-Dedicated"
    type: socks5
    server: 198.51.100.11
    port: 1080
  - name: "JP-OpenAI-Pro"
    type: socks5
    server: 198.51.100.12
    port: 1080
`
	if err := os.WriteFile(filepath.Join(provDir, "demo-airport.yaml"), []byte(provYAML), 0600); err != nil {
		t.Fatal(err)
	}
	profYAML := `mixed-port: 17890
proxies:
  - name: "HK-Fast-Inline"
    type: socks5
    server: 198.51.100.13
    port: 1080
proxy-providers:
  demo-airport:
    type: file
    path: ./providers/demo-airport.yaml
proxy-groups:
  - name: "Proxy"
    type: select
    use:
      - demo-airport
    proxies:
      - "HK-Fast-Inline"
      - "DIRECT"
rules:
  - MATCH,Proxy
`
	if err := os.WriteFile(filepath.Join(cfgDir, "demo-airport.yaml"), []byte(profYAML), 0600); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		if err := eng.SelectProfile(context.Background(), "demo-airport"); err != nil {
			done <- fmt.Errorf("SelectProfile: %w", err)
			return
		}
		if err := eng.SetAILock(context.Background(), true, "US-Claude-Dedicated", false); err != nil {
			done <- fmt.Errorf("SetAILock: %w", err)
			return
		}
		snap := eng.Snapshot()
		if len(snap.NodeServices.Rows) != 3 {
			done <- fmt.Errorf("expected 3 multi-node rows (1 inline + 2 provider), got %d: %+v", len(snap.NodeServices.Rows), snap.NodeServices.Rows)
			return
		}
		rawRt, rErr := os.ReadFile(eng.ctrl.RuntimeConfigPath())
		if rErr != nil {
			done <- rErr
			return
		}
		if !strings.Contains(string(rawRt), "clashfx-node-service-target-") {
			done <- fmt.Errorf("expected hidden target wrapper group for non-materialized provider listener in runtime config:\n%s", string(rawRt))
			return
		}
		done <- nil
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("DEADLOCK: SelectProfile + SetAILock + Snapshot timed out on proxy-providers profile")
	}
}

type uacSpyApplier struct {
	engine       *Engine
	called       bool
	seenAllowUAC bool
}

func (a *uacSpyApplier) ValidateConfig(_, _ string) error { return nil }
func (a *uacSpyApplier) ApplyRuntimeConfig(_ context.Context, _ profile.AppSettings, _, _ string, _ int, _ string) error {
	a.called = true
	a.seenAllowUAC = a.engine.allowPromptUAC
	return nil
}

func TestReloadConfigParsesAllowUACAndDisableAILockDowngradesEnhanced(t *testing.T) {
	appData := t.TempDir()
	t.Setenv("CLASHFX_SERVICE_SOCK", filepath.Join(appData, "nonexistent-service.sock"))

	eng, err := NewEngine(Config{AppDataDir: appData}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Shutdown()

	spy := &uacSpyApplier{engine: eng}
	eng.ctrl = transaction.NewController(eng.store, spy, "")

	srv := &Server{engine: eng, clients: map[*clientConn]struct{}{}}
	resp := srv.dispatch(&clientConn{}, RPCRequest{
		ID:     1,
		Method: "reload_config",
		Params: json.RawMessage(`{"allow_uac":true}`),
	})
	if !resp.OK {
		t.Fatalf("reload_config dispatch failed: %s", resp.Error)
	}
	if !spy.called || !spy.seenAllowUAC {
		t.Fatalf("expected reload_config to propagate allow_uac=true (called=%v seen=%v)", spy.called, spy.seenAllowUAC)
	}

	if _, err := eng.store.UpdateSettings(func(s *profile.AppSettings) error {
		s.AILockEnabled = true
		s.AILockTarget = "DIRECT"
		s.EnhancedMode = true
		s.AILockSavedState = nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := eng.SetAILock(context.Background(), false, "", false); err != nil {
		t.Fatalf("SetAILock(false) failed without privileged service: %v", err)
	}
	st := eng.store.Settings()
	if st.AILockEnabled {
		t.Fatalf("expected AILockEnabled=false after disable")
	}
	if supportsPrivilegedService() && st.EnhancedMode {
		t.Fatalf("expected EnhancedMode downgraded to false when disabling AI lock without privileged service")
	}
}

func TestConcurrentReadRPCsOverIPCCachedNetworkSignature(t *testing.T) {
	sockDir, err := os.MkdirTemp("", "cfx-rpc-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sockDir)

	appData := t.TempDir()
	t.Setenv("CLASHFX_SERVICE_SOCK", filepath.Join(appData, "nonexistent-service.sock"))
	t.Setenv("CLASHFX_DISABLE_LAUNCH_AGENT", "1")

	endpoint := filepath.Join(sockDir, "d.sock")
	if runtime.GOOS == "windows" {
		endpoint = fmt.Sprintf(`\\.\pipe\ClashFXDaemonConcurrentTest-%d`, time.Now().UnixNano())
	}

	ln, err := ListenIPC(endpoint)
	if err != nil {
		t.Fatalf("ListenIPC failed: %v", err)
	}
	eng, err := NewEngine(Config{AppDataDir: appData}, nil)
	if err != nil {
		_ = ln.Close()
		t.Fatal(err)
	}
	defer eng.Shutdown()

	srv := &Server{
		endpoint: endpoint,
		engine:   eng,
		ln:       ln,
		clients:  map[*clientConn]struct{}{},
		quitCh:   make(chan struct{}),
	}
	go srv.acceptLoop()
	defer func() {
		_ = ln.Close()
		srv.clientsMu.Lock()
		for c := range srv.clients {
			_ = c.conn.Close()
		}
		srv.clients = map[*clientConn]struct{}{}
		srv.clientsMu.Unlock()
	}()

	// Warm the network signature cache once before timing concurrent calls.
	_ = eng.Snapshot()

	methods := []string{"get_state", "get_profile_mixin", "get_effective_rules", "get_diagnostic_info"}
	const workers = 8
	const iters = 3

	start := time.Now()
	var wg sync.WaitGroup
	errCh := make(chan error, workers*iters*len(methods))

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for iter := 0; iter < iters; iter++ {
				for idx, method := range methods {
					conn, dErr := DialIPC(endpoint)
					if dErr != nil {
						errCh <- fmt.Errorf("w=%d dial %s: %w", workerID, method, dErr)
						continue
					}
					reqID := int64(workerID*100 + iter*10 + idx + 1)
					frame, _ := json.Marshal(RPCRequest{ID: reqID, Method: method})
					frame = append(frame, '\n')
					_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
					if _, wErr := conn.Write(frame); wErr != nil {
						_ = conn.Close()
						errCh <- fmt.Errorf("w=%d write %s: %w", workerID, method, wErr)
						continue
					}
					_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
					line, rErr := bufio.NewReader(conn).ReadBytes('\n')
					_ = conn.Close()
					if rErr != nil {
						errCh <- fmt.Errorf("w=%d read %s: %w", workerID, method, rErr)
						continue
					}
					var resp RPCResponse
					if jErr := json.Unmarshal(line, &resp); jErr != nil || !resp.OK || resp.ID != reqID {
						errCh <- fmt.Errorf("w=%d bad resp %s: err=%v resp=%+v", workerID, method, jErr, resp)
					}
				}
			}
		}(w)
	}

	wg.Wait()
	close(errCh)
	for e := range errCh {
		t.Fatal(e)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("concurrent IPC RPC batch took unexpectedly long: %v", elapsed)
	}
}

func TestSingleConnectionConcurrentRPCNoHeadOfLineBlock(t *testing.T) {
	sockDir, err := os.MkdirTemp("", "cfx-hol-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sockDir)

	appData := t.TempDir()
	t.Setenv("CLASHFX_SERVICE_SOCK", filepath.Join(appData, "nonexistent-service.sock"))
	t.Setenv("CLASHFX_DISABLE_LAUNCH_AGENT", "1")

	slowSubSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(350 * time.Millisecond)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer slowSubSrv.Close()

	endpoint := filepath.Join(sockDir, "d.sock")
	if runtime.GOOS == "windows" {
		endpoint = fmt.Sprintf(`\\.\pipe\ClashFXDaemonHoLTest-%d`, time.Now().UnixNano())
	}

	ln, err := ListenIPC(endpoint)
	if err != nil {
		t.Fatalf("ListenIPC failed: %v", err)
	}
	eng, err := NewEngine(Config{AppDataDir: appData}, nil)
	if err != nil {
		_ = ln.Close()
		t.Fatal(err)
	}
	defer eng.Shutdown()

	srv := &Server{
		endpoint: endpoint,
		engine:   eng,
		ln:       ln,
		clients:  map[*clientConn]struct{}{},
		quitCh:   make(chan struct{}),
	}
	go srv.acceptLoop()
	defer func() {
		_ = ln.Close()
		srv.clientsMu.Lock()
		for c := range srv.clients {
			_ = c.conn.Close()
		}
		srv.clients = map[*clientConn]struct{}{}
		srv.clientsMu.Unlock()
	}()

	conn, err := DialIPC(endpoint)
	if err != nil {
		t.Fatalf("DialIPC failed: %v", err)
	}
	defer conn.Close()

	slowSchemeURI := "clash-verge://install-config?url=" + url.QueryEscape(slowSubSrv.URL)
	reqs := []RPCRequest{
		{ID: 1, Method: "import_url_scheme", Params: json.RawMessage(fmt.Sprintf(`{"uri":%q}`, slowSchemeURI))},
		{ID: 2, Method: "ping"},
		{ID: 3, Method: "get_state"},
		{ID: 4, Method: "get_profile_mixin"},
	}
	var buf bytes.Buffer
	for _, r := range reqs {
		line, _ := json.Marshal(r)
		buf.Write(line)
		buf.WriteByte('\n')
	}
	_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write(buf.Bytes()); err != nil {
		t.Fatalf("pipelined write failed: %v", err)
	}

	reader := bufio.NewReader(conn)
	order := make([]int64, 0, len(reqs))
	resps := map[int64]RPCResponse{}
	for len(resps) < len(reqs) {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		line, rErr := reader.ReadBytes('\n')
		if rErr != nil {
			t.Fatalf("read reply failed: %v", rErr)
		}
		var resp RPCResponse
		if jErr := json.Unmarshal(line, &resp); jErr != nil {
			t.Fatalf("unmarshal reply: %v", jErr)
		}
		order = append(order, resp.ID)
		resps[resp.ID] = resp
	}

	if order[len(order)-1] != 1 {
		t.Fatalf("expected fast RPCs (2,3,4) to complete BEFORE slow RPC 1 on same connection, got order %v", order)
	}
	for _, id := range []int64{2, 3, 4} {
		if !resps[id].OK {
			t.Fatalf("fast RPC id=%d failed: %+v", id, resps[id])
		}
	}
	// Slow import hit HTTP 500 rather than "unsupported scheme", verifying clash-verge:// scheme is accepted.
	if strings.Contains(resps[1].Error, "unsupported scheme") {
		t.Fatalf("expected clash-verge:// scheme accepted, got %q", resps[1].Error)
	}
}

func TestUpdateSettingsFrontendPayloadAndLaunchAtLogin(t *testing.T) {
	sockDir, err := os.MkdirTemp("", "cfx-updset-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sockDir)

	appData := t.TempDir()
	t.Setenv("CLASHFX_SERVICE_SOCK", filepath.Join(appData, "nonexistent-service.sock"))
	t.Setenv("CLASHFX_DISABLE_LAUNCH_AGENT", "1")

	endpoint := filepath.Join(sockDir, "d.sock")
	if runtime.GOOS == "windows" {
		endpoint = fmt.Sprintf(`\\.\pipe\ClashFXDaemonSettingsTest-%d`, time.Now().UnixNano())
	}

	ln, err := ListenIPC(endpoint)
	if err != nil {
		t.Fatalf("ListenIPC failed: %v", err)
	}
	eng, err := NewEngine(Config{AppDataDir: appData}, nil)
	if err != nil {
		_ = ln.Close()
		t.Fatal(err)
	}
	defer eng.Shutdown()

	srv := &Server{
		endpoint: endpoint,
		engine:   eng,
		ln:       ln,
		clients:  map[*clientConn]struct{}{},
		quitCh:   make(chan struct{}),
	}
	go srv.acceptLoop()
	defer func() {
		_ = ln.Close()
		srv.clientsMu.Lock()
		for c := range srv.clients {
			_ = c.conn.Close()
		}
		srv.clients = map[*clientConn]struct{}{}
		srv.clientsMu.Unlock()
	}()

	conn, err := DialIPC(endpoint)
	if err != nil {
		t.Fatalf("DialIPC failed: %v", err)
	}
	defer conn.Close()

	// 1) Fast-path desktop settings update (must succeed with zero core restart / validation).
	cur := eng.store.Settings()
	cur.LaunchAtLogin = true
	cur.TrayIconPreset = "monochrome"
	cur.Language = "en_US"
	cur.AutoUpdateSubs = false
	cur.UpdateChannel = "lab"
	cur.BenchmarkURL = "https://example.com/generate_204"
	cur.Hotkeys = profile.HotkeySettings{
		ToggleSystemProxy:  "Cmd+Shift+P",
		SwitchOutboundMode: "Cmd+Shift+M",
	}
	fastPayload, _ := json.Marshal(map[string]any{"settings": cur})
	fastFrame, _ := json.Marshal(RPCRequest{
		ID:     41,
		Method: "update_settings",
		Params: json.RawMessage(fastPayload),
	})
	fastFrame = append(fastFrame, '\n')
	_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write(fastFrame); err != nil {
		t.Fatalf("write fast update_settings failed: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	fastLine, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read fast update_settings reply failed: %v", err)
	}
	var fastResp RPCResponse
	if err := json.Unmarshal(fastLine, &fastResp); err != nil {
		t.Fatalf("unmarshal fast update_settings reply: %v", err)
	}
	if !fastResp.OK {
		t.Fatalf("expected desktop-only update_settings fast path OK, got error=%q code=%q", fastResp.Error, fastResp.ErrorCode)
	}

	// 2) Full frontend payload including string tun_route_excludes (must never fail JSON unmarshal).
	frontendSettings := `{
		"settings": {
			"selected_profile": "config",
			"outbound_mode": "rule",
			"allow_lan": false,
			"ipv6": false,
			"system_proxy_enabled": false,
			"enhanced_mode": false,
			"port_override": 0,
			"api_port": 19090,
			"log_level": "info",
			"tun_mtu": 1500,
			"tun_stack": "mixed",
			"tun_route_excludes": "192.168.0.0/16, 10.0.0.0/8",
			"proxy_bypass_list": "localhost,127.0.0.1",
			"auto_update_subs": true,
			"tray_icon_preset": "monochrome",
			"language": "zh_CN",
			"launch_at_login": true,
			"hotkeys": {"toggle_system_proxy": "Cmd+Shift+P", "switch_outbound_mode": "Cmd+Shift+M"},
			"suspend_networks": ["CorpWiFi"],
			"update_channel": "stable",
			"benchmark_url": "https://cp.cloudflare.com/generate_204"
		}
	}`
	frame, _ := json.Marshal(RPCRequest{
		ID:     42,
		Method: "update_settings",
		Params: json.RawMessage(frontendSettings),
	})
	frame = append(frame, '\n')
	_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write(frame); err != nil {
		t.Fatalf("write update_settings failed: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read update_settings reply failed: %v", err)
	}
	var resp RPCResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatalf("unmarshal update_settings reply: %v", err)
	}
	if strings.Contains(resp.Error, "unmarshal") {
		t.Fatalf("unexpected JSON unmarshal error on frontend settings payload: %q", resp.Error)
	}

	got := eng.store.Settings()
	if got.TunRouteExcludes != "192.168.0.0/16, 10.0.0.0/8" {
		t.Fatalf("unexpected tun_route_excludes: %q", got.TunRouteExcludes)
	}
	if !got.LaunchAtLogin {
		t.Fatalf("expected launch_at_login=true")
	}
	if got.TrayIconPreset != "monochrome" {
		t.Fatalf("expected tray_icon_preset=monochrome, got %q", got.TrayIconPreset)
	}
	if len(got.SuspendNetworks) != 1 || got.SuspendNetworks[0] != "CorpWiFi" {
		t.Fatalf("unexpected suspend_networks: %+v", got.SuspendNetworks)
	}

	if runtime.GOOS == "darwin" {
		tempHome := t.TempDir()
		t.Setenv("HOME", tempHome)
		t.Setenv("CLASHFX_DISABLE_LAUNCH_AGENT", "")
		t.Setenv("CLASHFX_TEST_REAL_LAUNCH_AGENT", "1")

		if err := SetLaunchAtLogin(true, "/Applications/ClashFX.app/Contents/MacOS/ClashFX"); err != nil {
			t.Fatalf("SetLaunchAtLogin(true) failed: %v", err)
		}
		plistPath := filepath.Join(tempHome, "Library", "LaunchAgents", "com.clashfx.app.login.plist")
		raw, err := os.ReadFile(plistPath)
		if err != nil {
			t.Fatalf("read LaunchAgent plist failed: %v", err)
		}
		if !strings.Contains(string(raw), "<string>--minimized</string>") {
			t.Fatalf("expected --minimized in plist, got:\n%s", string(raw))
		}
		if err := SetLaunchAtLogin(false, ""); err != nil {
			t.Fatalf("SetLaunchAtLogin(false) failed: %v", err)
		}
		if _, err := os.Stat(plistPath); !os.IsNotExist(err) {
			t.Fatalf("expected plist removed on disable, stat err=%v", err)
		}
	}
}
