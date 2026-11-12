package features

import (
	"strings"

	"github.com/clashfx/core-engine/internal/profile"
)

// CommonCNAppProcesses lists mainstream Windows Chinese desktop client executables
// bypassed via DIRECT when BypassCNApps is enabled in Enhanced Mode (TUN).
var CommonCNAppProcesses = []string{
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

func CNAppBypassRules() []string {
	rules := make([]string, 0, len(CommonCNAppProcesses))
	for _, exe := range CommonCNAppProcesses {
		rules = append(rules, "PROCESS-NAME,"+exe+",DIRECT")
	}
	return rules
}

// TransformHookForCNApps inserts DIRECT process rules for common domestic Windows
// apps when EnhancedMode && BypassCNApps are active (placed after internal core
// bypass and AI Proxy Lock rules, before general profile rules).
func TransformHookForCNApps() func(root map[string]any, settings profile.AppSettings) error {
	return func(root map[string]any, settings profile.AppSettings) error {
		if !settings.EnhancedMode || settings.CustomConfigAsIs || !settings.BypassCNApps {
			return nil
		}
		bypass := CNAppBypassRules()
		var existing []string
		switch r := root["rules"].(type) {
		case []string:
			existing = r
		case []any:
			for _, item := range r {
				if s, ok := item.(string); ok {
					existing = append(existing, s)
				}
			}
		}

		// Keep internal core self-loop rules and AI Proxy Lock pairs ahead of CN app bypass!
		splitIdx := 0
		for i, rule := range existing {
			trimmed := strings.TrimSpace(rule)
			if strings.HasPrefix(trimmed, "PROCESS-NAME,mihomo_core.exe,") ||
				strings.HasPrefix(trimmed, "PROCESS-NAME,clashfx-") ||
				strings.HasPrefix(trimmed, "PROCESS-NAME-REGEX,") ||
				strings.HasPrefix(trimmed, "PROCESS-PATH-REGEX,") ||
				(i > 0 && strings.HasSuffix(trimmed, ",REJECT")) {
				splitIdx = i + 1
				continue
			}
			// Also keep paired DOMAIN/DOMAIN-SUFFIX AI Lock rules that are immediately followed by ,REJECT
			if i+1 < len(existing) {
				comma := strings.LastIndex(trimmed, ",")
				if comma > 0 && strings.TrimSpace(existing[i+1]) == trimmed[:comma]+",REJECT" {
					splitIdx = i + 2
					continue
				}
			}
			if i >= splitIdx {
				break
			}
		}

		seen := make(map[string]bool, len(existing)+len(bypass))
		out := make([]any, 0, len(existing)+len(bypass))
		appendUnique := func(list []string) {
			for _, s := range list {
				if !seen[s] {
					seen[s] = true
					out = append(out, s)
				}
			}
		}
		appendUnique(existing[:splitIdx])
		appendUnique(bypass)
		appendUnique(existing[splitIdx:])
		root["rules"] = out
		return nil
	}
}
