package features

import (
	"github.com/clashfx/core-engine/internal/profile"
)

// CommonCNAppProcesses lists mainstream Windows Chinese desktop client executables
// bypassed via DIRECT when BypassCNApps is enabled in Enhanced Mode (TUN).
var CommonCNAppProcesses = append(append([]string(nil), profile.DefaultCommonCNAppProcesses...), profile.DefaultDarwinCNAppProcesses...)

func CNAppRuleProviderPayload() []string {
	payload := make([]string, 0, len(CommonCNAppProcesses))
	for _, proc := range CommonCNAppProcesses {
		payload = append(payload, "PROCESS-NAME,"+proc)
	}
	return payload
}

func CNAppBypassRules() []string {
	return []string{"RULE-SET," + profile.CNAppsRuleProviderName + ",DIRECT"}
}

// TransformHookForCNApps inserts the file-backed clashfx-cn-apps-direct rule-set for
// common domestic Windows apps when EnhancedMode && BypassCNApps are active
// (placed after internal core bypass and AI Proxy Lock rules, before general profile rules).
func TransformHookForCNApps() func(root map[string]any, settings profile.AppSettings) error {
	return func(root map[string]any, settings profile.AppSettings) error {
		if !settings.EnhancedMode || settings.CustomConfigAsIs || !settings.BypassCNApps {
			return nil
		}
		profile.SetFileClassicalRuleProvider(
			root,
			profile.CNAppsRuleProviderName,
			profile.BuiltinRulesetRelPath(profile.CNAppsRuleProviderName),
		)
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
