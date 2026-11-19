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

	// Disable GEOSITE rules in mixin so the offline integration test needs no external geosite.dat.
	if err := profile.WriteFileAtomic(store.ProfileMixinPath(), []byte(profile.DisabledProfileMixinTemplate), 0600); err != nil {
		t.Fatal(err)
	}

	// Local mock UDP DNS server to prove conan.dingtalk.test and dynamically added custom intranet
	// domains bypass Fake-IP and route DIRECT!
	mockDNSAddr, stopMockDNS := startLocalMockDNSServer(t, map[string][4]byte{
		"conan.dingtalk.test":  {127, 0, 0, 1},
		"app.clashfx.local":    {127, 0, 0, 1},
		"buildbox":             {127, 0, 0, 1},
		"repo.custom-conan.biz": {127, 0, 0, 1},
	})
	defer stopMockDNS()

	cfgYAML := fmt.Sprintf(`mixed-port: 17992
dns:
  enable: true
  listen: 127.0.0.1:10553
  enhanced-mode: fake-ip
  fake-ip-range: 198.18.0.1/16
  nameserver:
    - %s
  nameserver-policy:
    "+.test": %s
    "+.local": %s
    "+.custom-conan.biz": %s
    "*": %s
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
`, mockDNSAddr, mockDNSAddr, mockDNSAddr, mockDNSAddr, mockDNSAddr, generalPort, lockedPort)

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

	// 1b. Private/intranet domains MUST skip Fake-IP (returning real IP, never 198.18.x.x)
	// and route DIRECT even while AI Proxy Lock is active!
	dnsListenAddr := fmt.Sprintf("127.0.0.1:%d", state.DNSPort)
	for _, privHost := range []string{"conan.dingtalk.test", "app.clashfx.local", "buildbox"} {
		ip, qErr := queryUDPTypeA(dnsListenAddr, privHost)
		if qErr != nil {
			t.Fatalf("DNS A query for private host %s via mihomo_core (%s) failed: %v", privHost, dnsListenAddr, qErr)
		}
		if ip != "127.0.0.1" {
			t.Fatalf("private host %s resolved to %s (expected real IP 127.0.0.1, NOT Fake-IP 198.18.x.x)", privHost, ip)
		}
	}
	if aiIP, qErr := queryUDPTypeA(dnsListenAddr, "api.anthropic.com"); qErr != nil || !strings.HasPrefix(aiIP, "198.18.") {
		t.Fatalf("expected public AI host api.anthropic.com to receive Fake-IP 198.18.x.x, got %q (err=%v)", aiIP, qErr)
	}
	directBeforePriv := directHits.Load()
	privBody, privErr := fetchViaProxy(client, fmt.Sprintf("http://conan.dingtalk.test:%d/v1/conan", echoPort))
	if privErr != nil || !strings.Contains(privBody, "via=DIRECT_LEAK") || directHits.Load() <= directBeforePriv {
		t.Fatalf("expected conan.dingtalk.test to route DIRECT ahead of AI Lock, got body=%q err=%v", privBody, privErr)
	}

	// 1c. Dynamically reconfigure clashfx-private-direct.list: add a custom public-TLD intranet domain
	// (DOMAIN-SUFFIX,custom-conan.biz) and remove DOMAIN-SUFFIX,test, then hot-reload via ctrl.Apply!
	if beforeBizIP, qErr := queryUDPTypeA(dnsListenAddr, "repo.custom-conan.biz"); qErr != nil || !strings.HasPrefix(beforeBizIP, "198.18.") {
		t.Fatalf("expected repo.custom-conan.biz to get Fake-IP before adding to clashfx-private-direct.list, got %q (err=%v)", beforeBizIP, qErr)
	}
	privListPath := store.BuiltinRulesetPath(profile.PrivateDirectRuleProviderName)
	origPrivList, err := os.ReadFile(privListPath)
	if err != nil {
		t.Fatalf("read clashfx-private-direct.list: %v", err)
	}
	customPrivList := strings.ReplaceAll(string(origPrivList), "DOMAIN-SUFFIX,test\n", "") + "\nDOMAIN-SUFFIX,custom-conan.biz\n"
	if err := profile.WriteFileAtomic(privListPath, []byte(customPrivList), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ctrl.Apply(context.Background(), false); err != nil {
		t.Fatalf("hot-reload after editing clashfx-private-direct.list failed: %v", err)
	}
	if afterBizIP, qErr := queryUDPTypeA(dnsListenAddr, "repo.custom-conan.biz"); qErr != nil || afterBizIP != "127.0.0.1" {
		t.Fatalf("expected custom-conan.biz to skip Fake-IP after adding to clashfx-private-direct.list, got %q (err=%v)", afterBizIP, qErr)
	}
	directBeforeBiz := directHits.Load()
	bizBody, bizErr := fetchViaProxy(client, fmt.Sprintf("http://repo.custom-conan.biz:%d/v1/conan", echoPort))
	if bizErr != nil || !strings.Contains(bizBody, "via=DIRECT_LEAK") || directHits.Load() <= directBeforeBiz {
		t.Fatalf("expected repo.custom-conan.biz to route DIRECT after adding to clashfx-private-direct.list, got body=%q err=%v", bizBody, bizErr)
	}
	if removedTestIP, qErr := queryUDPTypeA(dnsListenAddr, "conan.dingtalk.test"); qErr != nil || !strings.HasPrefix(removedTestIP, "198.18.") {
		t.Fatalf("expected removed DOMAIN-SUFFIX,test to no longer be hardcoded in fake-ip-filter, got %q (err=%v)", removedTestIP, qErr)
	}
	// Restore default clashfx-private-direct.list
	if err := profile.WriteFileAtomic(privListPath, origPrivList, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ctrl.Apply(context.Background(), false); err != nil {
		t.Fatalf("restore default clashfx-private-direct.list failed: %v", err)
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

// startLocalMockDNSServer serves minimal RFC1035 A records on 127.0.0.1:0 for offline real-core DNS tests.
func startLocalMockDNSServer(t *testing.T, records map[string][4]byte) (string, func()) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		buf := make([]byte, 1500)
		for {
			n, addr, rErr := pc.ReadFrom(buf)
			if rErr != nil {
				return
			}
			if n < 12 {
				continue
			}
			name, qEnd := decodeDNSName(buf[:n], 12)
			if qEnd+4 > n {
				continue
			}
			qType := binary.BigEndian.Uint16(buf[qEnd : qEnd+2])
			resp := make([]byte, 0, n+16)
			resp = append(resp, buf[0], buf[1]) // ID
			ip, found := records[strings.ToLower(strings.TrimSuffix(name, "."))]
			if found && qType == 1 {
				resp = append(resp, 0x81, 0x80) // standard response, NOERROR
				resp = append(resp, 0, 1, 0, 1, 0, 0, 0, 0)
				resp = append(resp, buf[12:qEnd+4]...)
				resp = append(resp, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, ip[0], ip[1], ip[2], ip[3])
			} else {
				resp = append(resp, 0x81, 0x83) // NXDOMAIN
				resp = append(resp, 0, 1, 0, 0, 0, 0, 0, 0)
				resp = append(resp, buf[12:qEnd+4]...)
			}
			_, _ = pc.WriteTo(resp, addr)
		}
	}()
	return pc.LocalAddr().String(), func() { _ = pc.Close() }
}

func decodeDNSName(pkt []byte, offset int) (string, int) {
	var labels []string
	i := offset
	for i < len(pkt) {
		l := int(pkt[i])
		i++
		if l == 0 {
			break
		}
		if i+l > len(pkt) {
			break
		}
		labels = append(labels, string(pkt[i:i+l]))
		i += l
	}
	return strings.Join(labels, "."), i
}

func queryUDPTypeA(serverAddr, host string) (string, error) {
	conn, err := net.DialTimeout("udp", serverAddr, 2*time.Second)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

	pkt := []byte{0x12, 0x34, 0x01, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		pkt = append(pkt, byte(len(label)))
		pkt = append(pkt, []byte(label)...)
	}
	pkt = append(pkt, 0x00, 0x00, 0x01, 0x00, 0x01) // QTYPE=A, QCLASS=IN
	if _, err := conn.Write(pkt); err != nil {
		return "", err
	}
	resp := make([]byte, 1500)
	n, err := conn.Read(resp)
	if err != nil {
		return "", err
	}
	if n < 12 {
		return "", fmt.Errorf("short DNS response (%d bytes)", n)
	}
	ancount := binary.BigEndian.Uint16(resp[6:8])
	if ancount == 0 {
		return "", fmt.Errorf("no answer records (rcode=%d)", resp[3]&0x0f)
	}
	_, pos := decodeDNSName(resp[:n], 12)
	pos += 4 // skip QTYPE + QCLASS
	for i := 0; i < int(ancount) && pos < n; i++ {
		if resp[pos]&0xc0 == 0xc0 {
			pos += 2
		} else {
			_, pos = decodeDNSName(resp[:n], pos)
		}
		if pos+10 > n {
			break
		}
		rType := binary.BigEndian.Uint16(resp[pos : pos+2])
		rdLen := int(binary.BigEndian.Uint16(resp[pos+8 : pos+10]))
		pos += 10
		if pos+rdLen > n {
			break
		}
		if rType == 1 && rdLen == 4 {
			return fmt.Sprintf("%d.%d.%d.%d", resp[pos], resp[pos+1], resp[pos+2], resp[pos+3]), nil
		}
		pos += rdLen
	}
	return "", fmt.Errorf("no A record in DNS answer")
}
