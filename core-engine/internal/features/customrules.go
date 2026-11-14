package features

import (
	"fmt"
	"net"
	"strings"

	"github.com/clashfx/core-engine/internal/profile"
	"gopkg.in/yaml.v3"
)

const ProfileMixinHeaderComment = "# ClashFX Profile Mixin — merged into every profile at runtime.\n"

var SupportedMixinRuleTypes = []string{
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
	"RULE-SET",
	"NETWORK",
	"MATCH",
}

type MixinRuleItem struct {
	Type    string `json:"type"`
	Payload string `json:"payload"`
	Target  string `json:"target"`
}

type ProfileMixinDocument struct {
	Path          string          `json:"path,omitempty"`
	YAML          string          `json:"yaml"`
	BypassCNWeb   bool            `json:"bypass_cn_web"`
	BypassLAN     bool            `json:"bypass_lan"`
	Rules         []MixinRuleItem `json:"rules"`
	HasExtraKeys  bool            `json:"has_extra_keys"`
	DefaultTarget string          `json:"default_target,omitempty"`
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

func cleanDomainInput(raw string, stripLeadingDot bool) string {
	s := strings.TrimSpace(raw)
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "https://") {
		s = s[len("https://"):]
	} else if strings.HasPrefix(lower, "http://") {
		s = s[len("http://"):]
	}
	if slash := strings.IndexAny(s, "/?#"); slash >= 0 {
		s = s[:slash]
	}
	if host, _, err := net.SplitHostPort(s); err == nil && host != "" {
		s = host
	}
	if stripLeadingDot {
		s = strings.TrimLeft(s, ".")
	}
	return strings.TrimSpace(s)
}

// NormalizeAndValidateMixinRules validates and cleans visual rule items before writing YAML.
func NormalizeAndValidateMixinRules(items []MixinRuleItem) ([]MixinRuleItem, error) {
	if len(items) == 0 {
		return []MixinRuleItem{}, nil
	}
	validTypes := make(map[string]bool, len(SupportedMixinRuleTypes))
	for _, t := range SupportedMixinRuleTypes {
		validTypes[t] = true
	}

	out := make([]MixinRuleItem, 0, len(items))
	for i, item := range items {
		tp := strings.ToUpper(strings.TrimSpace(item.Type))
		if !validTypes[tp] {
			return nil, fmt.Errorf("第 %d 条规则类型不支持: %q", i+1, item.Type)
		}

		payload := strings.TrimSpace(item.Payload)
		target := strings.TrimSpace(item.Target)
		if strings.EqualFold(target, profile.ProxyTargetPlaceholder) || strings.EqualFold(target, "@DEFAULT") {
			target = profile.ProxyTargetPlaceholder
		}
		if target == "" {
			return nil, fmt.Errorf("第 %d 条规则未选择出口策略", i+1)
		}
		if strings.ContainsAny(target, ",\r\n") {
			return nil, fmt.Errorf("第 %d 条规则出口名称不合法", i+1)
		}

		if tp == "MATCH" {
			out = append(out, MixinRuleItem{Type: "MATCH", Payload: "", Target: target})
			continue
		}

		switch tp {
		case "DOMAIN-SUFFIX":
			payload = cleanDomainInput(payload, true)
		case "DOMAIN", "DOMAIN-KEYWORD":
			payload = cleanDomainInput(payload, false)
		case "PROCESS-NAME":
			if strings.ContainsAny(payload, `\/`) {
				tp = "PROCESS-PATH"
			}
		case "IP-CIDR", "IP-CIDR6":
			if !strings.Contains(payload, "/") {
				if ip := net.ParseIP(payload); ip != nil {
					if strings.Contains(payload, ":") {
						payload += "/128"
					} else {
						payload += "/32"
					}
				}
			}
			if strings.Contains(payload, ":") {
				tp = "IP-CIDR6"
			} else {
				tp = "IP-CIDR"
			}
		}

		if payload == "" {
			return nil, fmt.Errorf("第 %d 条规则（%s）内容不能为空", i+1, tp)
		}
		if strings.ContainsAny(payload, "\r\n") {
			return nil, fmt.Errorf("第 %d 条规则内容不能包含换行", i+1)
		}
		if !allowsCommaInPayload(tp) && strings.Contains(payload, ",") {
			return nil, fmt.Errorf("第 %d 条规则内容不能包含逗号", i+1)
		}

		out = append(out, MixinRuleItem{
			Type:    tp,
			Payload: payload,
			Target:  target,
		})
	}
	return out, nil
}

func FormatMixinRuleItem(item MixinRuleItem) string {
	tp := strings.ToUpper(strings.TrimSpace(item.Type))
	target := strings.TrimSpace(item.Target)
	if tp == "MATCH" {
		return "MATCH," + target
	}
	line := fmt.Sprintf("%s,%s,%s", tp, strings.TrimSpace(item.Payload), target)
	if SupportsNoResolveParam(tp) {
		line += ",no-resolve"
	}
	return line
}

func parseMixinRuleLine(rawLine string) (MixinRuleItem, bool) {
	trimmed := strings.TrimSpace(rawLine)
	if trimmed == "" {
		return MixinRuleItem{}, false
	}
	parts := strings.Split(trimmed, ",")
	if len(parts) < 2 {
		return MixinRuleItem{}, false
	}
	tp := strings.ToUpper(strings.TrimSpace(parts[0]))
	if tp == "MATCH" {
		tgt := strings.TrimSpace(parts[1])
		if strings.EqualFold(tgt, "@DEFAULT") {
			tgt = profile.ProxyTargetPlaceholder
		}
		return MixinRuleItem{
			Type:   "MATCH",
			Target: tgt,
		}, true
	}
	if len(parts) < 3 {
		return MixinRuleItem{}, false
	}
	// Parse optional trailing parameters (e.g. "no-resolve", "src") from the right
	// so regex rules containing commas like {1,3} preserve their full payload!
	endIdx := len(parts) - 1
	for endIdx >= 3 && isTrailingRuleOption(parts[endIdx]) {
		endIdx--
	}
	tgt := strings.TrimSpace(parts[endIdx])
	if strings.EqualFold(tgt, "@DEFAULT") {
		tgt = profile.ProxyTargetPlaceholder
	}
	payload := strings.TrimSpace(strings.Join(parts[1:endIdx], ","))
	return MixinRuleItem{
		Type:    tp,
		Payload: payload,
		Target:  tgt,
	}, true
}

func isTrailingRuleOption(tok string) bool {
	switch strings.ToLower(strings.TrimSpace(tok)) {
	case "no-resolve", "src":
		return true
	default:
		return false
	}
}

func isCNGeositeDirect(line string) bool {
	item, ok := parseMixinRuleLine(line)
	return ok && item.Type == "GEOSITE" && strings.EqualFold(item.Payload, "cn") && strings.EqualFold(item.Target, "DIRECT")
}

func isCNGeoipDirect(line string) bool {
	item, ok := parseMixinRuleLine(line)
	return ok && item.Type == "GEOIP" && strings.EqualFold(item.Payload, "cn") && strings.EqualFold(item.Target, "DIRECT")
}

func isLANGeositeDirect(line string) bool {
	item, ok := parseMixinRuleLine(line)
	return ok && item.Type == "GEOSITE" && strings.EqualFold(item.Payload, "private") && strings.EqualFold(item.Target, "DIRECT")
}

func isLANGeoipDirect(line string) bool {
	item, ok := parseMixinRuleLine(line)
	return ok && item.Type == "GEOIP" && strings.EqualFold(item.Payload, "lan") && strings.EqualFold(item.Target, "DIRECT")
}

// ParseProfileMixinDocument parses .profile_mixin.yaml into visual toggles + rule rows.
func ParseProfileMixinDocument(rawYAML []byte, defaultTarget string) (ProfileMixinDocument, error) {
	text := string(rawYAML)
	if strings.TrimSpace(text) == "" {
		text = profile.DefaultProfileMixinTemplate
	}
	text = string(profile.SanitizeYAMLIndentation([]byte(text)))
	doc, err := profile.UnmarshalYAMLMap([]byte(text))
	if err != nil {
		return ProfileMixinDocument{}, err
	}

	var prepend []string
	hasExtra := false
	for k, v := range doc {
		if k != "profile" {
			hasExtra = true
			continue
		}
		pm, ok := v.(map[string]any)
		if !ok {
			hasExtra = true
			continue
		}
		prepend = profile.ExtractStringRules(pm["prepend-rules"])
		for pk := range pm {
			if pk != "prepend-rules" {
				hasExtra = true
			}
		}
	}

	hasCNGeosite, hasCNGeoip := false, false
	hasLANGeosite, hasLANGeoip := false, false
	for _, r := range prepend {
		if isCNGeositeDirect(r) {
			hasCNGeosite = true
		}
		if isCNGeoipDirect(r) {
			hasCNGeoip = true
		}
		if isLANGeositeDirect(r) {
			hasLANGeosite = true
		}
		if isLANGeoipDirect(r) {
			hasLANGeoip = true
		}
	}
	bypassCNWeb := hasCNGeosite && hasCNGeoip
	bypassLAN := hasLANGeosite && hasLANGeoip

	rules := make([]MixinRuleItem, 0, len(prepend))
	for _, r := range prepend {
		if bypassCNWeb && (isCNGeositeDirect(r) || isCNGeoipDirect(r)) {
			continue
		}
		if bypassLAN && (isLANGeositeDirect(r) || isLANGeoipDirect(r)) {
			continue
		}
		if item, ok := parseMixinRuleLine(r); ok {
			rules = append(rules, item)
		}
	}

	return ProfileMixinDocument{
		YAML:          text,
		BypassCNWeb:   bypassCNWeb,
		BypassLAN:     bypassLAN,
		Rules:         rules,
		HasExtraKeys:  hasExtra,
		DefaultTarget: defaultTarget,
	}, nil
}

// BuildProfileMixinYAML updates profile.prepend-rules inside baseYAML while preserving
// any other top-level or profile keys (such as dns, proxy-groups, or append-rules).
func BuildProfileMixinYAML(baseYAML []byte, bypassCNWeb, bypassLAN bool, items []MixinRuleItem) ([]byte, error) {
	normalized, err := NormalizeAndValidateMixinRules(items)
	if err != nil {
		return nil, err
	}

	var doc map[string]any
	if len(strings.TrimSpace(string(baseYAML))) > 0 {
		doc, err = profile.UnmarshalYAMLMap(baseYAML)
		if err != nil {
			return nil, err
		}
	}
	if doc == nil {
		doc = map[string]any{}
	}

	var lines []string
	seen := map[string]bool{}
	addLine := func(s string) {
		if !seen[s] {
			seen[s] = true
			lines = append(lines, s)
		}
	}

	for _, item := range normalized {
		addLine(FormatMixinRuleItem(item))
	}
	if bypassLAN {
		addLine("GEOSITE,private,DIRECT")
		addLine("GEOIP,LAN,DIRECT,no-resolve")
	}
	if bypassCNWeb {
		addLine("GEOSITE,cn,DIRECT")
		addLine("GEOIP,CN,DIRECT,no-resolve")
	}

	profMap, _ := doc["profile"].(map[string]any)
	if len(lines) > 0 {
		if profMap == nil {
			profMap = map[string]any{}
		}
		profMap["prepend-rules"] = lines
		doc["profile"] = profMap
	} else if profMap != nil {
		delete(profMap, "prepend-rules")
		if len(profMap) == 0 {
			delete(doc, "profile")
		} else {
			doc["profile"] = profMap
		}
	}

	if len(doc) == 0 {
		return []byte(profile.DisabledProfileMixinTemplate), nil
	}

	marshaled, err := yaml.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return append([]byte(ProfileMixinHeaderComment), marshaled...), nil
}

// ExtractMixinRuleKeys returns normalized lookup keys ("type|payload|target") for all
// rules defined in mixinYAML (after resolving PROXY placeholder) so GetEffectiveRules
// can tag them as coming from Profile Mixin.
func ExtractMixinRuleKeys(mixinYAML []byte, defTarget string, literalPROXYExists bool) map[string]bool {
	out := map[string]bool{}
	if len(strings.TrimSpace(string(mixinYAML))) == 0 {
		return out
	}
	doc, err := profile.UnmarshalYAMLMap(mixinYAML)
	if err != nil {
		return out
	}
	var allLines []string
	if prof, ok := doc["profile"].(map[string]any); ok {
		allLines = append(allLines, profile.ExtractStringRules(prof["prepend-rules"])...)
		allLines = append(allLines, profile.ExtractStringRules(prof["append-rules"])...)
	}
	allLines = append(allLines, profile.ExtractStringRules(doc["rules"])...)

	for _, raw := range allLines {
		resolved := profile.ResolveMixinRuleLine(raw, defTarget, literalPROXYExists)
		if item, ok := parseMixinRuleLine(resolved); ok {
			cleanType := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(item.Type)), "-", "")
			key := cleanType + "|" + strings.ToLower(strings.TrimSpace(item.Payload)) + "|" + strings.TrimSpace(item.Target)
			out[key] = true
		}
	}
	return out
}
