package subscription

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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
