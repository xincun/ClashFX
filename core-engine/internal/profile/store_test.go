package profile

import (
	"os"
	"path/filepath"
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

	// Default config.yaml created automatically
	profiles, err := store.ListProfiles()
	if err != nil || len(profiles) != 1 || profiles[0] != "config" {
		t.Fatalf("ListProfiles = %v, %v; want [config]", profiles, err)
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
	if _, err := os.Stat(filepath.Join(customDir, "config.yaml")); err != nil {
		t.Fatalf("expected starter config in custom dir: %v", err)
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

func assertProfilesEqual(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("ListProfiles = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ListProfiles = %v, want %v", got, want)
		}
	}
}

func TestListProfilesHidesUnmodifiedStarterConfigWhenInactive(t *testing.T) {
	store, err := NewStore(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// 1. Fresh store: SelectedProfile == "config" -> [config]
	profs, err := store.ListProfiles()
	if err != nil {
		t.Fatal(err)
	}
	assertProfilesEqual(t, profs, "config")

	// 2. Active subscription present while config.yaml is unmodified starter -> omit config
	dir := store.ActiveConfigDir()
	if err := WriteFileAtomic(filepath.Join(dir, "sub1.yaml"), []byte("mixed-port: 7890\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateSettings(func(st *AppSettings) error {
		st.SelectedProfile = "sub1"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	profs, err = store.ListProfiles()
	if err != nil {
		t.Fatal(err)
	}
	assertProfilesEqual(t, profs, "sub1")

	// 3. User adds a custom local YAML -> retained while unmodified starter config stays hidden
	if err := WriteFileAtomic(filepath.Join(dir, "custom-local.yaml"), []byte("mixed-port: 7890\n"), 0600); err != nil {
		t.Fatal(err)
	}
	profs, err = store.ListProfiles()
	if err != nil {
		t.Fatal(err)
	}
	assertProfilesEqual(t, profs, "custom-local", "sub1")

	// 4. User modifies config.yaml -> config.yaml is no longer starter template, so keep it visible
	customCfg := "mixed-port: 7890\nproxies:\n  - name: n1\n    type: direct\n"
	if err := WriteFileAtomic(filepath.Join(dir, "config.yaml"), []byte(customCfg), 0600); err != nil {
		t.Fatal(err)
	}
	profs, err = store.ListProfiles()
	if err != nil {
		t.Fatal(err)
	}
	assertProfilesEqual(t, profs, "config", "custom-local", "sub1")
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
