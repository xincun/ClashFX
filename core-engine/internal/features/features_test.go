package features

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/clashfx/core-engine/internal/profile"
)

func TestCNAppsProbeDiagnosticsNetEnvAndUpdater(t *testing.T) {
	// 1. CN Apps bypass rule ordering (after AI Lock + internal rules, before general rules)
	root := map[string]any{
		"rules": []any{
			"PROCESS-NAME,mihomo_core.exe,DIRECT",
			"DOMAIN-SUFFIX,claude.ai,LockedNode",
			"DOMAIN-SUFFIX,claude.ai,REJECT",
			"MATCH,Proxy",
		},
	}
	hook := TransformHookForCNApps()
	if err := hook(root, profile.AppSettings{EnhancedMode: true, BypassCNApps: true}); err != nil {
		t.Fatal(err)
	}
	rules := root["rules"].([]any)
	if rules[0] != "PROCESS-NAME,mihomo_core.exe,DIRECT" || rules[2] != "DOMAIN-SUFFIX,claude.ai,REJECT" {
		t.Fatalf("AI Lock rules displaced by CN apps: %v", rules[:4])
	}
	if !strings.HasPrefix(rules[3].(string), "PROCESS-NAME,Weixin.exe,DIRECT") {
		t.Fatalf("expected Weixin.exe bypass after AI Lock, got %v", rules[3])
	}

	// 2. Single-node diagnostic decode of clashfx_probe_error
	mockCore := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": "An error occurred in the delay test",
			"clashfx_probe_error": map[string]string{
				"kind": "tls",
				"code": "tls_name_mismatch",
			},
		})
	}))
	defer mockCore.Close()
	u, _ := url.Parse(mockCore.URL)
	_, portStr, _ := strings.Cut(u.Host, ":")
	apiPort, _ := strconv.Atoi(portStr)

	rep, err := ProbeSingleNode(context.Background(), apiPort, "", "TestNode", "", "https://alice:secret@cp.cloudflare.com/generate_204?tok=1", 2000, "zh_CN")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Healthy || rep.Code != "tls_name_mismatch" || strings.Contains(rep.Formatted, "secret") {
		t.Fatalf("unexpected diagnostic report: %+v", rep)
	}

	// 3. Network suspend matching
	sig := ActiveNetworkSignature{
		ProfileNames: []string{"CorpOffice"},
		Gateways:     []string{"10.211.55.1"},
	}
	if rule, ok := MatchSuspendNetwork(sig, []string{"HomeWiFi", "10.211.55.1"}); !ok || rule != "10.211.55.1" {
		t.Fatalf("MatchSuspendNetwork = (%q, %v), want (10.211.55.1, true)", rule, ok)
	}

	// 4. Updater stable vs lab channel
	relSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"tag_name":   "v2.1.0-beta.1",
				"prerelease": true,
				"html_url":   "https://example.com/lab",
				"assets": []map[string]string{
					{"name": "ClashFX-Setup-arm64.exe", "browser_download_url": "https://example.com/arm64-beta.exe"},
					{"name": "ClashFX-Setup-amd64.exe", "browser_download_url": "https://example.com/amd64-beta.exe"},
				},
			},
			{
				"tag_name":   "v2.0.0",
				"prerelease": false,
				"html_url":   "https://example.com/stable",
				"assets": []map[string]string{
					{"name": "ClashFX-Setup-arm64.exe", "browser_download_url": "https://example.com/arm64.exe"},
					{"name": "ClashFX-Setup-amd64.exe", "browser_download_url": "https://example.com/amd64.exe"},
				},
			},
		})
	}))
	defer relSrv.Close()

	stable, err := CheckForUpdates(context.Background(), "v1.19.24", "stable", relSrv.URL)
	if err != nil || !stable.UpdateAvailable || stable.LatestVersion != "v2.0.0" || stable.DownloadURL == "" {
		t.Fatalf("unexpected stable update result: %+v (%v)", stable, err)
	}
	lab, err := CheckForUpdates(context.Background(), "v2.0.0", "lab", relSrv.URL)
	if err != nil || !lab.UpdateAvailable || lab.LatestVersion != "v2.1.0-beta.1" {
		t.Fatalf("unexpected lab update result: %+v (%v)", lab, err)
	}
}
