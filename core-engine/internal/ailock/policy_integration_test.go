package ailock_test

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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/clashfx/core-engine/internal/ailock"
	"github.com/clashfx/core-engine/internal/profile"
	"github.com/clashfx/core-engine/internal/service"
	"github.com/clashfx/core-engine/internal/subscription"
	"github.com/clashfx/core-engine/internal/transaction"
)

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
	ctrl := transaction.NewController(store, applier, "", ailock.TransformHookForController(nil))

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

	// 2. Unrelated domain (github.com & perplexity.ai) is NOT captured initially -> follows MATCH,Proxy (GeneralNode)!
	nonAIBody, err := fetchViaProxy(client, "http://github.com/explore")
	if err != nil || !strings.Contains(nonAIBody, "via=GENERAL_NODE") {
		t.Fatalf("expected github.com via GENERAL_NODE, got body=%q err=%v", nonAIBody, err)
	}
	perpBefore, err := fetchViaProxy(client, "http://perplexity.ai/search")
	if err != nil || !strings.Contains(perpBefore, "via=GENERAL_NODE") {
		t.Fatalf("expected perplexity.ai via GENERAL_NODE before hot-update, got body=%q err=%v", perpBefore, err)
	}

	// 2b. Zero-reload hot update of file-backed ruleset/clashfx-ai-lock.list via PUT /providers/rules/clashfx-ai-lock!
	rulesetPath := store.BuiltinRulesetPath(ailock.InlineRuleProviderName)
	origRulesetBytes, err := os.ReadFile(rulesetPath)
	if err != nil {
		t.Fatalf("read seeded clashfx-ai-lock.list: %v", err)
	}
	updatedRuleset := string(origRulesetBytes) + "\nDOMAIN-SUFFIX,perplexity.ai\n"
	if err := profile.WriteFileAtomic(rulesetPath, []byte(updatedRuleset), 0600); err != nil {
		t.Fatal(err)
	}
	putReq, _ := http.NewRequest(http.MethodPut, fmt.Sprintf("http://127.0.0.1:%d/providers/rules/%s", state.APIPort, ailock.InlineRuleProviderName), nil)
	putResp, err := http.DefaultClient.Do(putReq)
	if err != nil || (putResp.StatusCode != http.StatusNoContent && putResp.StatusCode != http.StatusOK) {
		t.Fatalf("PUT /providers/rules/%s failed: resp=%v err=%v", ailock.InlineRuleProviderName, putResp, err)
	}
	putResp.Body.Close()

	perpAfter, err := fetchViaProxy(client, "http://perplexity.ai/search")
	if err != nil || !strings.Contains(perpAfter, "via=LOCKED_AI_NODE") {
		t.Fatalf("expected perplexity.ai via LOCKED_AI_NODE immediately after hot provider update, got body=%q err=%v", perpAfter, err)
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
