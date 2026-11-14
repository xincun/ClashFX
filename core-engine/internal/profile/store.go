package profile

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

const DefaultWindowsProxyBypass = "<local>;localhost;127.*;10.*;172.16.*;172.17.*;172.18.*;172.19.*;172.20.*;172.21.*;172.22.*;172.23.*;172.24.*;172.25.*;172.26.*;172.27.*;172.28.*;172.29.*;172.30.*;172.31.*;192.168.*"

const DefaultStarterConfigYAML = `mixed-port: 7890
allow-lan: false
mode: rule
log-level: info
ipv6: false
external-controller: 127.0.0.1:19090
secret: ""
dns:
  enable: true
  listen: 127.0.0.1:1053
  enhanced-mode: fake-ip
  fake-ip-range: 198.18.0.1/16
  default-nameserver:
    - 114.114.114.114
    - 223.5.5.5
  nameserver:
    - https://doh.pub/dns-query
    - https://223.5.5.5/dns-query
proxies: []
proxy-groups:
  - name: Proxy
    type: select
    proxies:
      - DIRECT
rules:
  - DOMAIN,localhost,DIRECT
  - DOMAIN-SUFFIX,local,DIRECT
  - IP-CIDR,127.0.0.0/8,DIRECT,no-resolve
  - IP-CIDR,10.0.0.0/8,DIRECT,no-resolve
  - IP-CIDR,172.16.0.0/12,DIRECT,no-resolve
  - IP-CIDR,192.168.0.0/16,DIRECT,no-resolve
  - MATCH,Proxy
`

type HotkeySettings struct {
	ToggleSystemProxy  string `json:"toggle_system_proxy"`
	SwitchOutboundMode string `json:"switch_outbound_mode"`
}

type AILockPreLockState struct {
	OutboundMode       string `json:"outbound_mode"`
	EnhancedMode       bool   `json:"enhanced_mode"`
	SystemProxyEnabled bool   `json:"system_proxy_enabled"`
}

type AppSettings struct {
	SelectedProfile       string                       `json:"selected_profile"`
	CustomConfigDir       string                       `json:"custom_config_dir"`
	OutboundMode          string                       `json:"outbound_mode"`
	AllowLan              bool                         `json:"allow_lan"`
	IPv6                  bool                         `json:"ipv6"`
	SystemProxyEnabled    bool                         `json:"system_proxy_enabled"`
	EnhancedMode          bool                         `json:"enhanced_mode"`
	CustomConfigAsIs      bool                         `json:"custom_config_as_is"`
	BypassCNApps          bool                         `json:"bypass_cn_apps"`
	PortOverride          int                          `json:"port_override"`
	APIPort               int                          `json:"api_port"`
	APISecret             string                       `json:"api_secret"`
	LogLevel              string                       `json:"log_level"`
	TunMTU                uint32                       `json:"tun_mtu"`
	TunStack              string                       `json:"tun_stack"`
	TunInterfaceName      string                       `json:"tun_interface_name"`
	TunRouteExcludes      string                       `json:"tun_route_excludes"`
	ProxyBypassList       string                       `json:"proxy_bypass_list"`
	AutoUpdateSubs        bool                         `json:"auto_update_subs"`
	AutoUpdateIntervalSec int                          `json:"auto_update_interval_sec"`
	AILockEnabled         bool                         `json:"claudeProxyLockEnabled"`
	AILockTarget          string                       `json:"claudeProxyLockTarget"`
	AILockSavedState      *AILockPreLockState          `json:"ai_lock_saved_state,omitempty"`
	TrayIconPreset        string                       `json:"tray_icon_preset"`
	Language              string                       `json:"language"`
	LaunchAtLogin         bool                         `json:"launch_at_login"`
	Hotkeys               HotkeySettings               `json:"hotkeys"`
	SuspendNetworks       []string                     `json:"suspend_networks"`
	UpdateChannel         string                       `json:"update_channel"`
	BenchmarkURL          string                       `json:"benchmark_url"`
	MMDBDownloadURL       string                       `json:"mmdb_download_url"`
	GeoSiteDownloadURL    string                       `json:"geosite_download_url"`
	SavedSelections       map[string]map[string]string `json:"saved_selections"`
}

func DefaultSettings() AppSettings {
	return AppSettings{
		SelectedProfile:       "config",
		OutboundMode:          "rule",
		APIPort:               19090,
		LogLevel:              "info",
		TunMTU:                1500,
		TunStack:              "mixed",
		ProxyBypassList:       DefaultWindowsProxyBypass,
		AutoUpdateSubs:        true,
		AutoUpdateIntervalSec: 7200,
		TrayIconPreset:        "default",
		Language:              "zh_CN",
		UpdateChannel:         "stable",
		BenchmarkURL:          "https://cp.cloudflare.com/generate_204",
		MMDBDownloadURL:       "https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/country.mmdb",
		GeoSiteDownloadURL:    "https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/geosite.dat",
		SavedSelections:       map[string]map[string]string{},
	}
}

func DefaultAppDataDir() (string, error) {
	if runtime.GOOS == "windows" {
		if appData := strings.TrimSpace(os.Getenv("APPDATA")); appData != "" {
			return filepath.Join(appData, "ClashFX"), nil
		}
		cfgDir, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(cfgDir, "ClashFX"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "clashfx"), nil
}

type Store struct {
	mu          sync.RWMutex
	appDataDir  string
	resourceDir string
	settings    AppSettings

	watchMu    sync.Mutex
	watcher    *fsnotify.Watcher
	watchStop  chan struct{}
	watchedDir string

	internalWriteMu sync.Mutex
	internalWrites  map[string]time.Time
}

func NewStore(appDataDir, bundledResourceDir string) (*Store, error) {
	if strings.TrimSpace(appDataDir) == "" {
		var err error
		appDataDir, err = DefaultAppDataDir()
		if err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(appDataDir, 0700); err != nil {
		return nil, fmt.Errorf("create appDataDir %s: %w", appDataDir, err)
	}
	s := &Store{
		appDataDir:  appDataDir,
		resourceDir: bundledResourceDir,
		settings:    DefaultSettings(),
	}
	if err := s.loadSettingsLocked(); err != nil {
		return nil, err
	}
	if err := s.EnsureWorkspaceReady(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) AppDataDir() string {
	return s.appDataDir
}

func (s *Store) ActiveConfigDir() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.activeConfigDirLocked()
}

func (s *Store) activeConfigDirLocked() string {
	if custom := strings.TrimSpace(s.settings.CustomConfigDir); custom != "" {
		return filepath.Clean(custom)
	}
	return s.appDataDir
}

func (s *Store) Settings() AppSettings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	clone := s.settings
	clone.SuspendNetworks = append([]string(nil), s.settings.SuspendNetworks...)
	clone.SavedSelections = make(map[string]map[string]string, len(s.settings.SavedSelections))
	for prof, groups := range s.settings.SavedSelections {
		m := make(map[string]string, len(groups))
		for g, t := range groups {
			m[g] = t
		}
		clone.SavedSelections[prof] = m
	}
	return clone
}

func (s *Store) UpdateSettings(mutator func(*AppSettings) error) (AppSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.settings
	if next.SavedSelections == nil {
		next.SavedSelections = map[string]map[string]string{}
	}
	if err := mutator(&next); err != nil {
		return s.settings, err
	}
	if strings.TrimSpace(next.SelectedProfile) == "" {
		next.SelectedProfile = "config"
	}
	if next.APIPort < 1 || next.APIPort > 65535 {
		next.APIPort = 19090
	}
	if next.TunMTU == 0 {
		next.TunMTU = 1500
	}
	if strings.TrimSpace(next.CustomConfigDir) != "" {
		cleanCustom := filepath.Clean(strings.TrimSpace(next.CustomConfigDir))
		if !filepath.IsAbs(cleanCustom) {
			return s.settings, fmt.Errorf("custom_config_dir must be an absolute path: %q", next.CustomConfigDir)
		}
		if err := os.MkdirAll(cleanCustom, 0700); err != nil {
			return s.settings, fmt.Errorf("prepare custom_config_dir %s: %w", cleanCustom, err)
		}
		next.CustomConfigDir = cleanCustom
	}
	if err := writeJSONAtomic(s.settingsPathLocked(), next); err != nil {
		return s.settings, err
	}
	s.settings = next
	_ = s.ensureDirResourcesLocked(s.activeConfigDirLocked())
	return s.settings, nil
}

func (s *Store) RememberProxySelection(profileName, group, target string) error {
	profileName = strings.TrimSpace(profileName)
	group = strings.TrimSpace(group)
	target = strings.TrimSpace(target)
	if profileName == "" || group == "" || target == "" {
		return fmt.Errorf("profile, group, and target must be non-empty")
	}
	_, err := s.UpdateSettings(func(st *AppSettings) error {
		if st.SavedSelections[profileName] == nil {
			st.SavedSelections[profileName] = map[string]string{}
		}
		st.SavedSelections[profileName][group] = target
		return nil
	})
	return err
}

func (s *Store) ClearProxySelection(profileName, group string) error {
	_, err := s.UpdateSettings(func(st *AppSettings) error {
		if st.SavedSelections[profileName] != nil {
			delete(st.SavedSelections[profileName], group)
		}
		return nil
	})
	return err
}

func (s *Store) RecoverySelectionsForProfile(profileName string) map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	src := s.settings.SavedSelections[profileName]
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]string, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func (s *Store) RenameProfileReferences(oldName, newName string) error {
	if oldName == newName || oldName == "" || newName == "" {
		return nil
	}
	_, err := s.UpdateSettings(func(st *AppSettings) error {
		if st.SelectedProfile == oldName {
			st.SelectedProfile = newName
		}
		if oldMap, ok := st.SavedSelections[oldName]; ok {
			if st.SavedSelections[newName] == nil {
				st.SavedSelections[newName] = oldMap
			}
			delete(st.SavedSelections, oldName)
		}
		return nil
	})
	return err
}

func (s *Store) ProfilePath(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "config"
	}
	name = strings.TrimSuffix(name, ".yaml")
	return filepath.Join(s.ActiveConfigDir(), name+".yaml")
}

func (s *Store) SelectedProfilePath() string {
	st := s.Settings()
	return s.ProfilePath(st.SelectedProfile)
}

func (s *Store) ProfileMixinPath() string {
	dir := s.ActiveConfigDir()
	namedPath := filepath.Join(dir, ProfileMixinNamedFile)
	if _, err := os.Stat(namedPath); err == nil {
		return namedPath
	}
	return filepath.Join(dir, ProfileMixinDotFile)
}

func normalizeWatchPath(p string) string {
	clean := filepath.Clean(strings.TrimSpace(p))
	if runtime.GOOS == "windows" {
		return strings.ToLower(clean)
	}
	return clean
}

// MarkInternalWrite records paths modified directly by ClashFX so WatchActiveConfigDir
// suppresses redundant self-triggered reloads while still detecting external user edits.
func (s *Store) MarkInternalWrite(paths ...string) {
	if len(paths) == 0 {
		return
	}
	now := time.Now()
	s.internalWriteMu.Lock()
	defer s.internalWriteMu.Unlock()
	if s.internalWrites == nil {
		s.internalWrites = make(map[string]time.Time)
	}
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			continue
		}
		s.internalWrites[normalizeWatchPath(p)] = now
	}
}

func (s *Store) isRecentInternalWrite(path string) bool {
	const suppressWindow = 1200 * time.Millisecond
	key := normalizeWatchPath(path)
	now := time.Now()
	s.internalWriteMu.Lock()
	defer s.internalWriteMu.Unlock()
	if len(s.internalWrites) == 0 {
		return false
	}
	for k, ts := range s.internalWrites {
		if now.Sub(ts) > suppressWindow {
			delete(s.internalWrites, k)
		}
	}
	ts, ok := s.internalWrites[key]
	return ok && now.Sub(ts) <= suppressWindow
}

func (s *Store) EnsureProfileMixinFile() (string, error) {
	return s.ensureProfileMixinInDir(s.ActiveConfigDir())
}

func (s *Store) ensureProfileMixinInDir(dir string) (string, error) {
	namedPath := filepath.Join(dir, ProfileMixinNamedFile)
	path := filepath.Join(dir, ProfileMixinDotFile)
	if _, err := os.Stat(namedPath); err == nil {
		path = namedPath
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) || (err == nil && IsLegacyUntouchedProfileMixin(raw)) {
		s.MarkInternalWrite(path)
		if writeErr := WriteFileAtomic(path, []byte(DefaultProfileMixinTemplate), 0600); writeErr != nil {
			return "", writeErr
		}
	} else if err != nil {
		return "", err
	}
	return path, nil
}

func (s *Store) ReadProfileMixinBytes() ([]byte, error) {
	dir := s.ActiveConfigDir()
	for _, name := range []string{ProfileMixinNamedFile, ProfileMixinDotFile} {
		p := filepath.Join(dir, name)
		data, err := os.ReadFile(p)
		if err == nil {
			return data, nil
		}
		if !os.IsNotExist(err) {
			return nil, err
		}
	}
	return nil, nil
}

// IsDefaultStarterConfigFile reports whether the given file still matches the
// auto-generated starter config.yaml template byte-for-byte (ignoring CRLF/whitespace).
func IsDefaultStarterConfigFile(path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	normalize := func(s string) string {
		return strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
	}
	return normalize(string(raw)) == normalize(DefaultStarterConfigYAML)
}

func (s *Store) ListProfiles() ([]string, error) {
	dir := s.ActiveConfigDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	selected := s.Settings().SelectedProfile
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		fn := entry.Name()
		if strings.HasPrefix(fn, ".") || !strings.HasSuffix(strings.ToLower(fn), ".yaml") {
			continue
		}
		if IsProfileMixinFileName(fn) {
			continue
		}
		stem := strings.TrimSuffix(fn, filepath.Ext(fn))
		// Hide the auto-generated unmodified starter config.yaml when another
		// profile is active so empty fallback templates don't clutter UI lists.
		if strings.EqualFold(stem, "config") && !strings.EqualFold(selected, "config") && IsDefaultStarterConfigFile(filepath.Join(dir, fn)) {
			continue
		}
		names = append(names, stem)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return []string{"config"}, nil
	}
	return names, nil
}

func (s *Store) EnsureWorkspaceReady() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ensureDirResourcesLocked(s.activeConfigDirLocked())
}

func (s *Store) ensureDirResourcesLocked(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	defaultCfg := filepath.Join(dir, "config.yaml")
	if _, err := os.Stat(defaultCfg); os.IsNotExist(err) {
		s.MarkInternalWrite(defaultCfg)
		if err := WriteFileAtomic(defaultCfg, []byte(DefaultStarterConfigYAML), 0600); err != nil {
			return err
		}
	}
	if err := EnsureBuiltinRulesetFiles(dir); err != nil {
		return err
	}
	if _, err := s.ensureProfileMixinInDir(dir); err != nil {
		return err
	}
	if s.resourceDir != "" {
		unpackIfMissing(filepath.Join(s.resourceDir, "Country.mmdb.gz"), filepath.Join(dir, "Country.mmdb"))
		unpackIfMissing(filepath.Join(s.resourceDir, "geosite.dat.gz"), filepath.Join(dir, "geosite.dat"))
		copyIfMissing(filepath.Join(s.resourceDir, "Country.mmdb"), filepath.Join(dir, "Country.mmdb"))
		copyIfMissing(filepath.Join(s.resourceDir, "geosite.dat"), filepath.Join(dir, "geosite.dat"))
	}
	return nil
}

// WatchActiveConfigDir watches user-facing YAML and mixin edits via fsnotify
// with a 300ms debounce, ignoring internal hidden runtime files (.runtime-*).
func (s *Store) WatchActiveConfigDir(onChange func(changedFile string)) error {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()

	targetDir := s.ActiveConfigDir()
	if s.watcher != nil && s.watchedDir == targetDir {
		return nil
	}
	s.stopWatcherLocked()

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	if err := watcher.Add(targetDir); err != nil {
		_ = watcher.Close()
		return err
	}
	stopCh := make(chan struct{})
	s.watcher = watcher
	s.watchStop = stopCh
	s.watchedDir = targetDir

	go func() {
		var debounce *time.Timer
		var lastFile string
		for {
			select {
			case <-stopCh:
				if debounce != nil {
					debounce.Stop()
				}
				return
			case ev, ok := <-watcher.Events:
				if !ok {
					return
				}
				base := filepath.Base(ev.Name)
				lower := strings.ToLower(base)
				// Ignore internal .runtime-* / temp files, but DO watch .profile_mixin.yaml!
				if strings.HasPrefix(base, ".") && base != ProfileMixinDotFile {
					continue
				}
				if !strings.HasSuffix(lower, ".yaml") {
					continue
				}
				if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
					continue
				}
				if s.isRecentInternalWrite(ev.Name) {
					continue
				}
				lastFile = ev.Name
				if debounce != nil {
					debounce.Stop()
				}
				captured := lastFile
				debounce = time.AfterFunc(300*time.Millisecond, func() {
					if s.isRecentInternalWrite(captured) {
						return
					}
					if onChange != nil {
						onChange(captured)
					}
				})
			case _, ok := <-watcher.Errors:
				if !ok {
					return
				}
			}
		}
	}()
	return nil
}

func (s *Store) Close() {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	s.stopWatcherLocked()
}

func (s *Store) stopWatcherLocked() {
	if s.watchStop != nil {
		close(s.watchStop)
		s.watchStop = nil
	}
	if s.watcher != nil {
		_ = s.watcher.Close()
		s.watcher = nil
	}
	s.watchedDir = ""
}

func (s *Store) settingsPathLocked() string {
	return filepath.Join(s.appDataDir, "settings.json")
}

func (s *Store) loadSettingsLocked() error {
	data, err := os.ReadFile(s.settingsPathLocked())
	if os.IsNotExist(err) {
		return writeJSONAtomic(s.settingsPathLocked(), s.settings)
	}
	if err != nil {
		return err
	}
	loaded := DefaultSettings()
	if err := json.Unmarshal(data, &loaded); err != nil {
		return fmt.Errorf("corrupt settings.json: %w", err)
	}
	if loaded.SavedSelections == nil {
		loaded.SavedSelections = map[string]map[string]string{}
	}
	s.settings = loaded
	return nil
}

func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".clashfx-tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	_ = os.Chmod(tmpPath, perm)
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func writeJSONAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(path, data, 0600)
}

func unpackIfMissing(gzPath, dstPath string) {
	if _, err := os.Stat(dstPath); err == nil {
		return
	}
	src, err := os.Open(gzPath)
	if err != nil {
		return
	}
	defer src.Close()
	zr, err := gzip.NewReader(src)
	if err != nil {
		return
	}
	defer zr.Close()
	data, err := io.ReadAll(zr)
	if err != nil || len(data) == 0 {
		return
	}
	_ = WriteFileAtomic(dstPath, data, 0644)
}

func copyIfMissing(srcPath, dstPath string) {
	if _, err := os.Stat(dstPath); err == nil {
		return
	}
	data, err := os.ReadFile(srcPath)
	if err != nil || len(data) == 0 {
		return
	}
	_ = WriteFileAtomic(dstPath, data, 0644)
}
