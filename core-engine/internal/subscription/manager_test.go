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
