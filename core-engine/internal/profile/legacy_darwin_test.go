//go:build darwin

package profile

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMigrateLegacyDarwinPlistFile(t *testing.T) {
	dir := t.TempDir()
	remoteJSON := `[
		{
			"url": "https://example.com/sub.yaml",
			"name": "LegacySub",
			"userAgent": "ClashFX/1.0",
			"updateTime": 780000000,
			"subscriptionInfo": {
				"upload": 1024,
				"download": 2048,
				"total": 1073741824,
				"expire": 1900000000,
				"expireText": "2030-03-17"
			}
		}
	]`
	remoteB64 := base64.StdEncoding.EncodeToString([]byte(remoteJSON))

	plistXML := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>selectConfigName</key>
	<string>LegacySub</string>
	<key>selectOutBoundMode</key>
	<string>global</string>
	<key>enhancedMode</key>
	<true/>
	<key>enhancedModeUseCustomConfig</key>
	<true/>
	<key>bypassChineseApps</key>
	<true/>
	<key>claudeProxyLockEnabled</key>
	<true/>
	<key>claudeProxyLockTarget</key>
	<string>US-Node</string>
	<key>proxyPort</key>
	<integer>7891</integer>
	<key>apiPort</key>
	<integer>19091</integer>
	<key>apiPortAllowLan</key>
	<true/>
	<key>enableIPV6</key>
	<true/>
	<key>appLanguage</key>
	<string>en</string>
	<key>selectedMenuIconID</key>
	<string>cat</string>
	<key>disableSSIDList</key>
	<array>
		<string>Corp-WiFi</string>
		<string>Home-5G</string>
	</array>
	<key>tunMTU</key>
	<integer>1400</integer>
	<key>tunInterfaceName</key>
	<string>en0</string>
	<key>tunRouteExcludeRawText</key>
	<string>100.64.0.0/10</string>
	<key>isLabChannel</key>
	<true/>
	<key>kRemoteConfigs</key>
	<data>%s</data>
</dict>
</plist>`, remoteB64)

	plistPath := filepath.Join(dir, "com.clashfx.app.plist")
	if err := os.WriteFile(plistPath, []byte(plistXML), 0600); err != nil {
		t.Fatal(err)
	}

	appDir := filepath.Join(dir, "appdata")
	if err := os.MkdirAll(appDir, 0700); err != nil {
		t.Fatal(err)
	}
	settings := DefaultSettings()
	if err := MigrateLegacyDarwinPlistFile(plistPath, appDir, &settings); err != nil {
		t.Fatalf("MigrateLegacyDarwinPlistFile failed: %v", err)
	}

	if settings.SelectedProfile != "LegacySub" || settings.OutboundMode != "global" {
		t.Fatalf("unexpected profile/mode: %+v", settings)
	}
	if !settings.EnhancedMode || !settings.CustomConfigAsIs || !settings.BypassCNApps {
		t.Fatalf("expected boolean toggles true: %+v", settings)
	}
	if !settings.AILockEnabled || settings.AILockTarget != "US-Node" {
		t.Fatalf("unexpected AI lock migration: %+v", settings)
	}
	if settings.PortOverride != 7891 || settings.APIPort != 19091 || !settings.AllowLan || !settings.IPv6 {
		t.Fatalf("unexpected network settings: %+v", settings)
	}
	if settings.Language != "en_US" || settings.TrayIconPreset != "default" || settings.UpdateChannel != "lab" {
		t.Fatalf("unexpected UI settings: %+v", settings)
	}
	if !reflect.DeepEqual(settings.SuspendNetworks, []string{"Corp-WiFi", "Home-5G"}) {
		t.Fatalf("unexpected SuspendNetworks: %v", settings.SuspendNetworks)
	}
	if settings.TunMTU != 1400 || settings.TunInterfaceName != "en0" || settings.TunRouteExcludes != "100.64.0.0/10" {
		t.Fatalf("unexpected TUN settings: %+v", settings)
	}

	subBytes, err := os.ReadFile(filepath.Join(appDir, "subscriptions.json"))
	if err != nil {
		t.Fatalf("expected subscriptions.json created: %v", err)
	}
	var subs []legacyManagedSubscription
	if err := json.Unmarshal(subBytes, &subs); err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 || subs[0].Name != "LegacySub" || subs[0].URL != "https://example.com/sub.yaml" {
		t.Fatalf("unexpected migrated subscriptions: %+v", subs)
	}
	wantUpdated := int64(780000000) + appleRefEpochOffset
	if subs[0].UpdatedAt == nil || *subs[0].UpdatedAt != wantUpdated {
		t.Fatalf("UpdatedAt = %v, want %d", subs[0].UpdatedAt, wantUpdated)
	}
	if subs[0].UserInfo == nil || subs[0].UserInfo.Total == nil || *subs[0].UserInfo.Total != 1073741824 {
		t.Fatalf("unexpected migrated userinfo: %+v", subs[0].UserInfo)
	}
}

func TestNormalizeAndUnwritableLegacyRulesetFallback(t *testing.T) {
	if NormalizeLanguageCode("en") != "en_US" {
		t.Fatalf("expected en -> en_US")
	}
	if NormalizeTrayIconPreset("cat-lightning-outline") != "monochrome" {
		t.Fatalf("expected cat-lightning-outline -> monochrome")
	}

	dir := t.TempDir()
	legacyDir := filepath.Join(dir, RulesetDirName)
	if err := os.MkdirAll(legacyDir, 0755); err != nil {
		t.Fatal(err)
	}
	legacyCN := filepath.Join(legacyDir, CNAppsRuleProviderName+".list")
	if err := os.WriteFile(legacyCN, []byte("PROCESS-NAME,CustomMacApp\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(legacyDir, 0555); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(legacyDir, 0755) }()

	// Seed a settings.json containing legacy macOS values to verify auto-healing on load
	legacySettings := `{
  "selected_profile": "config",
  "tray_icon_preset": "cat-lightning-outline",
  "language": "en"
}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(legacySettings), 0600); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(dir, "")
	if err != nil {
		t.Fatalf("NewStore failed with unwritable legacy ruleset dir: %v", err)
	}
	defer store.Close()

	st := store.Settings()
	if st.Language != "en_US" || st.TrayIconPreset != "monochrome" {
		t.Fatalf("expected settings normalized on load, got lang=%q icon=%q", st.Language, st.TrayIconPreset)
	}
	if ResolveRulesetDirName(dir) != FallbackRulesetDirName {
		t.Fatalf("expected fallback ruleset dir %q, got %q", FallbackRulesetDirName, ResolveRulesetDirName(dir))
	}
	relPath := BuiltinRulesetRelPathInDir(dir, CNAppsRuleProviderName)
	if relPath != "./clashfx-rulesets/clashfx-cn-apps-direct.list" {
		t.Fatalf("unexpected fallback rel path: %q", relPath)
	}
	fallbackCN, err := os.ReadFile(filepath.Join(dir, FallbackRulesetDirName, CNAppsRuleProviderName+".list"))
	if err != nil {
		t.Fatalf("expected fallback CN ruleset created: %v", err)
	}
	if string(fallbackCN) != "PROCESS-NAME,CustomMacApp\n" {
		t.Fatalf("expected legacy CN ruleset preserved, got %q", string(fallbackCN))
	}
	if _, err := os.Stat(filepath.Join(dir, FallbackRulesetDirName, AILockRuleProviderName+".list")); err != nil {
		t.Fatalf("expected fallback AI lock ruleset created: %v", err)
	}
}
