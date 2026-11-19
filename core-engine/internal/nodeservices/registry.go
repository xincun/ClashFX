package nodeservices

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/clashfx/core-engine/internal/portprobe"
	"github.com/clashfx/core-engine/internal/profile"
)

const (
	ListenerPrefix = "clashfx-node-service-"
	StateFileName  = "node-services.json"
)

var (
	ErrInvalidSettings = errors.New("use ports 1024-65000, intervals 15-3600s, 2-10 failures, and cooldowns 60-86400s")
	ErrNoFreePort      = errors.New("no free local port is available")
	ErrInvalidNodes    = errors.New("proxy names must be non-empty and unique")
	ErrCorruptState    = errors.New("saved node port assignments cannot be read")
)

type PortConflictError struct {
	Port int
}

func (e PortConflictError) Error() string {
	return fmt.Sprintf("port %d is reserved or assigned to another node", e.Port)
}

type Settings struct {
	PortsEnabled       bool `json:"ports_enabled"`
	RecoveryEnabled    bool `json:"recovery_enabled"`
	FirstPort          int  `json:"first_port"`
	CheckIntervalSec   int  `json:"check_interval_sec"`
	FailureThreshold   int  `json:"failure_threshold"`
	RefreshCooldownSec int  `json:"refresh_cooldown_sec"`
}

func DefaultSettings() Settings {
	return Settings{
		PortsEnabled:       true,
		RecoveryEnabled:    true,
		FirstPort:          21000,
		CheckIntervalSec:   60,
		FailureThreshold:   3,
		RefreshCooldownSec: 300,
	}
}

func (s Settings) Validate() error {
	if s.FirstPort < 1024 || s.FirstPort > 65000 ||
		s.CheckIntervalSec < 15 || s.CheckIntervalSec > 3600 ||
		s.FailureThreshold < 2 || s.FailureThreshold > 10 ||
		s.RefreshCooldownSec < 60 || s.RefreshCooldownSec > 86400 {
		return ErrInvalidSettings
	}
	return nil
}

type NodePortBinding struct {
	Name      string `json:"name"`
	ServerKey string `json:"server_key"`
	Port      int    `json:"port"`
	Enabled   bool   `json:"enabled"`
}

func (b *NodePortBinding) UnmarshalJSON(data []byte) error {
	type rawBinding struct {
		Name      string `json:"name"`
		ServerKey string `json:"server_key"`
		Port      int    `json:"port"`
		Enabled   *bool  `json:"enabled"`
	}
	var raw rawBinding
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	b.Name = raw.Name
	b.ServerKey = raw.ServerKey
	b.Port = raw.Port
	if raw.Enabled == nil {
		b.Enabled = true
	} else {
		b.Enabled = *raw.Enabled
	}
	return nil
}

type Registry struct {
	Profiles   map[string][]NodePortBinding `json:"profiles"`
	DirectPort *int                         `json:"direct_port,omitempty"`
}

func NewRegistry() Registry {
	return Registry{
		Profiles: map[string][]NodePortBinding{},
	}
}

func (r *Registry) clone() Registry {
	cp := Registry{
		Profiles: make(map[string][]NodePortBinding, len(r.Profiles)),
	}
	if r.DirectPort != nil {
		v := *r.DirectPort
		cp.DirectPort = &v
	}
	for k, list := range r.Profiles {
		cp.Profiles[k] = append([]NodePortBinding(nil), list...)
	}
	return cp
}

func (r *Registry) Reconcile(
	profileKey string,
	proxies []map[string]any,
	firstPort int,
	reserved map[int]bool,
	available func(int) bool,
) ([]NodePortBinding, error) {
	if r.Profiles == nil {
		r.Profiles = map[string][]NodePortBinding{}
	}
	names := make([]string, 0, len(proxies))
	nameSet := make(map[string]bool, len(proxies))
	serverKeys := make([]string, 0, len(proxies))
	serverCounts := map[string]int{}

	for _, px := range proxies {
		name, _ := px["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" || nameSet[name] {
			continue
		}
		nameSet[name] = true
		names = append(names, name)
		sk := proxyServerKey(px)
		serverKeys = append(serverKeys, sk)
		if sk != "" {
			serverCounts[sk]++
		}
	}

	previous := r.Profiles[profileKey]
	retained := append([]NodePortBinding(nil), previous...)
	used := map[int]bool{}
	for p := range reserved {
		used[p] = true
	}
	for _, b := range previous {
		used[b.Port] = true
	}
	if r.DirectPort != nil {
		used[*r.DirectPort] = true
	}

	claimed := map[string]bool{}
	active := make([]NodePortBinding, 0, len(names))

	for i, name := range names {
		key := serverKeys[i]
		var found *NodePortBinding
		for idx := range previous {
			if previous[idx].Name == name {
				cp := previous[idx]
				found = &cp
				break
			}
		}
		// Retain port across a rename when serverKey is unambiguous (1-node-per-server)
		if found == nil && key != "" && serverCounts[key] == 1 {
			var cands []NodePortBinding
			for _, prev := range previous {
				if prev.ServerKey == key && !nameSet[prev.Name] && !claimed[prev.Name] {
					cands = append(cands, prev)
				}
			}
			if len(cands) == 1 {
				cp := cands[0]
				found = &cp
				retained = removeBindingByName(retained, cp.Name)
			}
		}

		if found != nil {
			if (found.Enabled && reserved[found.Port]) || (r.DirectPort != nil && found.Port == *r.DirectPort) {
				return nil, PortConflictError{Port: found.Port}
			}
			claimed[found.Name] = true
			found.Name = name
			found.ServerKey = key
			retained = removeBindingByName(retained, name)
			retained = append(retained, *found)
			active = append(active, *found)
			continue
		}

		port := 0
		for cand := firstPort; cand <= 65535; cand++ {
			if !used[cand] && available(cand) {
				port = cand
				break
			}
		}
		if port == 0 {
			return nil, ErrNoFreePort
		}
		used[port] = true
		entry := NodePortBinding{
			Name:      name,
			ServerKey: key,
			Port:      port,
			Enabled:   true,
		}
		retained = append(retained, entry)
		active = append(active, entry)
	}

	r.Profiles[profileKey] = retained
	return active, nil
}

func (r *Registry) SetPorts(profileKey string, changes map[string]int) error {
	return r.SetPortsWithReserved(profileKey, changes, nil)
}

func (r *Registry) SetPortsWithReserved(profileKey string, changes map[string]int, reserved map[int]bool) error {
	next := r.clone()
	for name, port := range changes {
		if port < 1024 || port > 65535 || reserved[port] || (next.DirectPort != nil && port == *next.DirectPort) {
			return PortConflictError{Port: port}
		}
		list := next.Profiles[profileKey]
		idx := -1
		for i := range list {
			if list[i].Name == name {
				idx = i
				break
			}
		}
		if idx < 0 {
			return ErrInvalidNodes
		}
		list[idx].Port = port
		next.Profiles[profileKey] = list
	}

	seenPorts := map[int]bool{}
	for _, b := range next.Profiles[profileKey] {
		if seenPorts[b.Port] {
			return PortConflictError{Port: b.Port}
		}
		seenPorts[b.Port] = true
	}
	*r = next
	return nil
}

func (r *Registry) SetEnabled(profileKey string, changes map[string]bool) error {
	next := r.clone()
	list := next.Profiles[profileKey]
	for name, en := range changes {
		idx := -1
		for i := range list {
			if list[i].Name == name {
				idx = i
				break
			}
		}
		if idx < 0 {
			return ErrInvalidNodes
		}
		list[idx].Enabled = en
	}
	next.Profiles[profileKey] = list
	*r = next
	return nil
}

func (r *Registry) PrepareDirectPort(reserved map[int]bool, coreRunning bool, available func(int) bool) (int, error) {
	if r.DirectPort != nil {
		p := *r.DirectPort
		if reserved[p] {
			return 0, PortConflictError{Port: p}
		}
		if coreRunning || available(p) {
			return p, nil
		}
	}
	occupied := map[int]bool{}
	for p := range reserved {
		occupied[p] = true
	}
	for _, list := range r.Profiles {
		for _, b := range list {
			occupied[b.Port] = true
		}
	}
	for port := 20999; port >= 20000; port-- {
		if !occupied[port] && available(port) {
			pCopy := port
			r.DirectPort = &pCopy
			return port, nil
		}
	}
	return 0, ErrNoFreePort
}

func ReservedPortsInConfig(root map[string]any) map[int]bool {
	ports := map[int]bool{}
	for _, key := range []string{"port", "socks-port", "mixed-port", "redir-port", "tproxy-port"} {
		if p := intFromAny(root[key]); p > 0 {
			ports[p] = true
		}
	}
	if ctrl, _ := root["external-controller"].(string); ctrl != "" {
		if idx := strings.LastIndex(ctrl, ":"); idx >= 0 {
			if p, err := strconv.Atoi(strings.TrimSpace(ctrl[idx+1:])); err == nil && p > 0 {
				ports[p] = true
			}
		}
	}
	if dnsMap, ok := root["dns"].(map[string]any); ok {
		if listen, _ := dnsMap["listen"].(string); listen != "" {
			if idx := strings.LastIndex(listen, ":"); idx >= 0 {
				if p, err := strconv.Atoi(strings.TrimSpace(listen[idx+1:])); err == nil && p > 0 {
					ports[p] = true
				}
			}
		}
	}
	for _, ln := range mapSliceFromAny(root["listeners"]) {
		name, _ := ln["name"].(string)
		if strings.HasPrefix(name, ListenerPrefix) {
			continue
		}
		if p := intFromAny(ln["port"]); p > 0 {
			ports[p] = true
			continue
		}
		if s, ok := ln["port"].(string); ok {
			for _, part := range strings.Split(s, "/") {
				ends := strings.Split(strings.TrimSpace(part), "-")
				if len(ends) == 1 {
					if p, err := strconv.Atoi(ends[0]); err == nil && p > 0 {
						ports[p] = true
					}
				} else if len(ends) == 2 {
					p1, e1 := strconv.Atoi(ends[0])
					p2, e2 := strconv.Atoi(ends[1])
					if e1 == nil && e2 == nil && p1 > 0 && p1 <= p2 && p2 <= 65535 {
						for p := p1; p <= p2; p++ {
							ports[p] = true
						}
					}
				}
			}
		}
	}
	return ports
}

type ProviderProxy struct {
	ProviderName string
	Proxy        map[string]any
}

func ApplyListeners(root map[string]any, bindings []NodePortBinding, directPort *int) {
	ApplyListenersWithProviders(root, bindings, directPort, nil)
}

func ApplyListenersWithProviders(root map[string]any, bindings []NodePortBinding, directPort *int, providerOwners map[string]string) {
	var listeners []any
	for _, ln := range mapSliceFromAny(root["listeners"]) {
		name, _ := ln["name"].(string)
		if !strings.HasPrefix(name, ListenerPrefix) {
			listeners = append(listeners, ln)
		}
	}

	if existingSub, ok := root["sub-rules"].(map[string]any); ok {
		subRules := map[string]any{}
		for k, v := range existingSub {
			if !strings.HasPrefix(k, ListenerPrefix) {
				subRules[k] = v
			}
		}
		if len(subRules) > 0 {
			root["sub-rules"] = subRules
		} else {
			delete(root, "sub-rules")
		}
	}

	inlineNames := map[string]bool{}
	for _, px := range mapSliceFromAny(root["proxies"]) {
		if n, _ := px["name"].(string); strings.TrimSpace(n) != "" {
			inlineNames[strings.TrimSpace(n)] = true
		}
	}

	rawGroups := mapSliceFromAny(root["proxy-groups"])
	var proxyGroups []any
	groupNames := map[string]bool{}
	groupsModified := false
	for _, g := range rawGroups {
		n, _ := g["name"].(string)
		if strings.HasPrefix(n, ListenerPrefix+"target-") {
			groupsModified = true
			continue
		}
		if strings.TrimSpace(n) != "" {
			groupNames[strings.TrimSpace(n)] = true
		}
		proxyGroups = append(proxyGroups, g)
	}

	makeListener := func(name string, port int, proxy string, udp bool) map[string]any {
		return map[string]any{
			"name":   name,
			"type":   "mixed",
			"listen": "127.0.0.1",
			"port":   port,
			"udp":    udp,
			"proxy":  proxy,
		}
	}

	for _, b := range bindings {
		if !b.Enabled {
			continue
		}
		targetProxy := b.Name
		if !inlineNames[b.Name] && !groupNames[b.Name] {
			if provName := strings.TrimSpace(providerOwners[b.Name]); provName != "" && !strings.Contains(b.Name, "`") {
				hiddenGroup := fmt.Sprintf("%starget-%d", ListenerPrefix, b.Port)
				proxyGroups = append(proxyGroups, map[string]any{
					"name":   hiddenGroup,
					"type":   "select",
					"hidden": true,
					"use":    []any{provName},
					"filter": "^" + regexp.QuoteMeta(b.Name) + "$",
				})
				targetProxy = hiddenGroup
				groupsModified = true
			}
		}
		listeners = append(listeners, makeListener(
			fmt.Sprintf("%s%d", ListenerPrefix, b.Port),
			b.Port,
			targetProxy,
			true,
		))
	}
	if directPort != nil && *directPort > 0 {
		listeners = append(listeners, makeListener(
			ListenerPrefix+"direct-refresh",
			*directPort,
			"DIRECT",
			false,
		))
	}

	if groupsModified {
		root["proxy-groups"] = proxyGroups
	}
	if len(listeners) > 0 || root["listeners"] != nil {
		root["listeners"] = listeners
	}
}

type PersistedState struct {
	Settings       Settings                     `json:"settings"`
	Registry       Registry                     `json:"registry"`
	LastRefresh    map[string]time.Time         `json:"last_refresh,omitempty"`
	ActiveNames    map[string][]string          `json:"active_names,omitempty"`
	ProviderOwners map[string]map[string]string `json:"provider_owners,omitempty"`
}

func DefaultPersistedState() PersistedState {
	return PersistedState{
		Settings:       DefaultSettings(),
		Registry:       NewRegistry(),
		LastRefresh:    map[string]time.Time{},
		ActiveNames:    map[string][]string{},
		ProviderOwners: map[string]map[string]string{},
	}
}

type Store struct {
	mu              sync.Mutex
	statePath       string
	probePort       func(port int, checkUDP bool) (bool, bool)
	customPortProbe bool
}

func NewStore(appDataDir string) *Store {
	return &Store{
		statePath: filepath.Join(appDataDir, StateFileName),
		probePort: portprobe.ProbeLoopbackPort,
	}
}

func (s *Store) SetPortProbeForTest(fn func(port int, checkUDP bool) (bool, bool)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.probePort = fn
	s.customPortProbe = true
}

func (s *Store) portProbeLocked(privilegedCore bool) func(port int, checkUDP bool) (bool, bool) {
	if s.customPortProbe {
		return s.probePort
	}
	if privilegedCore && runtime.GOOS == "darwin" {
		return portprobe.ProbeLoopbackPortForPrivilegedCore
	}
	return s.probePort
}

func ProfileKey(profileName string) string {
	profileName = strings.TrimSpace(strings.TrimSuffix(profileName, ".yaml"))
	if profileName == "" {
		profileName = "config"
	}
	return "local:" + profileName
}

func (s *Store) Read() (PersistedState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readLocked()
}

func (s *Store) readLocked() (PersistedState, error) {
	data, err := os.ReadFile(s.statePath)
	if os.IsNotExist(err) {
		return DefaultPersistedState(), nil
	}
	if err != nil {
		return PersistedState{}, err
	}
	st := DefaultPersistedState()
	if err := json.Unmarshal(data, &st); err != nil {
		return PersistedState{}, ErrCorruptState
	}
	if st.Registry.Profiles == nil {
		st.Registry.Profiles = map[string][]NodePortBinding{}
	}
	if st.LastRefresh == nil {
		st.LastRefresh = map[string]time.Time{}
	}
	if st.ActiveNames == nil {
		st.ActiveNames = map[string][]string{}
	}
	if st.ProviderOwners == nil {
		st.ProviderOwners = map[string]map[string]string{}
	}
	return st, nil
}

func (s *Store) Write(st PersistedState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeLocked(st)
}

func (s *Store) writeLocked(st PersistedState) error {
	if err := st.Settings.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return profile.WriteFileAtomic(s.statePath, data, 0600)
}

func (s *Store) UpdateProfileBindings(profileName string, portChanges map[string]int, enabledChanges map[string]bool) error {
	return s.UpdateProfileBindingsWithReserved(profileName, portChanges, enabledChanges, nil)
}

func (s *Store) UpdateProfileBindingsWithReserved(profileName string, portChanges map[string]int, enabledChanges map[string]bool, reserved map[int]bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	st, err := s.readLocked()
	if err != nil {
		return err
	}
	pKey := ProfileKey(profileName)
	if len(portChanges) > 0 {
		if err := st.Registry.SetPortsWithReserved(pKey, portChanges, reserved); err != nil {
			return err
		}
	}
	if len(enabledChanges) > 0 {
		if err := st.Registry.SetEnabled(pKey, enabledChanges); err != nil {
			return err
		}
	}
	return s.writeLocked(st)
}

func (s *Store) UpdateSettings(next Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := next.Validate(); err != nil {
		return err
	}
	st, err := s.readLocked()
	if err != nil {
		return err
	}
	st.Settings = next
	return s.writeLocked(st)
}

func (s *Store) ActiveBindings(profileName string) ([]NodePortBinding, *int, Settings, error) {
	st, err := s.Read()
	if err != nil {
		return nil, nil, Settings{}, err
	}
	pKey := ProfileKey(profileName)
	activeSet := map[string]bool{}
	for _, n := range st.ActiveNames[pKey] {
		activeSet[n] = true
	}
	var out []NodePortBinding
	for _, b := range st.Registry.Profiles[pKey] {
		if activeSet[b.Name] {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Port < out[j].Port
	})
	return out, st.Registry.DirectPort, st.Settings, nil
}

// Prepare mutates root in-place with fixed node listeners + direct-refresh listener (pure 1-port-to-1-node proxy bindings, no sub-rules).
func (s *Store) Prepare(root map[string]any, profileName string, coreRunning bool) ([]NodePortBinding, error) {
	return s.PrepareWithProviderProxies(root, profileName, coreRunning, nil)
}

func (s *Store) PrepareWithFallbackProxies(root map[string]any, profileName string, coreRunning bool, fallbackProxies []map[string]any) ([]NodePortBinding, error) {
	wrapped := make([]ProviderProxy, 0, len(fallbackProxies))
	for _, px := range fallbackProxies {
		wrapped = append(wrapped, ProviderProxy{Proxy: px})
	}
	return s.PrepareWithProviderProxies(root, profileName, coreRunning, wrapped)
}

func (s *Store) PrepareWithProviderProxies(root map[string]any, profileName string, coreRunning bool, providerProxies []ProviderProxy) ([]NodePortBinding, error) {
	return s.prepareWithProviderProxies(root, profileName, coreRunning, false, providerProxies)
}

func (s *Store) prepareWithProviderProxies(root map[string]any, profileName string, coreRunning bool, privilegedCore bool, providerProxies []ProviderProxy) ([]NodePortBinding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	st, err := s.readLocked()
	if err != nil {
		return nil, err
	}
	if err := st.Settings.Validate(); err != nil {
		return nil, err
	}

	reserved := ReservedPortsInConfig(root)
	inlineProxies := mapSliceFromAny(root["proxies"])
	seenNames := make(map[string]bool, len(inlineProxies)+len(providerProxies))
	proxies := make([]map[string]any, 0, len(inlineProxies)+len(providerProxies))
	for _, px := range inlineProxies {
		n, _ := px["name"].(string)
		n = strings.TrimSpace(n)
		if n != "" {
			seenNames[n] = true
		}
		proxies = append(proxies, px)
	}
	providerOwners := map[string]string{}
	for _, pp := range providerProxies {
		if pp.Proxy == nil {
			continue
		}
		n, _ := pp.Proxy["name"].(string)
		n = strings.TrimSpace(n)
		if n == "" || seenNames[n] {
			continue
		}
		seenNames[n] = true
		proxies = append(proxies, pp.Proxy)
		if strings.TrimSpace(pp.ProviderName) != "" {
			providerOwners[n] = strings.TrimSpace(pp.ProviderName)
		}
	}
	pKey := ProfileKey(profileName)
	probe := s.portProbeLocked(privilegedCore)

	availFn := func(port int) bool {
		if probe == nil {
			return true
		}
		ok, _ := probe(port, true)
		return ok
	}

	var bindings []NodePortBinding
	if st.Settings.PortsEnabled && len(proxies) > 0 {
		bindings, err = st.Registry.Reconcile(pKey, proxies, st.Settings.FirstPort, reserved, availFn)
		if err != nil {
			return nil, err
		}
	}

	names := make([]string, 0, len(proxies))
	seenActive := make(map[string]bool, len(proxies))
	for _, px := range proxies {
		n, _ := px["name"].(string)
		n = strings.TrimSpace(n)
		if n != "" && !seenActive[n] {
			seenActive[n] = true
			names = append(names, n)
		}
	}
	st.ActiveNames[pKey] = names
	st.ProviderOwners[pKey] = providerOwners

	// Always allocate DirectPort whenever node listeners are active so manual and
	// scheduled subscription refreshes can bypass TUN/dead nodes via DIRECT even
	// if background 204 auto-recovery is disabled.
	dp, err := st.Registry.PrepareDirectPort(reserved, coreRunning, func(port int) bool {
		if probe == nil {
			return true
		}
		ok, _ := probe(port, false)
		return ok
	})
	if err != nil {
		return nil, err
	}
	directPtr := &dp

	var appliedBindings []NodePortBinding
	if st.Settings.PortsEnabled {
		appliedBindings = bindings
	}
	ApplyListenersWithProviders(root, appliedBindings, directPtr, providerOwners)

	if err := s.writeLocked(st); err != nil {
		return nil, err
	}
	return bindings, nil
}

func (s *Store) TransformHookForController(isCoreRunning func() bool) func(root map[string]any, settings profile.AppSettings) error {
	return s.TransformHookForControllerWithProviderProxies(isCoreRunning, nil)
}

func (s *Store) TransformHookForControllerWithProviders(
	isCoreRunning func() bool,
	fallbackProxiesLoader func(root map[string]any) []map[string]any,
) func(root map[string]any, settings profile.AppSettings) error {
	var wrapped func(root map[string]any) []ProviderProxy
	if fallbackProxiesLoader != nil {
		wrapped = func(root map[string]any) []ProviderProxy {
			raw := fallbackProxiesLoader(root)
			out := make([]ProviderProxy, 0, len(raw))
			for _, px := range raw {
				out = append(out, ProviderProxy{Proxy: px})
			}
			return out
		}
	}
	return s.TransformHookForControllerWithProviderProxies(isCoreRunning, wrapped)
}

func (s *Store) TransformHookForControllerWithProviderProxies(
	isCoreRunning func() bool,
	providerProxiesLoader func(root map[string]any) []ProviderProxy,
) func(root map[string]any, settings profile.AppSettings) error {
	return func(root map[string]any, settings profile.AppSettings) error {
		if settings.EnhancedMode && settings.CustomConfigAsIs {
			return nil
		}
		running := false
		if isCoreRunning != nil {
			running = isCoreRunning()
		}
		var provs []ProviderProxy
		if providerProxiesLoader != nil {
			provs = providerProxiesLoader(root)
		}
		_, err := s.prepareWithProviderProxies(root, settings.SelectedProfile, running, settings.EnhancedMode, provs)
		return err
	}
}

func proxyServerKey(px map[string]any) string {
	srv, _ := px["server"].(string)
	srv = strings.TrimSpace(srv)
	if srv == "" {
		return ""
	}
	pType, _ := px["type"].(string)
	return strings.ToLower(strings.TrimSpace(pType)) + "|" + strings.ToLower(srv)
}

func removeBindingByName(list []NodePortBinding, name string) []NodePortBinding {
	out := make([]NodePortBinding, 0, len(list))
	for _, item := range list {
		if item.Name != name {
			out = append(out, item)
		}
	}
	return out
}

func mapSliceFromAny(v any) []map[string]any {
	switch typed := v.(type) {
	case []map[string]any:
		return typed
	case []any:
		var out []map[string]any
		for _, item := range typed {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	default:
		return nil
	}
}

func intFromAny(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case string:
		p, _ := strconv.Atoi(strings.TrimSpace(n))
		return p
	default:
		return 0
	}
}
