package subscription

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/clashfx/core-engine/internal/profile"
)

type fakeCoreHelper struct{}

func (fakeCoreHelper) ConvertShareLinks(raw []byte) ([]byte, error) {
	return []byte("mixed-port: 7890\nproxies:\n  - name: Converted\n    type: socks5\n    server: 127.0.0.1\n    port: 1080\nrules:\n  - MATCH,Converted\n"), nil
}

func (fakeCoreHelper) ValidateConfig(_, configPath string) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	if strings.Contains(string(data), "INVALID_CONFIG") {
		return fmt.Errorf("Parse config error: invalid")
	}
	return nil
}

func TestSubscriptionFetchUserinfoAndShareLinkConversion(t *testing.T) {
	store, err := profile.NewStore(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	mode := "yaml"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != DefaultSubscriptionUserAgent {
			t.Errorf("unexpected UA: %q", r.Header.Get("User-Agent"))
		}
		w.Header().Set("Subscription-Userinfo", "upload=1024; download=2048; total=1048576; expire=1893456000")
		switch mode {
		case "yaml":
			_, _ = w.Write([]byte("mixed-port: 7890\nproxies:\n  - name: HK\n    type: socks5\n    server: 127.0.0.1\n    port: 1080\nrules:\n  - MATCH,HK\n"))
		case "base64":
			enc := base64.StdEncoding.EncodeToString([]byte("socks5://127.0.0.1:1080#NodeA\n"))
			_, _ = w.Write([]byte(enc))
		case "corrupt":
			_, _ = w.Write([]byte("proxies:\n  - name: Bad\nINVALID_CONFIG\n"))
		}
	}))
	defer srv.Close()

	mgr, err := NewManager(store, fakeCoreHelper{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.AddOrUpdate("ProviderA", srv.URL, ""); err != nil {
		t.Fatal(err)
	}

	sub, err := mgr.Refresh(context.Background(), "ProviderA", 0)
	if err != nil {
		t.Fatal(err)
	}
	if sub.UserInfo == nil || *sub.UserInfo.Upload != 1024 || *sub.UserInfo.Download != 2048 || *sub.UserInfo.Total != 1048576 {
		t.Fatalf("unexpected userinfo: %+v", sub.UserInfo)
	}

	// Corrupt remote payload must NOT overwrite the existing good file!
	mode = "corrupt"
	if _, err := mgr.Refresh(context.Background(), "ProviderA", 0); err == nil {
		t.Fatal("expected corrupt subscription validation to fail")
	}
	savedBytes, err := os.ReadFile(store.ProfilePath("ProviderA"))
	if err != nil || !strings.Contains(string(savedBytes), "name: HK") {
		t.Fatalf("corrupt update overwrote good profile: %q", string(savedBytes))
	}

	// Base64 share-link conversion succeeds
	mode = "base64"
	if _, err := mgr.Refresh(context.Background(), "ProviderA", 0); err != nil {
		t.Fatalf("base64 share-link refresh failed: %v", err)
	}
	savedBytes, _ = os.ReadFile(store.ProfilePath("ProviderA"))
	if !strings.Contains(string(savedBytes), "Converted") {
		t.Fatalf("expected converted YAML, got:\n%s", string(savedBytes))
	}
}

func TestParseUserinfoBodyAndCustomUserAgent(t *testing.T) {
	raw := base64.StdEncoding.EncodeToString([]byte(
		"socks5://127.0.0.1:1080#%E6%80%BB%E6%B5%81%E9%87%8F%3A%20100%20GB\n" +
			"socks5://127.0.0.1:1081#%E5%89%A9%E4%BD%99%E6%B5%81%E9%87%8F%3A%2040%20GB\n" +
			"socks5://127.0.0.1:1082#%E5%A5%97%E9%A4%90%E5%88%B0%E6%9C%9F%3A%202027-05-01\n",
	))
	parsed := ParseUserinfoBody([]byte(raw))
	if parsed == nil {
		t.Fatal("expected ParseUserinfoBody to extract quota & expiry")
	}
	const gb = int64(1073741824)
	if parsed.Total == nil || *parsed.Total != 100*gb {
		t.Fatalf("expected total 100 GB, got %+v", parsed.Total)
	}
	if parsed.Download == nil || *parsed.Download != 60*gb {
		t.Fatalf("expected derived used 60 GB, got %+v", parsed.Download)
	}
	if parsed.ExpireText != "2027-05-01" {
		t.Fatalf("expected expire_text 2027-05-01, got %q", parsed.ExpireText)
	}

	// Header takes precedence over body fallback when both exist
	hdr := ParseUserinfoHeader("upload=10; download=20; total=100")
	merged := MergeUserInfo(hdr, parsed)
	if *merged.Total != 100 || *merged.Upload != 10 || *merged.Download != 20 || merged.ExpireText != "2027-05-01" {
		t.Fatalf("unexpected MergeUserInfo result: %+v", merged)
	}
}

func TestRefreshWithFallbackProxyToDirect(t *testing.T) {
	store, err := profile.NewStore(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	const subYAML = "mixed-port: 7890\nproxies:\n  - name: JP-01\n    type: socks5\n    server: 127.0.0.1\n    port: 1080\nrules:\n  - MATCH,JP-01\n"
	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, subYAML)
	}))
	defer targetSrv.Close()

	var badHits atomic.Int32
	badProxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		badHits.Add(1)
		http.Error(w, "dead upstream proxy", http.StatusBadGateway)
	}))
	defer badProxySrv.Close()

	var goodHits atomic.Int32
	goodProxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		goodHits.Add(1)
		resp, err := http.Get(targetSrv.URL)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	defer goodProxySrv.Close()

	parsePort := func(raw string) int {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		p, err := strconv.Atoi(u.Port())
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	badPort := parsePort(badProxySrv.URL)
	goodPort := parsePort(goodProxySrv.URL)

	mgr, err := NewManager(store, fakeCoreHelper{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.AddOrUpdate("FallbackSub", targetSrv.URL, ""); err != nil {
		t.Fatal(err)
	}

	// 1. Primary dead proxy fails -> automatically retries via internal DIRECT listener
	if _, err := mgr.RefreshWithFallback(context.Background(), "FallbackSub", badPort, goodPort); err != nil {
		t.Fatalf("RefreshWithFallback(bad primary -> good direct) failed: %v", err)
	}
	if badHits.Load() != 1 || goodHits.Load() != 1 {
		t.Fatalf("expected 1 bad hit + 1 direct hit, got bad=%d good=%d", badHits.Load(), goodHits.Load())
	}

	// 2. Healthy primary proxy succeeds -> does not touch direct fallback
	badHits.Store(0)
	goodHits.Store(0)
	if _, err := mgr.RefreshWithFallback(context.Background(), "FallbackSub", goodPort, badPort); err != nil {
		t.Fatalf("RefreshWithFallback(good primary -> bad direct) failed: %v", err)
	}
	if goodHits.Load() != 1 || badHits.Load() != 0 {
		t.Fatalf("expected 1 primary hit + 0 fallback hits, got good=%d bad=%d", goodHits.Load(), badHits.Load())
	}

	// 3. Both proxy listeners unavailable -> falls back to direct transport (port 0)
	badHits.Store(0)
	if _, err := mgr.RefreshWithFallback(context.Background(), "FallbackSub", badPort, badPort); err != nil {
		t.Fatalf("RefreshWithFallback(bad proxies -> transport 0) failed: %v", err)
	}
	if badHits.Load() != 1 {
		t.Fatalf("expected deduplicated bad proxy hit count 1, got %d", badHits.Load())
	}
}

func TestSubscriptionSecurityHardeningAndUnicodeUserinfo(t *testing.T) {
	// 1. UTF-8 case-folding (Turkish İ -> i\u0307) must NEVER panic in ParseUserinfoBody.
	turkishBody := []byte("proxies:\n  - {name: \"İİİİİİİİTotal: 100GB\", type: ss}\n  - {name: \"İİİİİİİİExpire: 2028-01-01\", type: ss}\n")
	info := ParseUserinfoBody(turkishBody)
	if info == nil || info.Total == nil || info.ExpireText == "" {
		t.Fatalf("expected Turkish İ userinfo parsed cleanly without panic, got %+v", info)
	}

	// 2. Remove rejects path traversal and refuses to delete unmanaged local YAML files.
	dir := t.TempDir()
	store, err := profile.NewStore(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	localConfig := filepath.Join(store.ActiveConfigDir(), "config.yaml")
	if err := os.WriteFile(localConfig, []byte("mixed-port: 7890\n"), 0600); err != nil {
		t.Fatal(err)
	}
	mgr, err := NewManager(store, fakeCoreHelper{})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.Remove("../../../outside", true); err == nil {
		t.Fatal("expected Remove with traversal name to return error")
	}
	if err := mgr.Remove("config", true); err != nil {
		t.Fatalf("unexpected error removing absent managed sub: %v", err)
	}
	if _, err := os.Stat(localConfig); err != nil {
		t.Fatalf("expected unmanaged local config.yaml NOT to be deleted by Remove: %v", err)
	}

	// 3. Content-Disposition: filename="config.yaml" must not overwrite existing local config.yaml.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="config.yaml"`)
		_, _ = w.Write([]byte("proxies:\n  - {name: HK, type: ss, server: 1.1.1.1, port: 443, cipher: aes-128-gcm, password: pass}\n"))
	}))
	defer srv.Close()
	if _, err := mgr.AddOrUpdate("FreshSub", srv.URL, ""); err != nil {
		t.Fatal(err)
	}
	updated, err := mgr.Refresh(context.Background(), "FreshSub", 0)
	if err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}
	if updated.Name != "FreshSub" {
		t.Fatalf("expected FreshSub to keep its name instead of clobbering existing config.yaml, got %q", updated.Name)
	}
	rawLocal, _ := os.ReadFile(localConfig)
	if string(rawLocal) != "mixed-port: 7890\n" {
		t.Fatalf("existing config.yaml was overwritten: %q", string(rawLocal))
	}

	// 4. Share links colliding with reserved/group names (Proxy, Auto, DIRECT) are disambiguated.
	links := strings.Join([]string{
		"ss://YWVzLTEyOC1nY206cGFzcw==@1.1.1.1:443#Proxy",
		"ss://YWVzLTEyOC1nY206cGFzcw==@1.1.1.2:443#Auto",
		"ss://YWVzLTEyOC1nY206cGFzcw==@1.1.1.3:443#DIRECT",
	}, "\n")
	converted, err := ConvertShareLinksPayload([]byte(links))
	if err != nil {
		t.Fatalf("ConvertShareLinksPayload failed: %v", err)
	}
	text := string(converted)
	if !strings.Contains(text, "Proxy (2)") || !strings.Contains(text, "Auto (2)") || !strings.Contains(text, "DIRECT (2)") {
		t.Fatalf("expected reserved/group names disambiguated, got:\n%s", text)
	}
}
