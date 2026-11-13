package ailock

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/clashfx/core-engine/internal/profile"
	"github.com/clashfx/core-engine/internal/service"
	"github.com/clashfx/core-engine/internal/subscription"
	"github.com/clashfx/core-engine/internal/transaction"
)

func TestWindowsProcessAndPathMatchersAndDependencySafety(t *testing.T) {
	nameRe, err := regexp.Compile(ProcessNamePattern)
	if err != nil {
		t.Fatalf("compile process name regex %q: %v", ProcessNamePattern, err)
	}
	pathRe, err := regexp.Compile(WindowsProcessPathPattern)
	if err != nil {
		t.Fatalf("compile process path regex %q: %v", WindowsProcessPathPattern, err)
	}

	mustMatchName := []string{
		"Claude.exe", "claude.exe", "Claude", "Claude Helper (Renderer)",
		"codex.exe", "codex-app-server.exe", "ChatGPT.exe",
		"Antigravity.exe", "antigravity-cli.exe", "agy.exe",
	}
	for _, exe := range mustMatchName {
		if !nameRe.MatchString(exe) {
			t.Fatalf("expected AI executable %q to match PROCESS-NAME-REGEX", exe)
		}
	}
	for _, nonAI := range []string{"chrome.exe", "explorer.exe", "mihomo_core.exe", "powershell.exe", "node.exe"} {
		if nameRe.MatchString(nonAI) {
			t.Fatalf("non-AI executable %q unexpectedly matched %s", nonAI, nameRe.String())
		}
	}

	mustMatchPath := []string{
		`C:\Users\devtest\AppData\Local\Programs\Claude\Claude.exe`,
		`C:\Users\devtest\AppData\Local\AnthropicClaude\app-0.8.1\claude.exe`,
		`C:\Program Files\Antigravity\Antigravity.exe`,
		`C:\Program Files (x86)\Codex\Codex.exe`,
		`C:\Program Files\WindowsApps\OpenAI.ChatGPT-Desktop_1.2025_arm64__2p2nqsd0c76g0\app\ChatGPT.exe`,
		`C:\Users\devtest\.local\bin\claude.exe`,
		`C:\Users\devtest\.local\share\claude\versions\2.1.0\claude.exe`,
		`C:\Users\devtest\.gemini\antigravity-cli\bin\agy.exe`,
		`C:\Users\devtest\.antigravity\bin\antigravity.exe`,
	}
	for _, p := range mustMatchPath {
		if !pathRe.MatchString(p) {
			t.Fatalf("expected Windows AI install path %q to match PROCESS-PATH-REGEX", p)
		}
	}
	for _, nonMatchPath := range []string{
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`/Applications/Claude.app/Contents/MacOS/Claude`,
	} {
		if pathRe.MatchString(nonMatchPath) {
			t.Fatalf("unexpected match for non-Windows/non-AI path %q", nonMatchPath)
		}
	}

	// Verify safe relay assignment vs cycle avoidance + concrete node filtering + inline rule-provider
	root := map[string]any{
		"proxies": []any{
			map[string]any{"name": "GeneralHK", "type": "socks5", "server": "127.0.0.1", "port": 1081},
			map[string]any{"name": "DedicatedUS", "type": "socks5", "server": "198.51.100.10", "port": 1082},
		},
		"proxy-groups": []any{
			map[string]any{"name": "Proxy", "type": "select", "proxies": []any{"GeneralHK"}},
		},
		"rules": []any{
			"RULE-SET,clashfx-internal-direct,DIRECT",
			"MATCH,Proxy",
		},
	}

	cands := ListCandidateTargets(root, nil)
	if len(cands) != 2 || cands[0].Name != "GeneralHK" || cands[1].Name != "DedicatedUS" {
		t.Fatalf("unexpected candidates: %+v", cands)
	}

	// Reject proxy group as target
	if out := Apply(root, "Proxy", nil); out.Applied {
		t.Fatal("expected proxy group target to be rejected")
	}

	// DedicatedUS is outside Proxy -> safe relay Proxy assigned as dialer-proxy!
	out := Apply(root, "DedicatedUS", nil)
	if !out.Applied || out.RelayProxy != "Proxy" || out.ServerHost != "198.51.100.10" {
		t.Fatalf("unexpected Apply outcome: %+v", out)
	}
	rules, _ := extractStringListStrict(root["rules"])
	wantRules := []string{
		"RULE-SET,clashfx-internal-direct,DIRECT",
		"RULE-SET,clashfx-ai-lock,DedicatedUS",
		"RULE-SET,clashfx-ai-lock,REJECT",
		"MATCH,Proxy",
	}
	if len(rules) != len(wantRules) {
		t.Fatalf("rules = %v, want %v", rules, wantRules)
	}
	for i := range wantRules {
		if rules[i] != wantRules[i] {
			t.Fatalf("rules[%d] = %q, want %q", i, rules[i], wantRules[i])
		}
	}
	rpMap, _ := toStringMap(root["rule-providers"])
	aiProv, _ := toStringMap(rpMap[InlineRuleProviderName])
	if aiProv["type"] != "inline" || aiProv["behavior"] != "classical" {
		t.Fatalf("unexpected inline rule-provider: %+v", aiProv)
	}
	payload, _ := aiProv["payload"].([]string)
	if len(payload) != 2+len(ProtectedDomains) {
		t.Fatalf("payload len = %d, want %d", len(payload), 2+len(ProtectedDomains))
	}

	// GeneralHK IS inside Proxy -> relay must NOT be assigned (would create dialer cycle!)
	cyclicRoot := map[string]any{
		"proxies": []any{
			map[string]any{"name": "GeneralHK", "type": "socks5", "server": "127.0.0.1", "port": 1081},
		},
		"proxy-groups": []any{
			map[string]any{"name": "Proxy", "type": "select", "proxies": []any{"GeneralHK"}},
		},
		"rules": []any{"MATCH,Proxy"},
	}
	outNoRelay := Apply(cyclicRoot, "GeneralHK", nil)
	if !outNoRelay.Applied || outNoRelay.RelayProxy != "" {
		t.Fatalf("expected Apply without cyclic relay, got %+v", outNoRelay)
	}
}

func TestRealMihomoCoreAILockRoutingAndFailClosed(t *testing.T) {
	coreExe := strings.TrimSpace(os.Getenv("CLASHFX_CORE_EXE"))
	if coreExe == "" {
		t.Skip("CLASHFX_CORE_EXE not set; skipping live mihomo_core AI Lock verification")
	}

	// Start a local HTTP echo server representing external web endpoints
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLn.Close()
	echoPort := echoLn.Addr().(*net.TCPAddr).Port

	var directHits atomic.Int32
	echoSrv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			via := r.Header.Get("X-Upstream-Tag")
			if via == "" {
				directHits.Add(1)
				via = "DIRECT_LEAK"
			}
			_, _ = fmt.Fprintf(w, "host=%s via=%s", r.Host, via)
		}),
	}
	go func() { _ = echoSrv.Serve(echoLn) }()
	defer echoSrv.Close()

	// Start two minimal SOCKS5 mock proxy nodes that rewrite target port -> echoPort
	// and inject X-Upstream-Tag into the forwarded HTTP request!
	generalPort, stopGeneral, generalHits := startTaggedSOCKS5Mock(t, echoPort, "GENERAL_NODE")
	defer stopGeneral()

	lockedPort, stopLocked, lockedHits := startTaggedSOCKS5Mock(t, echoPort, "LOCKED_AI_NODE")
	defer stopLocked()

	trustedDir := filepath.Dir(coreExe)
	runner := service.NewRunner(trustedDir)
	runner.SetCoreBinaryForTest(filepath.Base(coreExe), 12*time.Second, 6*time.Second)
	defer func() { _ = runner.Stop() }()

	helper := subscription.BinaryCoreHelper{CoreBinaryPath: coreExe}
	store, err := profile.NewStore(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	cfgYAML := fmt.Sprintf(`mixed-port: 17992
proxies:
  - name: GeneralNode
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
    proxies: [GeneralNode, LockedAINode]
rules:
  - MATCH,Proxy
`, generalPort, lockedPort)

	if err := profile.WriteFileAtomic(store.ProfilePath("config"), []byte(cfgYAML), 0600); err != nil {
		t.Fatal(err)
	}

	_, err = store.UpdateSettings(func(s *profile.AppSettings) error {
		s.PortOverride = 17992
		s.APIPort = 19292
		s.AILockEnabled = true
		s.AILockTarget = "LockedAINode"
		// Keep EnhancedMode false in user-space test so Wintun admin isn't required here
		s.OutboundMode = "global" // verify Apply forces mode: rule!
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	applier := &ailockRunnerApplier{helper: helper, runner: runner}
	ctrl := transaction.NewController(store, applier, "", TransformHookForController(nil))

	state, err := ctrl.Apply(context.Background(), false)
	if err != nil {
		t.Fatalf("Apply AI Lock config failed: %v", err)
	}

	proxyURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", state.MixedPort))
	client := &http.Client{
		Timeout: 4 * time.Second,
		Transport: &http.Transport{
			Proxy:             http.ProxyURL(proxyURL),
			DisableKeepAlives: true,
		},
	}

	// 1. Protected AI & Google domains MUST route through LockedAINode (NOT MATCH,DIRECT!)
	for _, aiHost := range []string{"api.anthropic.com", "chatgpt.com", "gemini.google.com", "generativelanguage.googleapis.com", "antigravity.google"} {
		body, reqErr := fetchViaProxy(client, fmt.Sprintf("http://%s/v1/test", aiHost))
		if reqErr != nil {
			t.Fatalf("AI host %s request failed while LockedAINode healthy: %v", aiHost, reqErr)
		}
		if !strings.Contains(body, "via=LOCKED_AI_NODE") {
			t.Fatalf("AI host %s routed unexpectedly: %q", aiHost, body)
		}
	}
	if lockedHits.Load() < 5 {
		t.Fatalf("expected >= 5 hits on LockedAINode, got %d", lockedHits.Load())
	}

	// 2. Unrelated domain (github.com) is NOT captured by AI Lock -> follows MATCH,Proxy (GeneralNode)!
	nonAIBody, err := fetchViaProxy(client, "http://github.com/explore")
	if err != nil || !strings.Contains(nonAIBody, "via=GENERAL_NODE") {
		t.Fatalf("expected github.com via GENERAL_NODE, got body=%q err=%v", nonAIBody, err)
	}

	// 3. Kill LockedAINode while GeneralNode (Proxy) remains healthy -> AI hosts MUST FAIL CLOSED!
	stopLocked()
	generalBefore := generalHits.Load()
	directBefore := directHits.Load()
	for _, aiHost := range []string{"claude.ai", "api.openai.com", "cloudcode-pa.googleapis.com"} {
		body, reqErr := fetchViaProxy(client, fmt.Sprintf("http://%s/v1/failclosed", aiHost))
		if reqErr == nil && strings.Contains(body, "via=") {
			t.Fatalf("SECURITY VIOLATION: %s leaked when LockedAINode was down: %q", aiHost, body)
		}
	}
	if generalHits.Load() != generalBefore || directHits.Load() != directBefore {
		t.Fatalf("SECURITY VIOLATION: AI traffic fell back (general %d->%d, direct %d->%d)!",
			generalBefore, generalHits.Load(), directBefore, directHits.Load())
	}
	// Verify general non-AI traffic STILL works while LockedAINode is down!
	stillOK, err := fetchViaProxy(client, "http://example.org/")
	if err != nil || !strings.Contains(stillOK, "via=GENERAL_NODE") {
		t.Fatalf("expected non-AI example.org to keep working via GENERAL_NODE, got %q (%v)", stillOK, err)
	}
}

type ailockRunnerApplier struct {
	helper subscription.BinaryCoreHelper
	runner *service.Runner
}

func (a *ailockRunnerApplier) ValidateConfig(homeDir, configPath string) error {
	return a.helper.ValidateConfig(homeDir, configPath)
}

func (a *ailockRunnerApplier) ApplyRuntimeConfig(ctx context.Context, _ profile.AppSettings, homeDir, runtimeConfigPath string, apiPort int, secret string) error {
	if a.runner.Status().Running {
		return transaction.ReloadViaREST(ctx, apiPort, secret, runtimeConfigPath)
	}
	_, err := a.runner.Start(homeDir, runtimeConfigPath, "")
	return err
}

func fetchViaProxy(client *http.Client, rawURL string) (string, error) {
	resp, err := client.Get(rawURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return string(b), nil
}

// startTaggedSOCKS5Mock starts a minimal RFC1928 no-auth SOCKS5 server that
// connects every inbound stream to 127.0.0.1:echoPort and injects X-Upstream-Tag.
func startTaggedSOCKS5Mock(t *testing.T, echoPort int, tag string) (int, func(), *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
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
				_ = c.SetDeadline(time.Now().Add(4 * time.Second))
				// Greeting: VER, NMETHODS, METHODS...
				hdr := make([]byte, 2)
				if _, err := io.ReadFull(c, hdr); err != nil || hdr[0] != 0x05 {
					return
				}
				methods := make([]byte, int(hdr[1]))
				if _, err := io.ReadFull(c, methods); err != nil {
					return
				}
				_, _ = c.Write([]byte{0x05, 0x00})

				// Request: VER, CMD, RSV, ATYP
				reqHdr := make([]byte, 4)
				if _, err := io.ReadFull(c, reqHdr); err != nil || reqHdr[1] != 0x01 {
					return
				}
				switch reqHdr[3] {
				case 0x01:
					if _, err := io.ReadFull(c, make([]byte, 4+2)); err != nil {
						return
					}
				case 0x03:
					dlen := make([]byte, 1)
					if _, err := io.ReadFull(c, dlen); err != nil {
						return
					}
					if _, err := io.ReadFull(c, make([]byte, int(dlen[0])+2)); err != nil {
						return
					}
				case 0x04:
					if _, err := io.ReadFull(c, make([]byte, 16+2)); err != nil {
						return
					}
				default:
					return
				}

				up, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", echoPort), 2*time.Second)
				if err != nil {
					_, _ = c.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
					return
				}
				defer up.Close()
				hits.Add(1)

				// Reply success
				reply := []byte{0x05, 0x00, 0x00, 0x01, 127, 0, 0, 1, 0, 0}
				binary.BigEndian.PutUint16(reply[8:10], uint16(echoPort))
				if _, err := c.Write(reply); err != nil {
					return
				}

				// Read HTTP request from client, add X-Upstream-Tag, forward to echo server
				br := bufio.NewReader(c)
				httpReq, err := http.ReadRequest(br)
				if err != nil {
					return
				}
				httpReq.Header.Set("X-Upstream-Tag", tag)
				if err := httpReq.Write(up); err != nil {
					return
				}
				_, _ = io.Copy(c, up)
			}(conn)
		}
	}()

	return ln.Addr().(*net.TCPAddr).Port, stop, &hits
}
