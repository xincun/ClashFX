package features

import (
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

func CNAppRuleProviderPayload() []string {
	payload := make([]string, 0, len(CommonCNAppProcesses))
	for _, exe := range CommonCNAppProcesses {
		payload = append(payload, "PROCESS-NAME,"+exe)
	}
	return payload
}

func CNAppBypassRules() []string {
	return []string{"RULE-SET," + profile.CNAppsRuleProviderName + ",DIRECT"}
}

// TransformHookForCNApps inserts the inline clashfx-cn-apps-direct rule-set for
// common domestic Windows apps when EnhancedMode && BypassCNApps are active
// (placed after internal core bypass and AI Proxy Lock rules, before general profile rules).
func TransformHookForCNApps() func(root map[string]any, settings profile.AppSettings) error {
	return func(root map[string]any, settings profile.AppSettings) error {
		if !settings.EnhancedMode || settings.CustomConfigAsIs || !settings.BypassCNApps {
			return nil
		}
		profile.SetInlineClassicalRuleProvider(root, profile.CNAppsRuleProviderName, CNAppRuleProviderPayload())
		bypass := CNAppBypassRules()
		existing := profile.ExtractStringRules(root["rules"])
		splitIdx := profile.SystemGuardPrefixCount(existing)

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
