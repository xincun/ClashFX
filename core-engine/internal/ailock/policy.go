package ailock

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/clashfx/core-engine/internal/profile"
)

const (
	InlineRuleProviderName         = "clashfx-ai-lock"
	InternalDirectRuleProviderName = "clashfx-internal-direct"
)

const ProcessNamePattern = `(?i)^(claude|codex|codex-app-server|chatgpt|antigravity|antigravity-cli|agy)( helper.*)?(\.exe)?$`

const WindowsProcessPathPattern = `(?i)([\\/](Programs|Program Files( \(x86\))?)[\\/](Claude|AnthropicClaude|Codex|ChatGPT|Antigravity)[\\/].*|[\\/]AppData[\\/]Local[\\/]AnthropicClaude[\\/].*|[\\/]WindowsApps[\\/][^\\/]*(Claude|Anthropic|OpenAI|ChatGPT|Codex|Antigravity)[^\\/]*[\\/].*|(^|[\\/])\.local[\\/](bin|share)[\\/]claude([\\/].*|\.exe)?|(^|[\\/])(\.antigravity|\.gemini[\\/]antigravity-cli)[\\/].*)$`

var ProtectedDomains = []string{
	"claude.ai", "claude.com", "anthropic.com", "claudeusercontent.com",
	"openai.com", "chatgpt.com", "oaistatic.com", "oaiusercontent.com", "oaistatsig.com", "openaimerge.com",
	"antigravity.google",
}

var ProtectedExactDomains = []string{
	"accounts.google.com", "oauth2.googleapis.com", "generativelanguage.googleapis.com",
	"cloudcode-pa.googleapis.com", "businessaicode.googleapis.com",
	"businessaicode.us.rep.googleapis.com", "businessaicode.eu.rep.googleapis.com", "businessaicode.mtls.googleapis.com",
	"aiplatform.googleapis.com", "aiplatform.us.rep.googleapis.com", "aiplatform.eu.rep.googleapis.com", "aiplatform.mtls.googleapis.com",
	"discoveryengine.googleapis.com", "us-discoveryengine.googleapis.com", "eu-discoveryengine.googleapis.com",
	"discoveryengine.mtls.googleapis.com", "us-discoveryengine.mtls.googleapis.com", "eu-discoveryengine.mtls.googleapis.com",
}

type ProviderSnapshot struct {
	Name                string           `json:"name"`
	DialerProxy         string           `json:"dialer_proxy,omitempty"`
	OverrideDialerProxy string           `json:"override_dialer_proxy,omitempty"`
	Proxies             []map[string]any `json:"proxies"`
}

type ApplyOutcome struct {
	Applied         bool   `json:"applied"`
	ServerHost      string `json:"server_host,omitempty"`
	RelayProxy      string `json:"relay_proxy,omitempty"`
	RejectionReason string `json:"rejection_reason,omitempty"`
}

type CandidateNode struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Server   string `json:"server,omitempty"`
	Provider string `json:"provider,omitempty"`
}

// RequiresEnhancedMode mirrors ClaudeProxyLockPolicy.requiresEnhancedMode.
func RequiresEnhancedMode(lockEnabled, preferredEnhancedMode bool) bool {
	return lockEnabled || preferredEnhancedMode
}

// EnforceProtectedSettings ensures Rule mode + Enhanced TUN + System Proxy stay
// enabled whenever AI Proxy Lock is active.
func EnforceProtectedSettings(s *profile.AppSettings) {
	if s == nil || !s.AILockEnabled {
		return
	}
	s.OutboundMode = "rule"
	s.EnhancedMode = true
	s.SystemProxyEnabled = true
}

func IsValidTarget(target string) bool {
	trimmed := strings.TrimSpace(target)
	if trimmed == "" || strings.ContainsAny(trimmed, ",\r\n") {
		return false
	}
	switch strings.ToUpper(trimmed) {
	case "DIRECT", "REJECT", "COMPATIBLE", "PASS", "REJECT-DROP":
		return false
	}
	return true
}

// RuleProviderPayload returns the deduplicated classical matchers (without target)
// registered inside the inline clashfx-ai-lock rule-provider.
func RuleProviderPayload() []string {
	out := make([]string, 0, 2+len(ProtectedDomains)+len(ProtectedExactDomains))
	out = append(out, "PROCESS-NAME-REGEX,"+ProcessNamePattern)
	out = append(out, "PROCESS-PATH-REGEX,"+WindowsProcessPathPattern)
	for _, dom := range ProtectedDomains {
		out = append(out, "DOMAIN-SUFFIX,"+dom)
	}
	for _, exact := range ProtectedExactDomains {
		out = append(out, "DOMAIN,"+exact)
	}
	return out
}

// Rules returns fail-closed [RULE-SET -> target, RULE-SET -> REJECT] top-level rules
// backed by InlineRuleProviderName so AI traffic never falls through to DIRECT/general rules
// even when UDP lacks support.
func Rules(target string) []string {
	if !IsValidTarget(target) {
		return nil
	}
	trimmed := strings.TrimSpace(target)
	return []string{
		fmt.Sprintf("RULE-SET,%s,%s", InlineRuleProviderName, trimmed),
		fmt.Sprintf("RULE-SET,%s,REJECT", InlineRuleProviderName),
	}
}

// ListCandidateTargets enumerates concrete proxy nodes (excluding proxy-groups
// and built-in pseudo-outbounds) suitable for AI Proxy Lock selection.
func ListCandidateTargets(root map[string]any, providers []ProviderSnapshot) []CandidateNode {
	groupNames := map[string]bool{}
	for _, g := range extractMapList(root["proxy-groups"]) {
		if n, _ := g["name"].(string); strings.TrimSpace(n) != "" {
			groupNames[strings.TrimSpace(n)] = true
		}
	}

	seen := map[string]bool{}
	var out []CandidateNode

	for _, p := range extractMapList(root["proxies"]) {
		name, _ := p["name"].(string)
		name = strings.TrimSpace(name)
		if !IsValidTarget(name) || groupNames[name] || seen[name] {
			continue
		}
		pType, _ := p["type"].(string)
		srv, _ := p["server"].(string)
		seen[name] = true
		out = append(out, CandidateNode{
			Name:   name,
			Type:   pType,
			Server: normalizedServerHost(srv),
		})
	}

	defs, _ := toStringMap(root["proxy-providers"])
	for _, snap := range providers {
		var def map[string]any
		if defs != nil {
			def, _ = toStringMap(defs[snap.Name])
		}
		res := resolveProvider(snap, def)
		for _, rec := range res.proxies {
			if !IsValidTarget(rec.name) || groupNames[rec.name] || seen[rec.name] {
				continue
			}
			pType, _ := rec.proxy["type"].(string)
			srv, _ := rec.proxy["server"].(string)
			seen[rec.name] = true
			out = append(out, CandidateNode{
				Name:     rec.name,
				Type:     pType,
				Server:   normalizedServerHost(srv),
				Provider: snap.Name,
			})
		}
	}

	return out
}

// RelayName returns the group selected by MATCH when all its dependencies are safe and acyclic.
func RelayName(root map[string]any, lockedTarget string, providers []ProviderSnapshot) string {
	rules, ok := extractStringListStrict(root["rules"])
	if !ok {
		return ""
	}
	var rawMatch string
	for i := len(rules) - 1; i >= 0; i-- {
		if m, found := matchTarget(rules[i]); found {
			rawMatch = m
			break
		}
	}
	name := strings.TrimSpace(rawMatch)
	if name == "" || name == "DIRECT" || name == "REJECT" || name == lockedTarget {
		return ""
	}
	graph := buildDependencyGraph(root, providers)
	if !graph.knows(name) || !graph.isSafe(name, lockedTarget) {
		return ""
	}
	return name
}

// Apply validates the locked concrete proxy target, wires safe MATCH relay
// dialer-proxy if available, forces rule mode + find-process-mode: always, and
// prepends fail-closed AI Proxy Lock rules (keeping internal ClashFX binary
// self-loop protections ahead when Enhanced Mode is active).
func Apply(root map[string]any, target string, providers []ProviderSnapshot) ApplyOutcome {
	injected := Rules(target)
	if len(injected) == 0 {
		return rejected("The locked proxy target is invalid.")
	}
	trimmed := strings.TrimSpace(target)

	// Reject proxy-groups explicitly: AI Proxy Lock must pin a concrete exit node,
	// never a mutable/auto-switching group!
	for _, g := range extractMapList(root["proxy-groups"]) {
		if gName, _ := g["name"].(string); strings.TrimSpace(gName) == trimmed {
			return rejected("AI Proxy Lock requires a concrete proxy node, not a proxy group.")
		}
	}

	initialGraph := buildDependencyGraph(root, providers)
	if !initialGraph.knows(trimmed) {
		return rejected("The locked proxy target could not be resolved.")
	}
	if !initialGraph.isSafe(trimmed, "") {
		return rejected("The locked proxy has cyclic or unresolved dialer dependencies.")
	}

	located := locateProxy(root, trimmed, providers)
	finalRoot := deepCopyMap(root)
	var appliedRelay string

	if relay := RelayName(root, trimmed, providers); relay != "" {
		proposed := deepCopyMap(root)
		if assignRelay(proposed, trimmed, located, relay) {
			finalGraph := buildDependencyGraph(proposed, providers)
			if finalGraph.isSafe(trimmed, "") {
				finalRoot = proposed
				appliedRelay = relay
			}
		}
	}

	existingRules, _ := extractStringListStrict(finalRoot["rules"])

	// Keep ClashFX's own internal executables ahead of PROCESS-PATH/DOMAIN rules so
	// mihomo_core.exe / clashfx-daemon.exe / clashfx-service.exe never self-loop.
	var leadingInternal []string
	var remainder []string
	for _, r := range existingRules {
		if isInternalCoreDirectRule(r) {
			leadingInternal = append(leadingInternal, r)
		} else {
			remainder = append(remainder, r)
		}
	}

	injectedSet := make(map[string]bool, len(injected)+len(leadingInternal))
	outRules := make([]any, 0, len(leadingInternal)+len(injected)+len(remainder))
	for _, r := range leadingInternal {
		if !injectedSet[r] {
			injectedSet[r] = true
			outRules = append(outRules, r)
		}
	}
	for _, r := range injected {
		if !injectedSet[r] {
			injectedSet[r] = true
			outRules = append(outRules, r)
		}
	}
	for _, r := range remainder {
		if !injectedSet[r] {
			injectedSet[r] = true
			outRules = append(outRules, r)
		}
	}

	rpMap, ok := toStringMap(finalRoot["rule-providers"])
	if !ok || rpMap == nil {
		rpMap = map[string]any{}
	}
	rpMap[InlineRuleProviderName] = map[string]any{
		"type":     "inline",
		"behavior": "classical",
		"payload":  RuleProviderPayload(),
	}
	finalRoot["rule-providers"] = rpMap
	finalRoot["mode"] = "rule"
	finalRoot["find-process-mode"] = "always"
	finalRoot["rules"] = outRules

	for k := range root {
		delete(root, k)
	}
	for k, v := range finalRoot {
		root[k] = v
	}

	var serverHost string
	if located != nil {
		serverHost = located.server
	}
	return ApplyOutcome{
		Applied:    true,
		ServerHost: serverHost,
		RelayProxy: appliedRelay,
	}
}

// TransformHookForController returns a transaction.TransformHook-compatible closure.
func TransformHookForController(providerLoader func() []ProviderSnapshot) func(root map[string]any, settings profile.AppSettings) error {
	return func(root map[string]any, settings profile.AppSettings) error {
		if !settings.AILockEnabled {
			return nil
		}
		var providers []ProviderSnapshot
		if providerLoader != nil {
			providers = providerLoader()
		}
		outcome := Apply(root, settings.AILockTarget, providers)
		if !outcome.Applied {
			return fmt.Errorf("AI Proxy Lock rejected target %q: %s", settings.AILockTarget, outcome.RejectionReason)
		}
		return nil
	}
}

func isInternalCoreDirectRule(rule string) bool {
	switch strings.TrimSpace(rule) {
	case "RULE-SET," + InternalDirectRuleProviderName + ",DIRECT",
		"PROCESS-NAME,mihomo_core.exe,DIRECT",
		"PROCESS-NAME,clashfx-daemon.exe,DIRECT",
		"PROCESS-NAME,clashfx-service.exe,DIRECT":
		return true
	default:
		return false
	}
}

func rejected(reason string) ApplyOutcome {
	return ApplyOutcome{
		Applied:         false,
		RejectionReason: reason,
	}
}

type depNode struct {
	isGroup     bool
	dialerProxy string
	candidates  []string
	unresolved  bool
}

type providerProxyRecord struct {
	name        string
	proxy       map[string]any
	dialerProxy string
	unresolved  bool
}

type providerResolution struct {
	proxies []providerProxyRecord
	known   bool
}

type locatedProxy struct {
	server       string
	providerName string
	proxy        map[string]any
}

type dependencyGraph struct {
	nodes           map[string]depNode
	ambiguousNames  map[string]bool
	referencedNames map[string]bool
}

func (g dependencyGraph) knows(name string) bool {
	_, ok := g.nodes[name]
	return ok || g.ambiguousNames[name] || g.referencedNames[name]
}

func (g dependencyGraph) isSafe(start, forbidden string) bool {
	active := map[string]bool{}
	completed := map[string]bool{}

	var visit func(name string) bool
	visit = func(name string) bool {
		if name == "DIRECT" || name == "REJECT" {
			return true
		}
		if name == "" || (forbidden != "" && name == forbidden) || g.ambiguousNames[name] {
			return false
		}
		if active[name] {
			return false
		}
		if completed[name] {
			return true
		}
		node, ok := g.nodes[name]
		if !ok {
			return false
		}
		active[name] = true
		var safe bool
		if !node.isGroup {
			if node.unresolved {
				safe = false
			} else if node.dialerProxy != "" {
				safe = visit(node.dialerProxy)
			} else {
				safe = true
			}
		} else {
			if node.unresolved || len(node.candidates) == 0 {
				safe = false
			} else {
				safe = true
				for _, c := range node.candidates {
					if !visit(c) {
						safe = false
						break
					}
				}
			}
		}
		delete(active, name)
		if safe {
			completed[name] = true
		}
		return safe
	}

	return visit(start)
}

func buildDependencyGraph(root map[string]any, providers []ProviderSnapshot) dependencyGraph {
	nodes := map[string]depNode{}
	ambiguous := map[string]bool{}
	referenced := map[string]bool{}
	providerMembers := map[string][]providerProxyRecord{}
	providersKnown := map[string]bool{}
	nodeOwners := map[string]string{}
	proxyRecordsByName := map[string]providerProxyRecord{}

	insert := func(name string, node depNode, owner string) {
		if name == "" {
			return
		}
		if existingOwner, exists := nodeOwners[name]; exists {
			if existingOwner != owner {
				ambiguous[name] = true
			}
			return
		}
		nodeOwners[name] = owner
		nodes[name] = node
	}

	rawProviderDefs := root["proxy-providers"]
	providerDefs, providerDefsValid := toStringMap(rawProviderDefs)
	if rawProviderDefs == nil {
		providerDefsValid = true
	}

	snapshotsByName := map[string][]ProviderSnapshot{}
	for _, p := range providers {
		snapshotsByName[p.Name] = append(snapshotsByName[p.Name], p)
	}

	for providerName, snaps := range snapshotsByName {
		if len(snaps) != 1 {
			providersKnown[providerName] = false
			for _, snap := range snaps {
				def, _ := toStringMap(providerDefs[providerName])
				res := resolveProvider(snap, def)
				for _, px := range res.proxies {
					insert(px.name, depNode{dialerProxy: px.dialerProxy, unresolved: true}, "provider:"+providerName)
					proxyRecordsByName[px.name] = px
				}
			}
			continue
		}
		def, _ := toStringMap(providerDefs[providerName])
		res := resolveProvider(snaps[0], def)
		providersKnown[providerName] = res.known
		providerMembers[providerName] = res.proxies
		for _, px := range res.proxies {
			insert(px.name, depNode{
				dialerProxy: px.dialerProxy,
				unresolved:  px.unresolved || !res.known,
			}, "provider:"+providerName)
			proxyRecordsByName[px.name] = px
		}
	}

	inlineProxies, inlineProxiesValid := extractMapListStrict(root["proxies"])
	var inlineRecords []providerProxyRecord
	for idx, px := range inlineProxies {
		name, _ := px["name"].(string)
		if name == "" {
			continue
		}
		var dialer string
		unresolved := false
		if rawDialer, exists := px["dialer-proxy"]; exists && rawDialer != nil {
			if s, ok := rawDialer.(string); ok {
				dialer = strings.TrimSpace(s)
			} else {
				unresolved = true
			}
		}
		rec := providerProxyRecord{
			name:        name,
			proxy:       px,
			dialerProxy: dialer,
			unresolved:  unresolved,
		}
		inlineRecords = append(inlineRecords, rec)
		proxyRecordsByName[name] = rec
		insert(name, depNode{dialerProxy: dialer, unresolved: unresolved}, fmt.Sprintf("inline:%d", idx))
	}

	groups := extractMapList(root["proxy-groups"])
	groupDefsByName := map[string]map[string]any{}
	for _, g := range groups {
		if n, _ := g["name"].(string); n != "" && groupDefsByName[n] == nil {
			groupDefsByName[n] = g
		}
	}

	for idx, group := range groups {
		name, _ := group["name"].(string)
		if name == "" {
			continue
		}
		var candidates []string
		unresolved := false

		includeAll := groupFlag("include-all", group, &unresolved)
		includeAllProxies := includeAll || groupFlag("include-all-proxies", group, &unresolved)
		includeAllProviders := includeAll || groupFlag("include-all-providers", group, &unresolved)

		appendCandidate := func(rawName string, applyIncludeFilter bool, rec *providerProxyRecord) {
			cand := strings.TrimSpace(rawName)
			if cand == "" {
				unresolved = true
				return
			}
			var candType string
			if rec != nil {
				candType, _ = rec.proxy["type"].(string)
			}
			if candType == "" {
				if gd := groupDefsByName[cand]; gd != nil {
					candType, _ = gd["type"].(string)
				}
			}
			if candType == "" {
				candType = builtinProxyType(cand)
			}
			included, ok := groupCandidateIncluded(cand, candType, group, applyIncludeFilter)
			if !ok {
				unresolved = true
				candidates = append(candidates, cand)
				referenced[cand] = true
				return
			}
			if included {
				candidates = append(candidates, cand)
				referenced[cand] = true
			}
		}

		if rawCands, exists := group["proxies"]; exists && rawCands != nil {
			if vals, ok := extractStringListStrict(rawCands); ok {
				for _, v := range vals {
					var recPtr *providerProxyRecord
					if r, found := proxyRecordsByName[v]; found {
						rCopy := r
						recPtr = &rCopy
					}
					appendCandidate(v, false, recPtr)
				}
			} else {
				unresolved = true
			}
		}

		if rawNow, exists := group["now"]; exists && rawNow != nil {
			if v, ok := rawNow.(string); ok {
				var recPtr *providerProxyRecord
				if r, found := proxyRecordsByName[v]; found {
					rCopy := r
					recPtr = &rCopy
				}
				appendCandidate(v, false, recPtr)
			} else {
				unresolved = true
			}
		}

		if includeAllProxies {
			if !inlineProxiesValid {
				unresolved = true
			} else {
				for i := range inlineRecords {
					appendCandidate(inlineRecords[i].name, true, &inlineRecords[i])
				}
			}
		}

		var usedProviders []string
		if includeAllProviders {
			if !providerDefsValid {
				unresolved = true
			} else {
				for k := range providerDefs {
					usedProviders = append(usedProviders, k)
				}
				sort.Strings(usedProviders)
			}
		} else if rawUse, exists := group["use"]; exists && rawUse != nil {
			if vals, ok := extractStringListStrict(rawUse); ok {
				usedProviders = vals
			} else {
				unresolved = true
			}
		}

		for _, provName := range usedProviders {
			if !providersKnown[provName] {
				unresolved = true
				continue
			}
			members, ok := providerMembers[provName]
			if !ok {
				unresolved = true
				continue
			}
			for i := range members {
				appendCandidate(members[i].name, true, &members[i])
			}
		}

		if len(candidates) == 0 {
			unresolved = true
		}
		insert(name, depNode{
			isGroup:    true,
			candidates: uniqueStrings(candidates),
			unresolved: unresolved,
		}, fmt.Sprintf("group:%d", idx))
	}

	return dependencyGraph{
		nodes:           nodes,
		ambiguousNames:  ambiguous,
		referencedNames: referenced,
	}
}

func groupFlag(key string, group map[string]any, unresolved *bool) bool {
	raw, exists := group[key]
	if !exists || raw == nil {
		return false
	}
	b, ok := raw.(bool)
	if !ok {
		*unresolved = true
		return false
	}
	return b
}

func groupCandidateIncluded(name, proxyType string, group map[string]any, applyIncludeFilter bool) (bool, bool) {
	var include string
	if applyIncludeFilter {
		if raw, exists := group["filter"]; exists && raw != nil {
			s, ok := raw.(string)
			if !ok {
				return false, false
			}
			include = s
		}
	}
	var exclude string
	if raw, exists := group["exclude-filter"]; exists && raw != nil {
		s, ok := raw.(string)
		if !ok {
			return false, false
		}
		exclude = s
	}
	matched, ok := matchesProviderFilters(name, include, exclude)
	if !ok || !matched {
		return matched, ok
	}

	rawExType, exists := group["exclude-type"]
	if !exists || rawExType == nil {
		return true, true
	}
	exType, ok := rawExType.(string)
	if !ok {
		return false, false
	}
	if exType == "" {
		return true, true
	}
	if proxyType == "" {
		return false, false
	}
	for _, part := range strings.Split(exType, "|") {
		if strings.EqualFold(strings.TrimSpace(part), proxyType) {
			return false, true
		}
	}
	return true, true
}

func builtinProxyType(name string) string {
	switch name {
	case "DIRECT":
		return "Direct"
	case "REJECT", "REJECT-DROP":
		return "Reject"
	case "PASS":
		return "Pass"
	case "COMPATIBLE":
		return "Compatible"
	default:
		return ""
	}
}

func resolveProvider(snap ProviderSnapshot, def map[string]any) providerResolution {
	known := def != nil && len(snap.Proxies) > 0
	var override map[string]any
	if def != nil && def["override"] != nil {
		var ok bool
		override, ok = toStringMap(def["override"])
		if !ok {
			known = false
		}
	}

	providerDialer := snap.DialerProxy
	if providerDialer == "" && def != nil && def["dialer-proxy"] != nil {
		if s, ok := def["dialer-proxy"].(string); ok {
			providerDialer = s
		} else {
			known = false
		}
	}

	overrideDialer := snap.OverrideDialerProxy
	hasOverrideDialer := snap.OverrideDialerProxy != ""
	if !hasOverrideDialer && override != nil && override["dialer-proxy"] != nil {
		if s, ok := override["dialer-proxy"].(string); ok {
			overrideDialer = s
			hasOverrideDialer = true
		} else {
			known = false
		}
	}

	var rawFilter string
	if def != nil && def["filter"] != nil {
		s, ok := def["filter"].(string)
		if !ok {
			known = false
		} else {
			rawFilter = s
		}
	}
	var rawExcludeFilter string
	if def != nil && def["exclude-filter"] != nil {
		s, ok := def["exclude-filter"].(string)
		if !ok {
			known = false
		} else {
			rawExcludeFilter = s
		}
	}
	var rawExcludeType string
	if def != nil && def["exclude-type"] != nil {
		s, ok := def["exclude-type"].(string)
		if !ok {
			known = false
		} else {
			rawExcludeType = s
		}
	}

	type seenEntry struct {
		rawName     string
		fingerprint string
	}
	seenNames := map[string]seenEntry{}
	var records []providerProxyRecord

	for _, rawProxy := range snap.Proxies {
		rawName, _ := rawProxy["name"].(string)
		if rawName == "" {
			known = false
			continue
		}
		if rawExcludeType != "" {
			pType, _ := rawProxy["type"].(string)
			if pType == "" {
				continue
			}
			excluded := false
			for _, part := range strings.Split(rawExcludeType, "|") {
				if strings.EqualFold(strings.TrimSpace(part), pType) {
					excluded = true
					break
				}
			}
			if excluded {
				continue
			}
		}
		included, ok := matchesProviderFilters(rawName, rawFilter, rawExcludeFilter)
		if !ok {
			known = false
			continue
		}
		if !included {
			continue
		}
		effectiveName, ok := providerProxyName(rawName, override)
		if !ok {
			known = false
			continue
		}
		fp := proxyFingerprint(rawProxy)
		if prev, exists := seenNames[effectiveName]; exists {
			if prev.rawName != rawName || prev.fingerprint == "" || fp == "" || prev.fingerprint != fp {
				known = false
			}
			continue
		}
		seenNames[effectiveName] = seenEntry{rawName: rawName, fingerprint: fp}
		if fp == "" {
			known = false
		}

		var rawProxyDialer string
		proxyHasInvalidDialer := false
		if rv, exists := rawProxy["dialer-proxy"]; exists && rv != nil {
			if s, ok := rv.(string); ok {
				rawProxyDialer = s
			} else {
				proxyHasInvalidDialer = true
			}
		}

		var effectiveDialer string
		switch {
		case hasOverrideDialer:
			effectiveDialer = strings.TrimSpace(overrideDialer)
		case strings.TrimSpace(providerDialer) != "":
			effectiveDialer = strings.TrimSpace(providerDialer)
		case proxyHasInvalidDialer:
			known = false
		default:
			effectiveDialer = strings.TrimSpace(rawProxyDialer)
		}

		mat := deepCopyMap(rawProxy)
		mat["name"] = effectiveName
		records = append(records, providerProxyRecord{
			name:        effectiveName,
			proxy:       mat,
			dialerProxy: effectiveDialer,
			unresolved:  !known || proxyHasInvalidDialer,
		})
	}

	if len(records) == 0 {
		known = false
	}
	if !known {
		for i := range records {
			records[i].unresolved = true
		}
	}
	return providerResolution{proxies: records, known: known}
}

func providerProxyName(name string, override map[string]any) (string, bool) {
	result := name
	if override != nil && override["proxy-name"] != nil {
		rules, ok := toAnySlice(override["proxy-name"])
		if !ok {
			return "", false
		}
		for _, rawRule := range rules {
			rule, ok := toStringMap(rawRule)
			if !ok {
				return "", false
			}
			pat, ok1 := rule["pattern"].(string)
			tgt, ok2 := rule["target"].(string)
			if !ok1 || !ok2 {
				return "", false
			}
			re, err := regexp.Compile(pat)
			if err != nil {
				return "", false
			}
			result = re.ReplaceAllString(result, tgt)
		}
	}
	var prefix, suffix string
	if override != nil && override["additional-prefix"] != nil {
		s, ok := override["additional-prefix"].(string)
		if !ok {
			return "", false
		}
		prefix = s
	}
	if override != nil && override["additional-suffix"] != nil {
		s, ok := override["additional-suffix"].(string)
		if !ok {
			return "", false
		}
		suffix = s
	}
	return prefix + result + suffix, true
}

func matchesProviderFilters(name, include, exclude string) (bool, bool) {
	if exclude != "" {
		for _, pat := range strings.Split(exclude, "`") {
			re, err := regexp.Compile(pat)
			if err != nil {
				return false, false
			}
			if re.MatchString(name) {
				return false, true
			}
		}
	}
	if include == "" {
		return true, true
	}
	for _, pat := range strings.Split(include, "`") {
		re, err := regexp.Compile(pat)
		if err != nil {
			return false, false
		}
		if re.MatchString(name) {
			return true, true
		}
	}
	return false, true
}

func proxyFingerprint(proxy map[string]any) string {
	b, err := json.Marshal(proxy)
	if err != nil {
		return ""
	}
	return string(b)
}

func locateProxy(root map[string]any, target string, providers []ProviderSnapshot) *locatedProxy {
	for _, px := range extractMapList(root["proxies"]) {
		if n, _ := px["name"].(string); n == target {
			srv, _ := px["server"].(string)
			return &locatedProxy{
				server: normalizedServerHost(srv),
				proxy:  deepCopyMap(px),
			}
		}
	}
	defs, _ := toStringMap(root["proxy-providers"])
	for _, snap := range providers {
		var def map[string]any
		if defs != nil {
			def, _ = toStringMap(defs[snap.Name])
		}
		res := resolveProvider(snap, def)
		for _, px := range res.proxies {
			if px.name == target {
				srv, _ := px.proxy["server"].(string)
				return &locatedProxy{
					server:       normalizedServerHost(srv),
					providerName: snap.Name,
					proxy:        deepCopyMap(px.proxy),
				}
			}
		}
	}
	return nil
}

func assignRelay(root map[string]any, target string, loc *locatedProxy, relay string) bool {
	if setInlineDialer(root, target, relay) {
		return true
	}
	if loc == nil || loc.providerName == "" || strings.Contains(target, "`") {
		return false
	}
	proxies := extractMapList(root["proxies"])
	for _, px := range proxies {
		if n, _ := px["name"].(string); n == target {
			return false
		}
	}
	copyPx := deepCopyMap(loc.proxy)
	copyPx["dialer-proxy"] = relay
	out := make([]any, 0, len(proxies)+1)
	for _, px := range proxies {
		out = append(out, px)
	}
	out = append(out, copyPx)
	root["proxies"] = out
	return appendExactNameExclusion(target, loc.providerName, root)
}

func setInlineDialer(root map[string]any, target, relay string) bool {
	proxies, ok := extractMapListStrict(root["proxies"])
	if !ok || len(proxies) == 0 {
		return false
	}
	updated := false
	out := make([]any, len(proxies))
	for i, px := range proxies {
		cp := deepCopyMap(px)
		if n, _ := cp["name"].(string); n == target {
			cp["dialer-proxy"] = relay
			updated = true
		}
		out[i] = cp
	}
	if updated {
		root["proxies"] = out
	}
	return updated
}

func appendExactNameExclusion(name, providerName string, root map[string]any) bool {
	providers, ok := toStringMap(root["proxy-providers"])
	if !ok || providers == nil {
		return false
	}
	prov, ok := toStringMap(providers[providerName])
	if !ok || prov == nil {
		return false
	}
	var existing string
	if raw, exists := prov["exclude-filter"]; exists && raw != nil {
		s, ok := raw.(string)
		if !ok {
			return false
		}
		existing = s
	}
	prov["exclude-filter"] = buildExcludeFilter(name, existing)
	providers[providerName] = prov
	root["proxy-providers"] = providers
	return true
}

func buildExcludeFilter(name, existing string) string {
	if strings.Contains(name, "`") {
		return existing
	}
	clause := "^" + regexp.QuoteMeta(name) + "$"
	cur := strings.TrimSpace(existing)
	if cur == "" {
		return clause
	}
	for _, p := range strings.Split(cur, "`") {
		if p == clause {
			return cur
		}
	}
	return cur + "`" + clause
}

func matchTarget(rule string) (string, bool) {
	parts := strings.SplitN(rule, ",", 3)
	if len(parts) < 2 {
		return "", false
	}
	if strings.TrimSpace(parts[0]) != "MATCH" {
		return "", false
	}
	return strings.TrimSpace(parts[1]), true
}

func normalizedServerHost(server string) string {
	host := strings.TrimSpace(server)
	if host == "" {
		return ""
	}
	if strings.HasPrefix(host, "[") {
		if end := strings.IndexByte(host, ']'); end > 1 {
			host = host[1:end]
		}
	}
	if host == "" || strings.ContainsAny(host, ",/ \t\r\n") {
		return ""
	}
	return host
}

func uniqueStrings(items []string) []string {
	seen := make(map[string]bool, len(items))
	out := make([]string, 0, len(items))
	for _, s := range items {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func toStringMap(v any) (map[string]any, bool) {
	if m, ok := v.(map[string]any); ok {
		return m, true
	}
	return nil, false
}

func toAnySlice(v any) ([]any, bool) {
	if s, ok := v.([]any); ok {
		return s, true
	}
	return nil, false
}

func extractMapList(v any) []map[string]any {
	list, _ := extractMapListStrict(v)
	return list
}

func extractMapListStrict(v any) ([]map[string]any, bool) {
	if v == nil {
		return nil, true
	}
	switch typed := v.(type) {
	case []map[string]any:
		return typed, true
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			m, ok := item.(map[string]any)
			if !ok {
				return nil, false
			}
			out = append(out, m)
		}
		return out, true
	default:
		return nil, false
	}
}

func extractStringListStrict(v any) ([]string, bool) {
	if v == nil {
		return nil, true
	}
	switch typed := v.(type) {
	case []string:
		return append([]string(nil), typed...), true
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			s, ok := item.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	default:
		return nil, false
	}
}

func deepCopyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = deepCopyValue(v)
	}
	return out
}

func deepCopyValue(v any) any {
	switch typed := v.(type) {
	case map[string]any:
		return deepCopyMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i, elem := range typed {
			out[i] = deepCopyValue(elem)
		}
		return out
	case []string:
		return append([]string(nil), typed...)
	case []map[string]any:
		out := make([]any, len(typed))
		for i, elem := range typed {
			out[i] = deepCopyMap(elem)
		}
		return out
	default:
		return typed
	}
}
