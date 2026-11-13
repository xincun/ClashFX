package features

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/clashfx/core-engine/internal/profile"
)

var SupportedCustomRuleTypes = []string{
	"DOMAIN-SUFFIX",
	"DOMAIN",
	"DOMAIN-KEYWORD",
	"DOMAIN-REGEX",
	"DOMAIN-WILDCARD",
	"GEOSITE",
	"IP-CIDR",
	"IP-CIDR6",
	"SRC-IP-CIDR",
	"GEOIP",
	"IP-ASN",
	"DST-PORT",
	"SRC-PORT",
	"PROCESS-NAME",
	"PROCESS-PATH",
	"PROCESS-NAME-REGEX",
	"PROCESS-PATH-REGEX",
	"NETWORK",
}

func SupportsNoResolveParam(ruleType string) bool {
	switch strings.ToUpper(strings.TrimSpace(ruleType)) {
	case "IP-CIDR", "IP-CIDR6", "GEOIP", "IP-ASN":
		return true
	default:
		return false
	}
}

func allowsCommaInPayload(ruleType string) bool {
	switch strings.ToUpper(strings.TrimSpace(ruleType)) {
	case "DOMAIN-REGEX", "PROCESS-NAME-REGEX", "PROCESS-PATH-REGEX":
		return true
	default:
		return false
	}
}

// NormalizeAndValidateCustomRules validates and normalizes user-defined routing rules.
func NormalizeAndValidateCustomRules(rules []profile.CustomRule) ([]profile.CustomRule, error) {
	if len(rules) == 0 {
		return nil, nil
	}
	validTypes := make(map[string]bool, len(SupportedCustomRuleTypes))
	for _, t := range SupportedCustomRuleTypes {
		validTypes[t] = true
	}

	out := make([]profile.CustomRule, 0, len(rules))
	for i, r := range rules {
		tp := strings.ToUpper(strings.TrimSpace(r.Type))
		if !validTypes[tp] {
			return nil, fmt.Errorf("rule #%d: unsupported rule type %q", i+1, r.Type)
		}
		payload := strings.TrimSpace(r.Payload)
		if payload == "" {
			return nil, fmt.Errorf("rule #%d (%s): payload is required", i+1, tp)
		}
		if strings.ContainsAny(payload, "\r\n") {
			return nil, fmt.Errorf("rule #%d (%s): payload must not contain line breaks", i+1, tp)
		}
		if !allowsCommaInPayload(tp) && strings.Contains(payload, ",") {
			return nil, fmt.Errorf("rule #%d (%s): payload must not contain commas", i+1, tp)
		}

		target := strings.TrimSpace(r.Target)
		if target == "" {
			return nil, fmt.Errorf("rule #%d (%s): target outbound is required", i+1, tp)
		}
		if strings.ContainsAny(target, ",\r\n") {
			return nil, fmt.Errorf("rule #%d (%s): target must not contain commas or line breaks", i+1, tp)
		}
		if strings.EqualFold(target, profile.DefaultProxyTargetToken) {
			target = profile.DefaultProxyTargetToken
		}

		pos := strings.ToLower(strings.TrimSpace(r.Position))
		if pos == "" || pos == "prepend" {
			pos = "prepend"
		} else if pos != "before_match" {
			return nil, fmt.Errorf("rule #%d (%s): invalid position %q", i+1, tp, r.Position)
		}

		noResolve := r.NoResolve && SupportsNoResolveParam(tp)
		note := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(r.Note, "\r", " "), "\n", " "))

		out = append(out, profile.CustomRule{
			Enabled:   r.Enabled,
			Type:      tp,
			Payload:   payload,
			Target:    target,
			Position:  pos,
			NoResolve: noResolve,
			Note:      note,
		})
	}
	return out, nil
}

// ResolveDefaultRuleTarget finds the active profile's primary proxy group:
// 1. Final MATCH,<target> if <target> is a proxy/group (not DIRECT/REJECT)
// 2. First non-GLOBAL proxy-group name
// 3. Fallback "DIRECT"
func ResolveDefaultRuleTarget(root map[string]any) string {
	rules := profile.ExtractStringRules(root["rules"])
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
	if groups, ok := root["proxy-groups"].([]any); ok {
		for _, gRaw := range groups {
			gm, ok := gRaw.(map[string]any)
			if !ok {
				continue
			}
			name, _ := gm["name"].(string)
			name = strings.TrimSpace(name)
			if name != "" && !strings.EqualFold(name, "GLOBAL") {
				return name
			}
		}
	}
	return "DIRECT"
}

// CollectValidRuleTargets returns all outbound/group/proxy names valid in root.
func CollectValidRuleTargets(root map[string]any, cfgDir string) map[string]bool {
	valid := map[string]bool{
		"DIRECT":      true,
		"REJECT":      true,
		"REJECT-DROP": true,
		"PASS":        true,
		"COMPATIBLE":  true,
		"GLOBAL":      true,
	}
	addNamedSlice := func(v any) {
		items, ok := v.([]any)
		if !ok {
			return
		}
		for _, raw := range items {
			if m, ok := raw.(map[string]any); ok {
				if n, _ := m["name"].(string); strings.TrimSpace(n) != "" {
					valid[strings.TrimSpace(n)] = true
				}
			}
		}
	}
	addNamedSlice(root["proxies"])
	addNamedSlice(root["proxy-groups"])

	if cfgDir != "" {
		if provs, ok := root["proxy-providers"].(map[string]any); ok {
			for _, pRaw := range provs {
				pm, ok := pRaw.(map[string]any)
				if !ok {
					continue
				}
				relPath, _ := pm["path"].(string)
				relPath = strings.TrimSpace(relPath)
				if relPath == "" {
					continue
				}
				fullPath := relPath
				if !filepath.IsAbs(fullPath) {
					fullPath = filepath.Join(cfgDir, relPath)
				}
				if data, err := os.ReadFile(fullPath); err == nil {
					if provDoc, pErr := profile.UnmarshalYAMLMap(data); pErr == nil {
						addNamedSlice(provDoc["proxies"])
					}
				}
			}
		}
	}
	return valid
}

// FormatCustomRuleLine renders an enabled CustomRule into a Mihomo top-level rule line.
func FormatCustomRuleLine(r profile.CustomRule, resolvedTarget string) string {
	tp := strings.ToUpper(strings.TrimSpace(r.Type))
	payload := strings.TrimSpace(r.Payload)
	line := fmt.Sprintf("%s,%s,%s", tp, payload, resolvedTarget)
	if r.NoResolve && SupportsNoResolveParam(tp) {
		line += ",no-resolve"
	}
	return line
}

// TransformHookForCustomRules compiles enabled user CustomRules into root["rules"]:
// - "prepend" rules are placed immediately after system guards (ahead of CN apps & profile rules).
// - "before_match" rules are placed immediately before the trailing MATCH rule.
// Rules targeting a concrete group/node absent from the selected profile are safely skipped
// so switching profiles never fails startup; @DEFAULT adapts automatically to every profile.
func TransformHookForCustomRules(cfgDirLoader func() string) func(root map[string]any, settings profile.AppSettings) error {
	return func(root map[string]any, settings profile.AppSettings) error {
		if (settings.EnhancedMode && settings.CustomConfigAsIs) || len(settings.CustomRules) == 0 {
			return nil
		}
		var cfgDir string
		if cfgDirLoader != nil {
			cfgDir = cfgDirLoader()
		}
		validTargets := CollectValidRuleTargets(root, cfgDir)
		defaultTarget := ResolveDefaultRuleTarget(root)

		var prependLines []string
		var beforeMatchLines []string
		for _, r := range settings.CustomRules {
			if !r.Enabled {
				continue
			}
			target := strings.TrimSpace(r.Target)
			if strings.EqualFold(target, profile.DefaultProxyTargetToken) {
				target = defaultTarget
			}
			if !validTargets[target] {
				continue
			}
			line := FormatCustomRuleLine(r, target)
			if strings.EqualFold(strings.TrimSpace(r.Position), "before_match") {
				beforeMatchLines = append(beforeMatchLines, line)
			} else {
				prependLines = append(prependLines, line)
			}
		}
		if len(prependLines) == 0 && len(beforeMatchLines) == 0 {
			return nil
		}

		existing := profile.ExtractStringRules(root["rules"])
		guardCount := profile.SystemGuardPrefixCount(existing)

		// Find final MATCH rule index (>= guardCount) for before_match insertion
		matchIdx := -1
		for i := len(existing) - 1; i >= guardCount; i-- {
			trimmed := strings.TrimSpace(existing[i])
			if strings.HasPrefix(strings.ToUpper(trimmed), "MATCH,") {
				matchIdx = i
				break
			}
		}

		seen := make(map[string]bool, len(existing)+len(prependLines)+len(beforeMatchLines))
		out := make([]any, 0, len(existing)+len(prependLines)+len(beforeMatchLines))
		appendUnique := func(list []string) {
			for _, s := range list {
				if !seen[s] {
					seen[s] = true
					out = append(out, s)
				}
			}
		}

		appendUnique(existing[:guardCount])
		appendUnique(prependLines)
		if matchIdx >= 0 {
			appendUnique(existing[guardCount:matchIdx])
			appendUnique(beforeMatchLines)
			appendUnique(existing[matchIdx:])
		} else {
			appendUnique(existing[guardCount:])
			appendUnique(beforeMatchLines)
		}

		root["rules"] = out
		return nil
	}
}
