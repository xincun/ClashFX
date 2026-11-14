package profile

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	ProfileMixinDotFile            = ".profile_mixin.yaml"
	ProfileMixinNamedFile          = "Profile Mixin.yaml"
	RecoverySelectionsKey          = "x-clashfx-recovery-selections"
	InternalDirectRuleProviderName = "clashfx-internal-direct"
	AILockRuleProviderName         = "clashfx-ai-lock"
	CNAppsRuleProviderName         = "clashfx-cn-apps-direct"
	ProxyTargetPlaceholder         = "PROXY"
)

const LegacyEmptyProfileMixinTemplate = `# Profile Mixin is merged into the selected profile at runtime.
# Example:
# proxy-groups:
#   - name: Auto 1x
#     type: url-test
#     use:
#       - ProviderName
#
# Rules can be inserted before or after the selected profile's rules:
# profile:
#   prepend-rules:
#     - "DOMAIN-SUFFIX,example.com,DIRECT"
#   append-rules:
#     - "MATCH,Proxy"
#
# PROCESS-NAME and PROCESS-PATH rules require Enhanced Mode (TUN).
`

const DefaultProfileMixinTemplate = `# ClashFX Profile Mixin — merged into every profile at runtime.
# Managed rules from the ClashFX UI live under profile.prepend-rules;
# any extra top-level keys (dns, sniffer, tun, hosts, rules, etc.) are preserved intact.
profile:
  prepend-rules:
    - GEOSITE,private,DIRECT
    - GEOIP,LAN,DIRECT,no-resolve
    - GEOSITE,cn,DIRECT
    - GEOIP,CN,DIRECT,no-resolve
`

const DisabledProfileMixinTemplate = `# ClashFX Profile Mixin — merged into every profile at runtime.
profile: {}
`

// IsLegacyUntouchedProfileMixin reports whether raw still matches the legacy
// comment-only starter mixin template before default CN/LAN direct rules existed.
func IsLegacyUntouchedProfileMixin(raw []byte) bool {
	normalize := func(s string) string {
		return strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
	}
	return normalize(string(raw)) == normalize(LegacyEmptyProfileMixinTemplate)
}

func IsProfileMixinFileName(name string) bool {
	return name == ProfileMixinDotFile || name == ProfileMixinNamedFile
}

// NormalizeYAMLValue converts nested maps from yaml.v3 into map[string]any recursively.
func NormalizeYAMLValue(v any) any {
	switch typed := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for k, val := range typed {
			out[k] = NormalizeYAMLValue(val)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(typed))
		for k, val := range typed {
			out[fmt.Sprint(k)] = NormalizeYAMLValue(val)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, val := range typed {
			out[i] = NormalizeYAMLValue(val)
		}
		return out
	default:
		return v
	}
}

func expandLeadingTabs(data []byte, tabStop int) []byte {
	lines := bytes.Split(data, []byte{'\n'})
	for idx, line := range lines {
		i := 0
		col := 0
		hasTab := false
		for i < len(line) {
			switch line[i] {
			case ' ':
				col++
				i++
			case '\t':
				col += tabStop - (col % tabStop)
				hasTab = true
				i++
			default:
				goto done
			}
		}
	done:
		if !hasTab {
			continue
		}
		expanded := make([]byte, col+len(line)-i)
		for j := 0; j < col; j++ {
			expanded[j] = ' '
		}
		copy(expanded[col:], line[i:])
		lines[idx] = expanded
	}
	return bytes.Join(lines, []byte{'\n'})
}

// SanitizeYAMLIndentation expands tab indentation to spaces when raw YAML fails
// initial parsing due to leading tabs (preserving tabs inside values/comments).
func SanitizeYAMLIndentation(data []byte) []byte {
	if !bytes.Contains(data, []byte{'\t'}) {
		return data
	}
	var probe any
	if yaml.Unmarshal(data, &probe) == nil {
		return data
	}
	for _, stop := range []int{2, 4} {
		cand := expandLeadingTabs(data, stop)
		if yaml.Unmarshal(cand, &probe) == nil {
			return cand
		}
	}
	return data
}

func UnmarshalYAMLMap(data []byte) (map[string]any, error) {
	data = SanitizeYAMLIndentation(data)
	var raw any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	if raw == nil {
		return map[string]any{}, nil
	}
	norm, ok := NormalizeYAMLValue(raw).(map[string]any)
	if !ok {
		return nil, fmt.Errorf("YAML root must be a mapping")
	}
	return norm, nil
}

// ApplyProfileMixin merges mixin YAML into base map following ClashFX's exact rules:
// 1. Extract profile.prepend-rules / profile.append-rules from base and mixin.
// 2. Recursively merge maps, merge named object slices by "name", and deduplicate string slices.
// 3. Resolve placeholder target "PROXY" in mixin rule directives to the merged profile's primary group.
func ApplyProfileMixin(base map[string]any, mixinYAML []byte) (map[string]any, error) {
	ApplyRuleDirectivesInPlace(base)
	if len(strings.TrimSpace(string(mixinYAML))) == 0 {
		return base, nil
	}
	mixin, err := UnmarshalYAMLMap(mixinYAML)
	if err != nil {
		return nil, fmt.Errorf("parse Profile Mixin: %w", err)
	}
	if len(mixin) == 0 {
		return base, nil
	}
	prepend, appendRules := takeProfileRuleDirectives(mixin)
	merged := mergeMixinMap(mixin, base)
	if len(prepend) > 0 || len(appendRules) > 0 {
		defTarget := ResolveDefaultRuleTarget(merged)
		literalPROXY := hasNamedOutbound(merged, ProxyTargetPlaceholder)
		existing := extractStringRules(merged["rules"])
		combined := make([]string, 0, len(prepend)+len(existing)+len(appendRules))
		for _, r := range prepend {
			combined = append(combined, ResolveMixinRuleLine(r, defTarget, literalPROXY))
		}
		combined = append(combined, existing...)
		for _, r := range appendRules {
			combined = append(combined, ResolveMixinRuleLine(r, defTarget, literalPROXY))
		}
		merged["rules"] = uniqueStringsToAny(combined)
	}
	return merged, nil
}

// ResolveDefaultRuleTarget finds the active profile's primary proxy group:
// 1. Final MATCH,<target> if <target> is a proxy/group (not DIRECT/REJECT)
// 2. First non-GLOBAL proxy-group name
// 3. Fallback "DIRECT"
func ResolveDefaultRuleTarget(root map[string]any) string {
	rules := extractStringRules(root["rules"])
	for i := len(rules) - 1; i >= 0; i-- {
		line := strings.TrimSpace(rules[i])
		parts := strings.SplitN(line, ",", 3)
		if len(parts) >= 2 && strings.EqualFold(strings.TrimSpace(parts[0]), "MATCH") {
			cand := strings.TrimSpace(parts[1])
			upper := strings.ToUpper(cand)
			if cand != "" && upper != "DIRECT" && upper != "REJECT" && upper != "REJECT-DROP" {
				return cand
			}
			break
		}
	}
	for _, m := range extractNamedMaps(root["proxy-groups"]) {
		name, _ := m["name"].(string)
		name = strings.TrimSpace(name)
		if name != "" && !strings.EqualFold(name, "GLOBAL") {
			return name
		}
	}
	return "DIRECT"
}

// ResolveMixinRuleLine rewrites placeholder target "PROXY" (when no literal "PROXY"
// group/node exists in the profile) to defTarget so Profile Mixin rules work across
// arbitrary subscriptions without manual target renaming.
func ResolveMixinRuleLine(rawLine, defTarget string, literalPROXYExists bool) string {
	trimmed := strings.TrimSpace(rawLine)
	if trimmed == "" || defTarget == "" {
		return trimmed
	}
	parts := strings.Split(trimmed, ",")
	if len(parts) < 2 {
		return trimmed
	}
	tp := strings.ToUpper(strings.TrimSpace(parts[0]))
	if tp == "MATCH" {
		tgt := strings.TrimSpace(parts[1])
		if (tgt == ProxyTargetPlaceholder && !literalPROXYExists) || strings.EqualFold(tgt, "@DEFAULT") {
			parts[0] = "MATCH"
			parts[1] = defTarget
			return strings.Join(parts[:2], ",")
		}
		return trimmed
	}
	if len(parts) < 3 {
		return trimmed
	}
	targetIdx := len(parts) - 1
	for targetIdx >= 3 {
		opt := strings.ToLower(strings.TrimSpace(parts[targetIdx]))
		if opt == "no-resolve" || opt == "src" {
			targetIdx--
			continue
		}
		break
	}
	tgt := strings.TrimSpace(parts[targetIdx])
	if (tgt == ProxyTargetPlaceholder && !literalPROXYExists) || strings.EqualFold(tgt, "@DEFAULT") {
		payload := strings.TrimSpace(strings.Join(parts[1:targetIdx], ","))
		tail := []string{tp, payload, defTarget}
		for _, extra := range parts[targetIdx+1:] {
			tail = append(tail, strings.TrimSpace(extra))
		}
		return strings.Join(tail, ",")
	}
	return trimmed
}

func hasNamedOutbound(root map[string]any, exactName string) bool {
	for _, key := range []string{"proxy-groups", "proxies"} {
		for _, m := range extractNamedMaps(root[key]) {
			if n, _ := m["name"].(string); strings.TrimSpace(n) == exactName {
				return true
			}
		}
	}
	return false
}

func extractNamedMaps(v any) []map[string]any {
	switch list := v.(type) {
	case []map[string]any:
		return list
	case []any:
		out := make([]map[string]any, 0, len(list))
		for _, item := range list {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	default:
		return nil
	}
}

func ApplyRuleDirectivesInPlace(root map[string]any) bool {
	prepend, appendRules := takeProfileRuleDirectives(root)
	if len(prepend) == 0 && len(appendRules) == 0 {
		return false
	}
	existing := extractStringRules(root["rules"])
	combined := make([]string, 0, len(prepend)+len(existing)+len(appendRules))
	combined = append(combined, prepend...)
	combined = append(combined, existing...)
	combined = append(combined, appendRules...)
	root["rules"] = uniqueStringsToAny(combined)
	return true
}

func takeProfileRuleDirectives(root map[string]any) (prepend []string, appendRules []string) {
	prof, ok := root["profile"].(map[string]any)
	if !ok {
		return nil, nil
	}
	prepend = extractStringRules(prof["prepend-rules"])
	appendRules = extractStringRules(prof["append-rules"])
	delete(prof, "prepend-rules")
	delete(prof, "append-rules")
	if len(prepend) == 0 && len(appendRules) == 0 {
		return nil, nil
	}
	if len(prof) == 0 {
		delete(root, "profile")
	} else {
		root["profile"] = prof
	}
	return prepend, appendRules
}

func ExtractStringRules(v any) []string {
	return extractStringRules(v)
}

func SetInlineClassicalRuleProvider(root map[string]any, name string, payload []string) {
	rpMap, ok := root["rule-providers"].(map[string]any)
	if !ok || rpMap == nil {
		if norm, normOK := NormalizeYAMLValue(root["rule-providers"]).(map[string]any); normOK && norm != nil {
			rpMap = norm
		} else {
			rpMap = map[string]any{}
		}
	}
	rpMap[name] = map[string]any{
		"type":     "inline",
		"behavior": "classical",
		"payload":  append([]string(nil), payload...),
	}
	root["rule-providers"] = rpMap
}

// SystemGuardPrefixCount returns how many leading top-level rules belong to
// ClashFX's system guards (internal core loop bypass + AI Proxy Lock).
func SystemGuardPrefixCount(rules []string) int {
	idx := 0
	for idx < len(rules) {
		trimmed := strings.TrimSpace(rules[idx])
		if trimmed == "RULE-SET,"+InternalDirectRuleProviderName+",DIRECT" ||
			strings.HasPrefix(trimmed, "RULE-SET,"+AILockRuleProviderName+",") ||
			trimmed == "PROCESS-NAME,mihomo_core.exe,DIRECT" ||
			trimmed == "PROCESS-NAME,clashfx-daemon.exe,DIRECT" ||
			trimmed == "PROCESS-NAME,clashfx-service.exe,DIRECT" ||
			trimmed == "PROCESS-NAME,mihomo_core,DIRECT" ||
			trimmed == "PROCESS-NAME,clashfx-daemon,DIRECT" ||
			trimmed == "PROCESS-NAME,clashfx-service,DIRECT" ||
			trimmed == "PROCESS-NAME,ClashFX,DIRECT" {
			idx++
			continue
		}
		if idx+1 < len(rules) {
			comma := strings.LastIndex(trimmed, ",")
			if comma > 0 && strings.TrimSpace(rules[idx+1]) == trimmed[:comma]+",REJECT" {
				idx += 2
				continue
			}
		}
		break
	}
	return idx
}

func extractStringRules(v any) []string {
	switch list := v.(type) {
	case []string:
		return append([]string(nil), list...)
	case []any:
		out := make([]string, 0, len(list))
		for _, item := range list {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func uniqueStringsToAny(items []string) []any {
	seen := make(map[string]struct{}, len(items))
	out := make([]any, 0, len(items))
	for _, s := range items {
		if _, exists := seen[s]; exists {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func mergeMixinMap(mixin, base map[string]any) map[string]any {
	merged := make(map[string]any, len(base)+len(mixin))
	for k, v := range base {
		merged[k] = v
	}
	for k, mVal := range mixin {
		bVal, exists := merged[k]
		if !exists {
			merged[k] = mVal
			continue
		}
		bMap, bIsMap := bVal.(map[string]any)
		mMap, mIsMap := mVal.(map[string]any)
		if bIsMap && mIsMap {
			merged[k] = mergeMixinMap(mMap, bMap)
			continue
		}
		bNamed, bIsNamed := toMapSlice(bVal)
		mNamed, mIsNamed := toMapSlice(mVal)
		if bIsNamed && mIsNamed {
			merged[k] = mergeNamedSlice(bNamed, mNamed)
			continue
		}
		bStrs, bIsStrs := toStringSliceStrict(bVal)
		mStrs, mIsStrs := toStringSliceStrict(mVal)
		if bIsStrs && mIsStrs {
			merged[k] = uniqueStringsToAny(append(bStrs, mStrs...))
			continue
		}
		merged[k] = mVal
	}
	return merged
}

func toMapSlice(v any) ([]map[string]any, bool) {
	switch list := v.(type) {
	case []map[string]any:
		return list, true
	case []any:
		if len(list) == 0 {
			return nil, false
		}
		out := make([]map[string]any, 0, len(list))
		for _, item := range list {
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

func toStringSliceStrict(v any) ([]string, bool) {
	switch list := v.(type) {
	case []string:
		return list, true
	case []any:
		out := make([]string, 0, len(list))
		for _, item := range list {
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

func mergeNamedSlice(base, mixin []map[string]any) []any {
	merged := make([]map[string]any, len(base))
	copy(merged, base)
	for _, item := range mixin {
		name, _ := item["name"].(string)
		idx := -1
		if name != "" {
			for i, existing := range merged {
				if existingName, _ := existing["name"].(string); existingName == name {
					idx = i
					break
				}
			}
		}
		if idx >= 0 {
			merged[idx] = mergeMixinMap(item, merged[idx])
		} else {
			merged = append(merged, item)
		}
	}
	out := make([]any, len(merged))
	for i, m := range merged {
		out[i] = m
	}
	return out
}
