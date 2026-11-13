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
	// 1. CN Apps bypass + Custom Rules ordering & @DEFAULT cross-profile resolution
	root := map[string]any{
		"proxy-groups": []any{
			map[string]any{"name": "JMS", "type": "select", "proxies": []any{"DIRECT"}},
		},
		"rules": []any{
			"RULE-SET,clashfx-internal-direct,DIRECT",
			"RULE-SET,clashfx-ai-lock,LockedNode",
			"RULE-SET,clashfx-ai-lock,REJECT",
			"DOMAIN-SUFFIX,sub-rule.example,JMS",
			"MATCH,JMS",
		},
	}
	customRules, err := NormalizeAndValidateCustomRules([]profile.CustomRule{
		{Enabled: true, Type: "domain-suffix", Payload: "scholar.google.com", Target: "@default", Position: "prepend"},
		{Enabled: true, Type: "ip-cidr", Payload: "10.99.0.0/16", Target: "DIRECT", Position: "before_match", NoResolve: true},
		{Enabled: true, Type: "domain", Payload: "other-profile-only.test", Target: "NonExistentGroup", Position: "prepend"},
		{Enabled: false, Type: "domain", Payload: "disabled.test", Target: "DIRECT", Position: "prepend"},
	})
	if err != nil {
		t.Fatal(err)
	}
	settings := profile.AppSettings{
		EnhancedMode: true,
		BypassCNApps: true,
		CustomRules:  customRules,
	}
	if err := TransformHookForCNApps()(root, settings); err != nil {
		t.Fatal(err)
	}
	if err := TransformHookForCustomRules(nil)(root, settings); err != nil {
		t.Fatal(err)
	}
	rules := profile.ExtractStringRules(root["rules"])
	wantOrder := []string{
		"RULE-SET,clashfx-internal-direct,DIRECT",
		"RULE-SET,clashfx-ai-lock,LockedNode",
		"RULE-SET,clashfx-ai-lock,REJECT",
		"DOMAIN-SUFFIX,scholar.google.com,JMS",
		"RULE-SET,clashfx-cn-apps-direct,DIRECT",
		"DOMAIN-SUFFIX,sub-rule.example,JMS",
		"IP-CIDR,10.99.0.0/16,DIRECT,no-resolve",
		"MATCH,JMS",
	}
	if len(rules) != len(wantOrder) {
		t.Fatalf("rules = %v, want %v", rules, wantOrder)
	}
	for i := range wantOrder {
		if rules[i] != wantOrder[i] {
			t.Fatalf("rules[%d] = %q, want %q", i, rules[i], wantOrder[i])
		}
	}
	rpMap, _ := root["rule-providers"].(map[string]any)
	if _, ok := rpMap[profile.CNAppsRuleProviderName]; !ok {
		t.Fatalf("expected inline rule-provider %s in root", profile.CNAppsRuleProviderName)
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
