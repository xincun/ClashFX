package profile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestStoreCustomConfigDirMixinAndWatcher(t *testing.T) {
	appDir := t.TempDir()
	customDir := filepath.Join(t.TempDir(), "synced-configs")

	store, err := NewStore(appDir, "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Neither default dir nor custom dir should auto-create starter config.yaml
	if _, err := os.Stat(filepath.Join(appDir, "config.yaml")); !os.IsNotExist(err) {
		t.Fatalf("did not expect starter config.yaml to be auto-created in appDir, stat err=%v", err)
	}

	// Switch to custom folder (replaces iCloud sync on Windows)
	_, err = store.UpdateSettings(func(st *AppSettings) error {
		st.CustomConfigDir = customDir
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if store.ActiveConfigDir() != customDir {
		t.Fatalf("ActiveConfigDir = %q, want %q", store.ActiveConfigDir(), customDir)
	}
	if _, err := os.Stat(filepath.Join(customDir, "config.yaml")); !os.IsNotExist(err) {
		t.Fatalf("did not expect starter config.yaml to be auto-created in custom dir, stat err=%v", err)
	}

	// Remember recovery selections
	if err := store.RememberProxySelection("config", "Proxy", "HK-01"); err != nil {
		t.Fatal(err)
	}
	sel := store.RecoverySelectionsForProfile("config")
	if sel["Proxy"] != "HK-01" {
		t.Fatalf("RecoverySelectionsForProfile = %v", sel)
	}

	// Profile Mixin merge test
	base, err := UnmarshalYAMLMap([]byte(`
mixed-port: 7890
proxy-groups:
  - name: Proxy
    type: select
    proxies: [DIRECT]
rules:
  - DOMAIN,keep.example,DIRECT
`))
	if err != nil {
		t.Fatal(err)
	}
	mixin := []byte(`
ipv6: true
proxy-groups:
  - name: Proxy
    proxies: [HK-01, DIRECT]
  - name: AI
    type: select
    proxies: [HK-01]
profile:
  prepend-rules:
    - DOMAIN-SUFFIX,first.example,Proxy
  append-rules:
    - MATCH,Proxy
`)
	merged, err := ApplyProfileMixin(base, mixin)
	if err != nil {
		t.Fatal(err)
	}
	if merged["ipv6"] != true {
		t.Fatalf("expected ipv6=true after mixin, got %v", merged["ipv6"])
	}
	rules := extractStringRules(merged["rules"])
	if len(rules) != 3 || rules[0] != "DOMAIN-SUFFIX,first.example,Proxy" || rules[2] != "MATCH,Proxy" {
		t.Fatalf("unexpected merged rules: %v", rules)
	}

	// fsnotify watcher debounce test
	var notifyCount atomic.Int32
	if err := store.WatchActiveConfigDir(func(_ string) {
		notifyCount.Add(1)
	}); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(filepath.Join(customDir, "work.yaml"), []byte("mixed-port: 7890\n"), 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && notifyCount.Load() == 0 {
		time.Sleep(30 * time.Millisecond)
	}
	if notifyCount.Load() == 0 {
		t.Fatal("expected fsnotify callback on work.yaml write")
	}
}

func TestEagerProfileMixinUpgradeAndInternalWriteWatcherSuppression(t *testing.T) {
	// 1. NewStore eagerly creates .profile_mixin.yaml with default CN & LAN direct rules
	dir := t.TempDir()
	s1, err := NewStore(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	mixinPath := s1.ProfileMixinPath()
	got, err := os.ReadFile(mixinPath)
	if err != nil {
		t.Fatalf("expected .profile_mixin.yaml eagerly created: %v", err)
	}
	if string(got) != DefaultProfileMixinTemplate {
		t.Fatalf("unexpected initial mixin contents:\n%s", string(got))
	}
	s1.Close()

	// 2. Untouched legacy comment-only .profile_mixin.yaml is upgraded on startup
	if err := WriteFileAtomic(mixinPath, []byte(LegacyEmptyProfileMixinTemplate), 0600); err != nil {
		t.Fatal(err)
	}
	s2, err := NewStore(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(mixinPath)
	if err != nil || string(got) != DefaultProfileMixinTemplate {
		t.Fatalf("expected legacy untouched mixin to be upgraded, got %q (%v)", string(got), err)
	}
	s2.Close()

	// 3. Explicit-off mixin (profile: {}) is preserved across restarts
	if err := WriteFileAtomic(mixinPath, []byte(DisabledProfileMixinTemplate), 0600); err != nil {
		t.Fatal(err)
	}
	s3, err := NewStore(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s3.Close()
	got, err = os.ReadFile(mixinPath)
	if err != nil || string(got) != DisabledProfileMixinTemplate {
		t.Fatalf("expected explicit-off mixin preserved, got %q (%v)", string(got), err)
	}

	// 4. Watcher suppresses internal write bounce, while still firing for external writes
	var notifyCount atomic.Int32
	if err := s3.WatchActiveConfigDir(func(_ string) {
		notifyCount.Add(1)
	}); err != nil {
		t.Fatal(err)
	}
	s3.MarkInternalWrite(mixinPath)
	if err := WriteFileAtomic(mixinPath, []byte(DefaultProfileMixinTemplate), 0600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	if notifyCount.Load() != 0 {
		t.Fatalf("expected internal write to be suppressed, got notifyCount=%d", notifyCount.Load())
	}

	extPath := filepath.Join(dir, "external.yaml")
	if err := WriteFileAtomic(extPath, []byte("mixed-port: 7890\n"), 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && notifyCount.Load() == 0 {
		time.Sleep(30 * time.Millisecond)
	}
	if notifyCount.Load() == 0 {
		t.Fatal("expected external write to trigger fsnotify callback")
	}
}

func TestUnmarshalYAMLMapAndApplyMixinSanitizesTabIndentation(t *testing.T) {
	tabMixin := []byte("# comment\nprofile:\n  prepend-rules:\n  \t- DOMAIN-SUFFIX,google.com,JMS\n    - GEOSITE,google,JMS\n")
	parsed, err := UnmarshalYAMLMap(tabMixin)
	if err != nil {
		t.Fatalf("expected tab-indented YAML to parse after sanitization: %v", err)
	}
	if _, ok := parsed["profile"].(map[string]any); !ok {
		t.Fatalf("expected profile map in parsed YAML: %+v", parsed)
	}

	base := map[string]any{
		"proxy-groups": []any{map[string]any{"name": "JMS", "type": "select", "proxies": []any{"DIRECT"}}},
		"rules":        []any{"MATCH,JMS"},
	}
	merged, err := ApplyProfileMixin(base, tabMixin)
	if err != nil {
		t.Fatalf("ApplyProfileMixin failed on tab-indented mixin: %v", err)
	}
	rules := extractStringRules(merged["rules"])
	if len(rules) != 3 || rules[0] != "DOMAIN-SUFFIX,google.com,JMS" || rules[1] != "GEOSITE,google,JMS" {
		t.Fatalf("unexpected merged rules: %v", rules)
	}

	fourSpaceTab := []byte("profile:\n    prepend-rules:\n\t- DOMAIN-SUFFIX,example.com,JMS\n    - MATCH,JMS\n")
	if _, err := UnmarshalYAMLMap(fourSpaceTab); err != nil {
		t.Fatalf("expected 4-space tab-indented YAML to parse: %v", err)
	}
}

func TestOutOfTheBoxDefaultSettingsAndMixin(t *testing.T) {
	store, err := NewStore(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	st := store.Settings()
	if !st.SystemProxyEnabled {
		t.Fatalf("expected SystemProxyEnabled=true by default")
	}
	if !st.EnhancedMode {
		t.Fatalf("expected EnhancedMode=true by default")
	}
	if !st.BypassCNApps {
		t.Fatalf("expected BypassCNApps=true by default")
	}
	if st.OutboundMode != "rule" {
		t.Fatalf("expected OutboundMode=rule by default, got %q", st.OutboundMode)
	}
	if !st.AutoUpdateSubs {
		t.Fatalf("expected AutoUpdateSubs=true by default")
	}
	if !st.ShowTraySpeed {
		t.Fatalf("expected ShowTraySpeed=true by default")
	}
	if strings.TrimSpace(st.ProxyBypassList) == "" {
		t.Fatalf("expected non-empty default ProxyBypassList")
	}
	if !strings.Contains(st.ProxyBypassList, "*.test") || !strings.Contains(st.ProxyBypassList, "*.internal") {
		t.Fatalf("expected default ProxyBypassList to include *.test and *.internal, got %q", st.ProxyBypassList)
	}
	if st.Hotkeys.OpenMainWindow != DefaultOpenMainWindowHotkey() {
		t.Fatalf("expected default OpenMainWindow=%q, got %q", DefaultOpenMainWindowHotkey(), st.Hotkeys.OpenMainWindow)
	}

	// Loading an existing settings.json that omits show_tray_speed or open_main_window must apply defaults.
	legacyJSONDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(legacyJSONDir, "settings.json"), []byte(`{"selected_profile":"config"}`), 0600); err != nil {
		t.Fatal(err)
	}
	legacyStore, err := NewStore(legacyJSONDir, "")
	if err != nil {
		t.Fatal(err)
	}
	defer legacyStore.Close()
	if !legacyStore.Settings().ShowTraySpeed {
		t.Fatalf("expected missing show_tray_speed in existing settings.json to default true")
	}
	if legacyStore.Settings().Hotkeys.OpenMainWindow != DefaultOpenMainWindowHotkey() {
		t.Fatalf("expected missing open_main_window in legacy settings.json to default %q, got %q", DefaultOpenMainWindowHotkey(), legacyStore.Settings().Hotkeys.OpenMainWindow)
	}

	// Loading an existing settings.json with custom hotkeys but no open_main_window must also default and persist it.
	legacyHotkeysDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(legacyHotkeysDir, "settings.json"), []byte(`{"selected_profile":"config","hotkeys":{"toggle_system_proxy":"Cmd+Shift+P"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	legacyHotkeysStore, err := NewStore(legacyHotkeysDir, "")
	if err != nil {
		t.Fatal(err)
	}
	defer legacyHotkeysStore.Close()
	if legacyHotkeysStore.Settings().Hotkeys.OpenMainWindow != DefaultOpenMainWindowHotkey() {
		t.Fatalf("expected missing open_main_window alongside custom hotkeys to default %q, got %q", DefaultOpenMainWindowHotkey(), legacyHotkeysStore.Settings().Hotkeys.OpenMainWindow)
	}
	if legacyHotkeysStore.Settings().Hotkeys.ToggleSystemProxy != "Cmd+Shift+P" {
		t.Fatalf("expected existing toggle_system_proxy preserved, got %q", legacyHotkeysStore.Settings().Hotkeys.ToggleSystemProxy)
	}

	// Loading settings.json where open_main_window was explicitly cleared ("") must preserve disabled state.
	clearedHotkeysDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(clearedHotkeysDir, "settings.json"), []byte(`{"selected_profile":"config","hotkeys":{"open_main_window":""}}`), 0600); err != nil {
		t.Fatal(err)
	}
	clearedStore, err := NewStore(clearedHotkeysDir, "")
	if err != nil {
		t.Fatal(err)
	}
	defer clearedStore.Close()
	if clearedStore.Settings().Hotkeys.OpenMainWindow != "" {
		t.Fatalf("expected explicitly cleared open_main_window to stay empty, got %q", clearedStore.Settings().Hotkeys.OpenMainWindow)
	}

	rawMixin, err := store.ReadProfileMixinBytes()
	if err != nil {
		t.Fatalf("ReadProfileMixinBytes failed: %v", err)
	}
	mixinText := string(rawMixin)
	for _, want := range []string{
		"GEOSITE,private,DIRECT",
		"GEOIP,LAN,DIRECT,no-resolve",
		"GEOSITE,cn,DIRECT",
		"GEOIP,CN,DIRECT,no-resolve",
	} {
		if !strings.Contains(mixinText, want) {
			t.Fatalf("expected default Profile Mixin to contain %q, got:\n%s", want, mixinText)
		}
	}

	merged, err := ApplyProfileMixin(map[string]any{
		"mixed-port": 7890,
		"rules":      []any{"MATCH,Proxy"},
	}, rawMixin)
	if err != nil {
		t.Fatalf("ApplyProfileMixin failed: %v", err)
	}
	var mergedRules []string
	switch v := merged["rules"].(type) {
	case []string:
		mergedRules = v
	case []any:
		for _, item := range v {
			mergedRules = append(mergedRules, fmt.Sprint(item))
		}
	default:
		t.Fatalf("unexpected merged rules type %T", merged["rules"])
	}
	joinedRules := strings.Join(mergedRules, "\n")
	for _, want := range []string{
		"GEOSITE,private,DIRECT",
		"GEOIP,LAN,DIRECT,no-resolve",
		"GEOSITE,cn,DIRECT",
		"GEOIP,CN,DIRECT,no-resolve",
		"MATCH,Proxy",
	} {
		if !strings.Contains(joinedRules, want) {
			t.Fatalf("expected merged rules to contain %q, got:\n%s", want, joinedRules)
		}
	}
}

func TestValidateProfileNameProviderPathSanitizationAndDeepCopy(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	for _, bad := range []string{"", ".", "..", "../secret", "a/b", `a\b`, ".profile_mixin", "foo/../../bar"} {
		if _, err := ValidateProfileName(bad); err == nil {
			t.Fatalf("expected ValidateProfileName(%q) to fail", bad)
		}
	}
	if got, err := ValidateProfileName("MySub.yaml"); err != nil || got != "MySub" {
		t.Fatalf("expected MySub, got (%q, %v)", got, err)
	}

	// SanitizeUntrustedProviderPaths strips ClashFX security rulesets, mixin, and traversal paths.
	root := map[string]any{
		"rule-providers": map[string]any{
			"stealth-priv": map[string]any{
				"type": "http",
				"url":  "https://evil.example/bypass.list",
				"path": "./ruleset/clashfx-private-direct.list",
			},
			"stealth-ai": map[string]any{
				"type": "http",
				"url":  "https://evil.example/ai.list",
				"path": "./clashfx-rulesets/clashfx-ai-lock.list",
			},
			"benign-http": map[string]any{
				"type": "http",
				"url":  "https://good.example/custom.list",
				"path": "./providers/custom.list",
			},
			AILockRuleProviderName: map[string]any{
				"type": "file",
				"path": "./ruleset/clashfx-ai-lock.list",
			},
		},
		"proxy-providers": map[string]any{
			"poison-mixin": map[string]any{
				"type": "http",
				"url":  "https://evil.example/mixin.yaml",
				"path": "./.profile_mixin.yaml",
			},
			"escape": map[string]any{
				"type": "http",
				"url":  "https://evil.example/p.yaml",
				"path": "../outside.yaml",
			},
		},
	}
	if !SanitizeUntrustedProviderPaths(root) {
		t.Fatal("expected SanitizeUntrustedProviderPaths to report modifications")
	}
	rp := root["rule-providers"].(map[string]any)
	if _, has := rp["stealth-priv"].(map[string]any)["path"]; has {
		t.Fatal("expected stealth-priv path targeting clashfx-private-direct.list to be stripped")
	}
	if _, has := rp["stealth-ai"].(map[string]any)["path"]; has {
		t.Fatal("expected stealth-ai path targeting clashfx-ai-lock.list to be stripped")
	}
	if got := rp["benign-http"].(map[string]any)["path"]; got != "./providers/custom.list" {
		t.Fatalf("expected benign-http path preserved, got %v", got)
	}
	if got := rp[AILockRuleProviderName].(map[string]any)["path"]; got != "./ruleset/clashfx-ai-lock.list" {
		t.Fatalf("expected builtin AI lock provider preserved, got %v", got)
	}
	pp := root["proxy-providers"].(map[string]any)
	if _, has := pp["poison-mixin"].(map[string]any)["path"]; has {
		t.Fatal("expected poison-mixin path targeting .profile_mixin.yaml to be stripped")
	}
	if _, has := pp["escape"].(map[string]any)["path"]; has {
		t.Fatal("expected escape traversal path to be stripped")
	}

	// UpdateSettings returns a deep copy and rolls back SavedSelections on mutator error.
	updated, err := store.UpdateSettings(func(s *AppSettings) error {
		s.SavedSelections["p1"] = map[string]string{"Proxy": "NodeA"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	updated.SavedSelections["p1"]["Proxy"] = "MUTATED_BY_CALLER"
	if got := store.Settings().SavedSelections["p1"]["Proxy"]; got != "NodeA" {
		t.Fatalf("expected caller mutation not to affect store, got %q", got)
	}
	_, _ = store.UpdateSettings(func(s *AppSettings) error {
		s.SavedSelections["p1"]["Proxy"] = "SHOULD_ROLLBACK"
		return fmt.Errorf("simulated failure")
	})
	if got := store.Settings().SavedSelections["p1"]["Proxy"]; got != "NodeA" {
		t.Fatalf("expected failed mutator to leave store untouched, got %q", got)
	}
}
