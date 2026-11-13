package profile

import (
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
)

const DefaultProfileMixinTemplate = `# Profile Mixin is merged into the selected profile at runtime.
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

func UnmarshalYAMLMap(data []byte) (map[string]any, error) {
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
	if len(prepend) > 0 || len(appendRules) > 0 {
		existing := extractStringRules(base["rules"])
		combined := make([]string, 0, len(prepend)+len(existing)+len(appendRules))
		combined = append(combined, prepend...)
		combined = append(combined, existing...)
		combined = append(combined, appendRules...)
		base["rules"] = uniqueStringsToAny(combined)
	}
	return mergeMixinMap(mixin, base), nil
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
			trimmed == "PROCESS-NAME,clashfx-service.exe,DIRECT" {
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
