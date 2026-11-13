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
	// 1. Profile Mixin visual <-> YAML lossless round-trip + CNApps + PROXY target resolution
	baseMixin := []byte("ipv6: true\n")
	builtYAML, err := BuildProfileMixinYAML(baseMixin, true, true, []MixinRuleItem{
		{Type: "domain-suffix", Payload: "https://.scholar.org/papers?q=1", Target: "proxy"},
		{Type: "domain-regex", Payload: `^api\d{1,3}\.scholar\.org$`, Target: "proxy"},
		{Type: "ip-cidr", Payload: "10.99.0.5", Target: "DIRECT"},
	})
	if err != nil {
		t.Fatal(err)
	}
	parsedDoc, err := ParseProfileMixinDocument(builtYAML, "JMS")
	if err != nil {
		t.Fatal(err)
	}
	if !parsedDoc.BypassCNWeb || !parsedDoc.BypassLAN || !parsedDoc.HasExtraKeys || len(parsedDoc.Rules) != 3 {
		t.Fatalf("unexpected parsed ProfileMixinDocument: %+v", parsedDoc)
	}
	if parsedDoc.Rules[0].Payload != "scholar.org" || parsedDoc.Rules[0].Target != "PROXY" {
		t.Fatalf("expected cleaned domain + PROXY placeholder, got %+v", parsedDoc.Rules[0])
	}
	if parsedDoc.Rules[1].Payload != `^api\d{1,3}\.scholar\.org$` || parsedDoc.Rules[1].Target != "PROXY" {
		t.Fatalf("expected comma-containing regex payload preserved intact, got %+v", parsedDoc.Rules[1])
	}
	if parsedDoc.Rules[2].Payload != "10.99.0.5/32" {
		t.Fatalf("expected /32 auto-mask on bare IPv4, got %+v", parsedDoc.Rules[2])
	}

	root := map[string]any{
		"proxy-groups": []any{
			map[string]any{"name": "JMS", "type": "select", "proxies": []any{"DIRECT"}},
		},
		"rules": []any{
			"MATCH,JMS",
		},
	}
	root, err = profile.ApplyProfileMixin(root, builtYAML)
	if err != nil {
		t.Fatal(err)
	}
	if root["ipv6"] != true {
		t.Fatalf("expected extra YAML key ipv6:true preserved, got %v", root["ipv6"])
	}
	// Simulate system guards + CN apps injected after ApplyProfileMixin (matching controller.go)
	existing := profile.ExtractStringRules(root["rules"])
	root["rules"] = append([]string{
		"RULE-SET,clashfx-internal-direct,DIRECT",
		"RULE-SET,clashfx-ai-lock,LockedNode",
		"RULE-SET,clashfx-ai-lock,REJECT",
	}, existing...)

	settings := profile.AppSettings{
		EnhancedMode: true,
		BypassCNApps: true,
	}
	if err := TransformHookForCNApps()(root, settings); err != nil {
		t.Fatal(err)
	}
	rules := profile.ExtractStringRules(root["rules"])
	wantOrder := []string{
		"RULE-SET,clashfx-internal-direct,DIRECT",
		"RULE-SET,clashfx-ai-lock,LockedNode",
		"RULE-SET,clashfx-ai-lock,REJECT",
		"RULE-SET,clashfx-cn-apps-direct,DIRECT",
		"DOMAIN-SUFFIX,scholar.org,JMS",
		`DOMAIN-REGEX,^api\d{1,3}\.scholar\.org$,JMS`,
		"IP-CIDR,10.99.0.5/32,DIRECT,no-resolve",
		"GEOSITE,private,DIRECT",
		"GEOIP,LAN,DIRECT,no-resolve",
		"GEOSITE,cn,DIRECT",
		"GEOIP,CN,DIRECT,no-resolve",
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

	// 2. Single-node diagnostic classification from stock Mihomo error message
	mockCore := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": "Get \"https://cp.cloudflare.com/generate_204\": x509: certificate is valid for wrong.example.com, not cp.cloudflare.com",
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
	if rep.Healthy || rep.Code != "tls_name_mismatch" || strings.Contains(rep.Formatted, "secret") || !strings.Contains(rep.Formatted, "x509:") {
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

	// 4. Updater stable vs lab channel + ignores macOS-only DMG releases
	relSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"tag_name":   "v9.9.9",
				"prerelease": false,
				"html_url":   "https://example.com/macos-only",
				"assets": []map[string]string{
					{"name": "ClashFX.dmg", "browser_download_url": "https://example.com/ClashFX.dmg"},
					{"name": "ClashFX.app.dSYM.zip", "browser_download_url": "https://example.com/ClashFX.app.dSYM.zip"},
				},
			},
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
