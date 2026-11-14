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

	AILockProcessNamePattern = `(?i)^(claude|codex|codex-app-server|chatgpt|antigravity|antigravity-cli|agy)( helper.*)?(\.exe)?$`
	AILockWindowsPathPattern = `(?i)([\\/](Programs|Program Files( \(x86\))?)[\\/](Claude|AnthropicClaude|Codex|ChatGPT|Antigravity)[\\/].*|[\\/]AppData[\\/]Local[\\/]AnthropicClaude[\\/].*|[\\/]WindowsApps[\\/][^\\/]*(Claude|Anthropic|OpenAI|ChatGPT|Codex|Antigravity)[^\\/]*[\\/].*|(^|[\\/])\.local[\\/](bin|share)[\\/]claude([\\/].*|\.exe)?|(^|[\\/])(\.antigravity|\.gemini[\\/]antigravity-cli)[\\/].*)$`
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
	"WeChat", "Weixin", "WeChatAppEx", "QQ", "DingTalk", "Feishu", "Lark", "WXWork", "AliWangWang",
	// Cloud Storage & Downloaders (macOS)
	"baidunetdisk", "aDrive", "QuarkCloudDrive", "Thunder",
	// Media, Office & Remote Desktop (macOS)
	"NeteaseMusic", "QQMusic", "Douyin", "BiliBili", "QQLive", "TencentVideo", "iQIYI",
	"wpsoffice", "WPS Office", "ToDesk", "SunloginClient",
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
	case AILockRuleProviderName, CNAppsRuleProviderName:
		return true
	default:
		return false
	}
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
	case CNAppsRuleProviderName:
		return DefaultCNAppsRulesetText(), true
	default:
		return "", false
	}
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

// EnsureBuiltinRulesetFiles creates ruleset/clashfx-ai-lock.list and
// ruleset/clashfx-cn-apps-direct.list inside cfgDir (or clashfx-rulesets/ if
// the legacy ruleset/ directory is not writable by the current user),
// seeding from any readable legacy file and preserving user edits on disk.
func EnsureBuiltinRulesetFiles(cfgDir string) error {
	if strings.TrimSpace(cfgDir) == "" {
		return nil
	}
	dirName := ResolveRulesetDirName(cfgDir)
	for _, name := range []string{AILockRuleProviderName, CNAppsRuleProviderName} {
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
