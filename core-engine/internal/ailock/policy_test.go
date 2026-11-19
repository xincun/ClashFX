package ailock

import (
	"regexp"
	"testing"

	"github.com/clashfx/core-engine/internal/profile"
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

	darwinPathRe := regexp.MustCompile(DarwinProcessPathPattern)
	for _, darwinMatch := range []string{
		`/Applications/Claude.app/Contents/MacOS/Claude`,
		`/Applications/Claude.app/Contents/Frameworks/Claude Helper.app/Contents/MacOS/Claude Helper`,
		`/Users/alice/Applications/Antigravity.app/Contents/MacOS/Antigravity`,
		`/Applications/Codex.app/Contents/MacOS/Codex`,
		`/Applications/ChatGPT.app/Contents/MacOS/ChatGPT`,
	} {
		if !darwinPathRe.MatchString(darwinMatch) {
			t.Fatalf("expected macOS AI install path %q to match DarwinProcessPathPattern", darwinMatch)
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
	if aiProv["type"] != "file" || aiProv["behavior"] != "classical" || aiProv["format"] != "text" || aiProv["path"] != "./ruleset/clashfx-ai-lock.list" {
		t.Fatalf("unexpected file rule-provider: %+v", aiProv)
	}
	parsedDefault, err := profile.ParseAndValidateClassicalRulesetText(profile.DefaultAILockRulesetText())
	if err != nil || len(parsedDefault) != 3+len(ProtectedDomains) {
		t.Fatalf("default AI lock ruleset len = %d (err=%v), want %d", len(parsedDefault), err, 3+len(ProtectedDomains))
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
