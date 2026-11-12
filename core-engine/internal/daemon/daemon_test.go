package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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

	endpoint := filepath.Join(t.TempDir(), "daemon-test.sock")
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
	if !strings.Contains(bundleStr, "ClashFX Windows Diagnostic Report") {
		t.Fatalf("unexpected diagnostic bundle: %s", bundleStr)
	}
}
