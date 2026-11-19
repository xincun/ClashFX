package profile

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	RulesetDirName         = "ruleset"
	FallbackRulesetDirName = "clashfx-rulesets"

	AILockProcessNamePattern = `(?i)^(claude|codex|codex-app-server|chatgpt|antigravity|antigravity-cli|agy|language_server_(windows|macos)_[a-z0-9_]+)( helper.*)?(\.exe)?$`
	AILockWindowsPathPattern = `(?i)([\\/](Programs|Program Files( \(x86\))?)[\\/](Claude|AnthropicClaude|Codex|ChatGPT|Antigravity)[\\/].*|[\\/]AppData[\\/]Local[\\/]AnthropicClaude[\\/].*|[\\/]WindowsApps[\\/][^\\/]*(Claude|Anthropic|OpenAI|ChatGPT|Codex|Antigravity)[^\\/]*[\\/].*|[\\/]extensions[\\/]antigravity[\\/].*|(^|[\\/])\.local[\\/](bin|share)[\\/]claude([\\/].*|\.exe)?|(^|[\\/])(\.antigravity|\.gemini[\\/]antigravity-cli)[\\/].*)$`
	AILockDarwinPathPattern  = `(?i)(^|/)(Claude|Codex|CodexCLI|ChatGPT|Antigravity)\.app/Contents/.*$`
)

var DefaultAILockProtectedDomains = []string{
	"claude.ai", "claude.com", "anthropic.com", "claudeusercontent.com",
	"openai.com", "chatgpt.com", "oaistatic.com", "oaiusercontent.com", "oaistatsig.com", "openaimerge.com",
	"antigravity.google", "google.com", "googleapis.com",
}

var DefaultCommonCNAppProcesses = []string{
	// IM & Collaboration
	"Weixin.exe", "WeChat.exe", "WeChatAppEx.exe", "QQ.exe", "TIM.exe",
	"DingTalk.exe", "Feishu.exe", "Lark.exe", "WXWork.exe", "AliIM.exe", "AliWorkbench.exe",
	// Cloud Storage & Downloaders
	"baidunetdisk.exe", "baidunetdiskhost.exe", "aDrive.exe", "QuarkCloudDrive.exe",
	"Thunder.exe", "DownloadSDKServer.exe",
	// Media, Office & Remote Desktop
	"cloudmusic.exe", "QQMusic.exe", "kugou.exe", "Douyin.exe", "BiliBili.exe",
	"QQLive.exe", "QyClient.exe", "wps.exe", "wpp.exe", "et.exe",
	"ToDesk.exe", "SunloginClient.exe",
}

var DefaultDarwinCNAppProcesses = []string{
	// IM & Collaboration (macOS)
	"WeChat", "Weixin", "WeChatAppEx", "QQ", "QQ Helper", "DingTalk", "DingTalk Helper", "DingTalk Helper (Networking)",
	"Feishu", "Feishu Helper", "Feishu Helper (Networking)", "Lark", "Lark Helper", "Lark Helper (Networking)",
	"WXWork", "AliWangWang",
	// Cloud Storage & Downloaders (macOS)
	"baidunetdisk", "BaiduNetdisk_mac", "aDrive", "QuarkCloudDrive", "Thunder",
	// Media, Office & Remote Desktop (macOS)
	"NeteaseMusic", "NeteaseMusic Helper", "QQMusic", "Douyin", "BiliBili", "bilibili",
	"QQLive", "TencentVideo", "iQIYI", "wpsoffice", "WPS Office", "ToDesk", "SunloginClient",
}

// SetFileClassicalRuleProvider registers a local plain-text classical rule-provider
// backed by relPath (resolved by Mihomo relative to its homeDir / ActiveConfigDir).
func SetFileClassicalRuleProvider(root map[string]any, name, relPath string) {
	rpMap, ok := root["rule-providers"].(map[string]any)
	if !ok || rpMap == nil {
		if norm, normOK := NormalizeYAMLValue(root["rule-providers"]).(map[string]any); normOK && norm != nil {
			rpMap = norm
		} else {
			rpMap = map[string]any{}
		}
	}
	rpMap[name] = map[string]any{
		"type":     "file",
		"behavior": "classical",
		"format":   "text",
		"path":     relPath,
	}
	root["rule-providers"] = rpMap
}

func IsEditableBuiltinRuleset(providerName string) bool {
	switch strings.TrimSpace(providerName) {
	case AILockRuleProviderName, PrivateDirectRuleProviderName, CNAppsRuleProviderName:
		return true
	default:
		return false
	}
}

func isReservedOrUnsafeProviderPath(rawPath string) bool {
	trimmed := strings.TrimSpace(rawPath)
	if trimmed == "" {
		return false
	}
	if strings.ContainsAny(trimmed, "\x00\r\n") {
		return true
	}
	// Reject absolute paths (POSIX, Windows drive, UNC) and parent traversals.
	if filepath.IsAbs(trimmed) || strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, `\`) {
		return true
	}
	if len(trimmed) >= 2 && ((trimmed[0] >= 'a' && trimmed[0] <= 'z') || (trimmed[0] >= 'A' && trimmed[0] <= 'Z')) && trimmed[1] == ':' {
		return true
	}
	norm := strings.ReplaceAll(trimmed, `\`, "/")
	clean := filepath.ToSlash(filepath.Clean(norm))
	clean = strings.TrimPrefix(clean, "./")
	if clean == "" || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return true
	}
	low := strings.ToLower(clean)
	base := strings.ToLower(filepath.Base(clean))

	// Never let any third-party provider overwrite ClashFX's own security rulesets,
	// geo databases, mixin, state files, or root-level profiles.
	if strings.HasPrefix(base, "clashfx-") && strings.HasSuffix(base, ".list") {
		return true
	}
	switch base {
	case "settings.json", "subscriptions.json", "node-services.json", "sysproxy-snapshot.json",
		".profile_mixin.yaml", "profile mixin.yaml", ".clashfx_runtime.yaml", ".clashfx_last_good.yaml",
		"country.mmdb", "geosite.dat", "geoip.dat", "asn.mmdb":
		return true
	}
	// Root-level *.yaml / *.yml / *.json files in ActiveConfigDir are profiles or daemon state.
	if !strings.Contains(low, "/") && (strings.HasSuffix(low, ".yaml") || strings.HasSuffix(low, ".yml") || strings.HasSuffix(low, ".json")) {
		return true
	}
	return false
}

// SanitizeUntrustedProviderPaths strips unsafe or ClashFX-reserved `path:` directives
// from untrusted rule-providers and proxy-providers so privileged mihomo_core cannot
// overwrite ClashFX security rulesets (clashfx-ai-lock.list, clashfx-private-direct.list),
// .profile_mixin.yaml, settings, or profiles when fetching remote providers.
// Returns true if any provider entry was modified.
func SanitizeUntrustedProviderPaths(root map[string]any) bool {
	if root == nil {
		return false
	}
	changed := false
	for _, sectionKey := range []string{"rule-providers", "proxy-providers"} {
		rawSec, exists := root[sectionKey]
		if !exists || rawSec == nil {
			continue
		}
		secMap, ok := rawSec.(map[string]any)
		if !ok || secMap == nil {
			if norm, nOK := NormalizeYAMLValue(rawSec).(map[string]any); nOK && norm != nil {
				secMap = norm
			} else {
				continue
			}
		}
		secModified := false
		for provName, rawProv := range secMap {
			if sectionKey == "rule-providers" && IsEditableBuiltinRuleset(provName) {
				continue
			}
			provMap, pOK := rawProv.(map[string]any)
			if !pOK || provMap == nil {
				if norm, nOK := NormalizeYAMLValue(rawProv).(map[string]any); nOK && norm != nil {
					provMap = norm
				} else {
					continue
				}
			}
			rawPath, hasPath := provMap["path"]
			if !hasPath || rawPath == nil {
				continue
			}
			pathStr := fmt.Sprint(rawPath)
			pType := strings.ToLower(strings.TrimSpace(fmt.Sprint(provMap["type"])))
			_, hasURL := provMap["url"]
			// Any remote/HTTP fetcher provider (which writes downloaded bytes to `path`)
			// must never target reserved ClashFX paths or escape homeDir.
			// File providers also must not escape homeDir.
			if pType == "http" || hasURL {
				if isReservedOrUnsafeProviderPath(pathStr) {
					delete(provMap, "path")
					secMap[provName] = provMap
					secModified = true
					changed = true
				}
			} else if isReservedOrUnsafeProviderPath(pathStr) {
				// Even non-HTTP providers must never escape homeDir or point at ClashFX state files.
				norm := strings.ReplaceAll(strings.TrimSpace(pathStr), `\`, "/")
				if filepath.IsAbs(pathStr) || strings.HasPrefix(norm, "/") || strings.HasPrefix(norm, "..") || strings.Contains(norm, "/../") {
					delete(provMap, "path")
					secMap[provName] = provMap
					secModified = true
					changed = true
				}
			}
		}
		if secModified {
			root[sectionKey] = secMap
		}
	}
	return changed
}

// ResolveRulesetDirName returns "ruleset" when cfgDir/ruleset can be created and
// written to by the current user, and falls back to "clashfx-rulesets" when the
// legacy "ruleset" directory is root-owned or otherwise unwritable.
func ResolveRulesetDirName(cfgDir string) string {
	trimmed := strings.TrimSpace(cfgDir)
	if trimmed == "" {
		return RulesetDirName
	}
	primaryDir := filepath.Join(trimmed, RulesetDirName)
	if err := os.MkdirAll(primaryDir, 0700); err != nil {
		return FallbackRulesetDirName
	}
	probe, err := os.CreateTemp(primaryDir, ".clashfx-probe-*")
	if err != nil {
		return FallbackRulesetDirName
	}
	probePath := probe.Name()
	_ = probe.Close()
	_ = os.Remove(probePath)
	return RulesetDirName
}

func BuiltinRulesetRelPath(providerName string) string {
	return "./" + RulesetDirName + "/" + strings.TrimSpace(providerName) + ".list"
}

func BuiltinRulesetRelPathInDir(cfgDir, providerName string) string {
	return "./" + ResolveRulesetDirName(cfgDir) + "/" + strings.TrimSpace(providerName) + ".list"
}

func BuiltinRulesetAbsPath(cfgDir, providerName string) string {
	return filepath.Join(cfgDir, ResolveRulesetDirName(cfgDir), strings.TrimSpace(providerName)+".list")
}

func DefaultBuiltinRulesetText(providerName string) (string, bool) {
	switch strings.TrimSpace(providerName) {
	case AILockRuleProviderName:
		return DefaultAILockRulesetText(), true
	case PrivateDirectRuleProviderName:
		return DefaultPrivateDirectRulesetText(), true
	case CNAppsRuleProviderName:
		return DefaultCNAppsRulesetText(), true
	default:
		return "", false
	}
}

func DefaultPrivateDirectRulesetText() string {
	var b strings.Builder
	b.WriteString("# ClashFX 内网与私有直连规则集 (clashfx-private-direct.list)\n")
	b.WriteString("# 格式说明：每行写一条无目标策略的 Classical 规则（无需写 ,DIRECT），顶层优先直连（高于 AI 锁定）。\n")
	b.WriteString("# 本文件中的域名会自动同步到 dns.fake-ip-filter（跳过 Fake-IP）与 dns.nameserver-policy（走系统/企业 DNS）。\n")
	b.WriteString("# 可按需自由增删公司内网域名或网段（如 DOMAIN-SUFFIX,dingtalk.test 或 IP-CIDR,30.0.0.0/8,no-resolve），保存后自动重载生效。\n\n")
	b.WriteString("# --- 回环与私有网段 (no-resolve) ---\n")
	for _, cidr := range PrivateIPCIDRs {
		b.WriteString("IP-CIDR," + cidr + ",no-resolve\n")
	}
	for _, cidr := range PrivateIPv6CIDRs {
		b.WriteString("IP-CIDR6," + cidr + ",no-resolve\n")
	}
	b.WriteString("\n# --- 私有 / 局域网 / 内网域名后缀 ---\n")
	b.WriteString("DOMAIN,localhost\n")
	for _, suffix := range PrivateDomainSuffixes {
		b.WriteString("DOMAIN-SUFFIX," + suffix + "\n")
	}
	b.WriteString("DOMAIN-REGEX," + DotlessHostnameRegex + "\n")
	return b.String()
}

func DefaultAILockRulesetText() string {
	var b strings.Builder
	b.WriteString("# ClashFX AI 专用锁定规则集 (clashfx-ai-lock.list)\n")
	b.WriteString("# 格式说明：每行写一条无目标策略的 Classical 规则（无需写 ,DIRECT 或节点名，出口由 AI 锁定统一接管）。\n")
	b.WriteString("# 以 # 开头的注释行和空行会被自动忽略；修改后可零中断热更新生效。\n\n")
	b.WriteString("# --- AI 客户端进程与安装目录 (需 TUN 增强模式) ---\n")
	b.WriteString("PROCESS-NAME-REGEX," + AILockProcessNamePattern + "\n")
	b.WriteString("PROCESS-PATH-REGEX," + AILockWindowsPathPattern + "\n")
	b.WriteString("PROCESS-PATH-REGEX," + AILockDarwinPathPattern + "\n\n")
	b.WriteString("# --- AI 与 Google 生态域名后缀 ---\n")
	for _, dom := range DefaultAILockProtectedDomains {
		b.WriteString("DOMAIN-SUFFIX," + dom + "\n")
	}
	return b.String()
}

func DefaultCNAppsRulesetText() string {
	var b strings.Builder
	b.WriteString("# ClashFX 常用国内桌面应用直连规则集 (clashfx-cn-apps-direct.list)\n")
	b.WriteString("# 格式说明：每行写一条无目标策略的 Classical 规则（如 PROCESS-NAME,Weixin.exe 或 PROCESS-NAME,WeChat），顶层统一路由至 DIRECT。\n")
	b.WriteString("# 注意：PROCESS-NAME / PROCESS-PATH 规则仅在开启增强模式 (TUN) 时生效。\n\n")
	for _, exe := range DefaultCommonCNAppProcesses {
		b.WriteString("PROCESS-NAME," + exe + "\n")
	}
	for _, proc := range DefaultDarwinCNAppProcesses {
		b.WriteString("PROCESS-NAME," + proc + "\n")
	}
	return b.String()
}

// EnsureBuiltinRulesetFiles creates ruleset/clashfx-ai-lock.list,
// ruleset/clashfx-private-direct.list, and ruleset/clashfx-cn-apps-direct.list
// inside cfgDir (or clashfx-rulesets/ if the legacy ruleset/ directory is not
// writable by the current user), seeding from any readable legacy file and
// preserving user edits on disk.
func EnsureBuiltinRulesetFiles(cfgDir string) error {
	if strings.TrimSpace(cfgDir) == "" {
		return nil
	}
	dirName := ResolveRulesetDirName(cfgDir)
	for _, name := range []string{AILockRuleProviderName, PrivateDirectRuleProviderName, CNAppsRuleProviderName} {
		absPath := filepath.Join(cfgDir, dirName, strings.TrimSpace(name)+".list")
		if _, err := os.Stat(absPath); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		initText, _ := DefaultBuiltinRulesetText(name)
		if dirName != RulesetDirName {
			legacyPath := filepath.Join(cfgDir, RulesetDirName, strings.TrimSpace(name)+".list")
			if legacyBytes, rErr := os.ReadFile(legacyPath); rErr == nil {
				if rules, pErr := ParseAndValidateClassicalRulesetText(string(legacyBytes)); pErr == nil && len(rules) > 0 {
					initText = NormalizeRulesetContent(string(legacyBytes))
				}
			}
		}
		if err := WriteFileAtomic(absPath, []byte(initText), 0600); err != nil {
			return fmt.Errorf("init ruleset %s: %w", name, err)
		}
	}
	return nil
}

// ReadPrivateDirectRules loads and parses the user-editable clashfx-private-direct.list
// ruleset on disk. If the file exists and parses cleanly (even if emptied by the user),
// its exact rules are returned; DefaultPrivateDirectRulePayload is only used as fallback
// when no readable ruleset file exists yet.
func ReadPrivateDirectRules(cfgDir string) []string {
	trimmed := strings.TrimSpace(cfgDir)
	if trimmed != "" {
		candidates := []string{
			BuiltinRulesetAbsPath(trimmed, PrivateDirectRuleProviderName),
			filepath.Join(trimmed, RulesetDirName, PrivateDirectRuleProviderName+".list"),
		}
		for _, p := range candidates {
			raw, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			rules, pErr := ParseAndValidateClassicalRulesetText(string(raw))
			if pErr == nil {
				return rules
			}
			break
		}
	}
	return DefaultPrivateDirectRulePayload()
}

// ExtractPrivateDirectDNSPatterns converts domain-based classical rules from
// clashfx-private-direct.list into Mihomo DomainTrie patterns suitable for
// dns.fake-ip-filter and dns.nameserver-policy.
func ExtractPrivateDirectDNSPatterns(rules []string) []string {
	seen := make(map[string]bool, len(rules))
	var out []string
	add := func(pat string) {
		p := strings.TrimSpace(pat)
		if p == "" {
			return
		}
		key := strings.ToLower(p)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, p)
	}
	for _, line := range rules {
		tp, rest, ok := strings.Cut(line, ",")
		if !ok {
			continue
		}
		tp = strings.ToUpper(strings.TrimSpace(tp))
		payload := strings.TrimSpace(rest)
		if payload == "" {
			continue
		}
		switch tp {
		case "DOMAIN":
			host := strings.TrimRight(strings.SplitN(payload, ",", 2)[0], ".")
			if host != "" && !strings.ContainsAny(host, " \t\r\n/:") && !strings.Contains(host, "..") {
				add(host)
			}
		case "DOMAIN-SUFFIX":
			suf := strings.Trim(strings.SplitN(payload, ",", 2)[0], ".")
			if suf != "" && !strings.ContainsAny(suf, " \t\r\n/:*") && !strings.Contains(suf, "..") {
				add("+." + suf)
			}
		case "DOMAIN-WILDCARD":
			wc := strings.TrimRight(strings.SplitN(payload, ",", 2)[0], ".")
			if wc == "" || strings.ContainsAny(wc, " \t\r\n/:") || strings.Contains(wc, "..") {
				continue
			}
			if strings.HasPrefix(wc, "*.") {
				suffix := strings.TrimLeft(strings.TrimPrefix(wc, "*."), ".")
				if suffix != "" && !strings.Contains(suffix, "*") {
					add("+." + suffix)
					continue
				}
			}
			add(wc)
		case "DOMAIN-REGEX":
			if payload == DotlessHostnameRegex {
				add("*")
			}
		}
	}
	return out
}

// ExtractPrivateDirectCIDRs extracts IPv4/IPv6 CIDR prefixes from clashfx-private-direct.list
// so OS-level AI Guard firewalls (WFP/PF) stay consistent with user-configured intranet subnets.
func ExtractPrivateDirectCIDRs(rules []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, line := range rules {
		tp, rest, ok := strings.Cut(line, ",")
		if !ok {
			continue
		}
		tp = strings.ToUpper(strings.TrimSpace(tp))
		if tp != "IP-CIDR" && tp != "IP-CIDR6" {
			continue
		}
		cidrStr := strings.TrimSpace(strings.SplitN(rest, ",", 2)[0])
		pfx, err := netip.ParsePrefix(cidrStr)
		if err != nil {
			continue
		}
		norm := pfx.Masked().String()
		if !seen[norm] {
			seen[norm] = true
			out = append(out, norm)
		}
	}
	return out
}

func (s *Store) BuiltinRulesetPath(providerName string) string {
	return BuiltinRulesetAbsPath(s.ActiveConfigDir(), providerName)
}

func (s *Store) EnsureBuiltinRulesets() error {
	return EnsureBuiltinRulesetFiles(s.ActiveConfigDir())
}

// NormalizeRulesetContent normalizes CRLF to LF and ensures a trailing newline.
func NormalizeRulesetContent(raw string) string {
	s := strings.ReplaceAll(raw, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.TrimRight(s, "\n ")
	if s == "" {
		return "\n"
	}
	return s + "\n"
}

var commaPayloadClassicalTypes = map[string]bool{
	"DOMAIN-REGEX":       true,
	"PROCESS-NAME-REGEX": true,
	"PROCESS-PATH-REGEX": true,
	"AND":                true,
	"OR":                 true,
	"NOT":                true,
}

var standardClassicalTypes = map[string]bool{
	"DOMAIN":                true,
	"DOMAIN-SUFFIX":         true,
	"DOMAIN-KEYWORD":        true,
	"DOMAIN-WILDCARD":       true,
	"GEOSITE":               true,
	"GEOIP":                 true,
	"SRC-GEOIP":             true,
	"IP-ASN":                true,
	"SRC-IP-ASN":            true,
	"IP-CIDR":               true,
	"IP-CIDR6":              true,
	"SRC-IP-CIDR":           true,
	"IP-SUFFIX":             true,
	"SRC-IP-SUFFIX":         true,
	"SRC-PORT":              true,
	"DST-PORT":              true,
	"IN-PORT":               true,
	"DSCP":                  true,
	"PROCESS-NAME":          true,
	"PROCESS-PATH":          true,
	"PROCESS-NAME-WILDCARD": true,
	"PROCESS-PATH-WILDCARD": true,
	"NETWORK":               true,
	"UID":                   true,
	"IN-TYPE":               true,
	"IN-USER":               true,
	"IN-NAME":               true,
}

// ParseAndValidateClassicalRulesetText extracts active rule lines from a Mihomo
// format:text classical ruleset and validates syntax so Mihomo never silently drops a typo line.
func ParseAndValidateClassicalRulesetText(content string) ([]string, error) {
	normalized := strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", "\n"), "\r", "\n")
	rawLines := strings.Split(normalized, "\n")
	var rules []string

	for idx, rawLine := range rawLines {
		lineNo := idx + 1
		trimmed := strings.TrimSpace(rawLine)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "//") {
			continue
		}

		parts := strings.Split(trimmed, ",")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		tp := strings.ToUpper(parts[0])
		if len(parts) < 2 {
			return nil, fmt.Errorf("第 %d 行格式有误（缺少逗号与匹配值）：%q，正确示例：DOMAIN-SUFFIX,example.com", lineNo, trimmed)
		}

		switch tp {
		case "MATCH", "RULE-SET", "SUB-RULE":
			return nil, fmt.Errorf("第 %d 行：Classical 规则集内部不支持嵌套 %s 规则", lineNo, tp)
		}

		if commaPayloadClassicalTypes[tp] {
			if len(parts) >= 3 {
				tailUpper := strings.ToUpper(parts[len(parts)-1])
				if tailUpper == "DIRECT" || tailUpper == "REJECT" || tailUpper == "REJECT-DROP" || tailUpper == "PROXY" {
					return nil, fmt.Errorf("第 %d 行无需在末尾填写分流目标 %q（规则集出口由顶层统一指定）", lineNo, parts[len(parts)-1])
				}
			}
			payload := strings.TrimSpace(strings.Join(parts[1:], ","))
			if payload == "" {
				return nil, fmt.Errorf("第 %d 行匹配内容不能为空：%q", lineNo, trimmed)
			}
			if strings.HasSuffix(tp, "-REGEX") {
				if _, err := regexp.Compile(payload); err != nil {
					return nil, fmt.Errorf("第 %d 行正则表达式无效：%v", lineNo, err)
				}
			}
			rules = append(rules, tp+","+payload)
			continue
		}

		if !standardClassicalTypes[tp] {
			return nil, fmt.Errorf("第 %d 行不支持的规则类型 %q", lineNo, parts[0])
		}

		payload := parts[1]
		if payload == "" {
			return nil, fmt.Errorf("第 %d 行匹配内容不能为空：%q", lineNo, trimmed)
		}

		if len(parts) > 2 {
			for _, extra := range parts[2:] {
				lowerExtra := strings.ToLower(extra)
				if lowerExtra != "no-resolve" && lowerExtra != "src" {
					return nil, fmt.Errorf("第 %d 行无需填写策略目标或未知参数 %q（规则集内请直接写 %s,%s）", lineNo, extra, tp, payload)
				}
			}
		}

		if tp == "IP-CIDR" || tp == "IP-CIDR6" || tp == "SRC-IP-CIDR" {
			if _, err := netip.ParsePrefix(payload); err != nil {
				return nil, fmt.Errorf("第 %d 行网段格式无效 %q（需包含掩码，如 192.168.1.1/32）：%v", lineNo, payload, err)
			}
		}

		normalizedParts := append([]string{tp, payload}, parts[2:]...)
		rules = append(rules, strings.Join(normalizedParts, ","))
	}

	return rules, nil
}
