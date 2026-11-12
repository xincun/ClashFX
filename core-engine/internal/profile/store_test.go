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
