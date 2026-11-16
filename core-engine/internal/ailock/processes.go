package ailock

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/clashfx/core-engine/internal/profile"
)

// ProcessEntry describes a running OS process inspected for AI Proxy Lock cleanup.
type ProcessEntry struct {
	PID     int
	Name    string
	ExePath string
}

// RunningAIClientsResult summarizes currently running protected AI client apps.
type RunningAIClientsResult struct {
	Names    []string `json:"names"`
	PIDCount int      `json:"pid_count"`
}

// ProcessMatcher evaluates whether an OS process matches protected AI client rules.
type ProcessMatcher struct {
	exactNames   []string
	exactPaths   []string
	nameRegexes  []*regexp.Regexp
	pathRegexes  []*regexp.Regexp
}

var (
	reOuterMacApp       = regexp.MustCompile(`(?i)/([^/]+)\.app/Contents/`)
	reWinStorePkg       = regexp.MustCompile(`(?i)[\\/]WindowsApps[\\/][^\\/]*(Claude|Anthropic|OpenAI|ChatGPT|Codex|Antigravity)[^\\/]*[\\/]`)
	reWinAppDirSegment  = regexp.MustCompile(`(?i)[\\/](AnthropicClaude|Claude|Codex|ChatGPT|Antigravity)[\\/]`)
	reTrailingHelperApp = regexp.MustCompile(`(?i)\s+helper(\s*\([^)]*\))?$`)

	clashfxOwnBinaries = map[string]bool{
		"clashfx":             true,
		"clashfx.exe":         true,
		"clashfx-ui":          true,
		"clashfx-ui.exe":      true,
		"clashfx-daemon":      true,
		"clashfx-daemon.exe":  true,
		"clashfx-service":     true,
		"clashfx-service.exe": true,
		"mihomo_core":         true,
		"mihomo_core.exe":     true,
	}
)

func wildcardToRegexp(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("(?i)^")
	for _, r := range pattern {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// CompileProcessMatchers builds a ProcessMatcher from the active clashfx-ai-lock
// ruleset on disk (if readable) combined with ClashFX's built-in AI client patterns.
func CompileProcessMatchers(cfgDir string, goos string) *ProcessMatcher {
	m := &ProcessMatcher{}
	seenRules := map[string]bool{}

	addRule := func(tp, payload string) {
		tp = strings.ToUpper(strings.TrimSpace(tp))
		payload = strings.TrimSpace(payload)
		if tp == "" || payload == "" {
			return
		}
		key := tp + "," + payload
		if seenRules[key] {
			return
		}
		seenRules[key] = true

		switch tp {
		case "PROCESS-NAME":
			m.exactNames = append(m.exactNames, payload)
		case "PROCESS-PATH":
			m.exactPaths = append(m.exactPaths, payload)
		case "PROCESS-NAME-REGEX":
			if re, err := regexp.Compile(payload); err == nil {
				m.nameRegexes = append(m.nameRegexes, re)
			}
		case "PROCESS-PATH-REGEX":
			if re, err := regexp.Compile(payload); err == nil {
				m.pathRegexes = append(m.pathRegexes, re)
			}
		case "PROCESS-NAME-WILDCARD":
			if re, err := wildcardToRegexp(payload); err == nil {
				m.nameRegexes = append(m.nameRegexes, re)
			}
		case "PROCESS-PATH-WILDCARD":
			if re, err := wildcardToRegexp(payload); err == nil {
				m.pathRegexes = append(m.pathRegexes, re)
			}
		}
	}

	// Always include built-in matchers so detection works even before first ruleset write.
	addRule("PROCESS-NAME-REGEX", profile.AILockProcessNamePattern)
	switch goos {
	case "darwin":
		addRule("PROCESS-PATH-REGEX", profile.AILockDarwinPathPattern)
	case "windows":
		addRule("PROCESS-PATH-REGEX", profile.AILockWindowsPathPattern)
	}

	if strings.TrimSpace(cfgDir) != "" {
		candidates := []string{
			profile.BuiltinRulesetAbsPath(cfgDir, profile.AILockRuleProviderName),
			filepath.Join(cfgDir, profile.RulesetDirName, profile.AILockRuleProviderName+".list"),
		}
		for _, p := range candidates {
			raw, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			if rules, pErr := profile.ParseAndValidateClassicalRulesetText(string(raw)); pErr == nil {
				for _, r := range rules {
					if tp, payload, ok := strings.Cut(r, ","); ok {
						addRule(tp, payload)
					}
				}
			}
			break
		}
	}

	return m
}

// Matches reports whether p matches any process rule in m.
func (m *ProcessMatcher) Matches(p ProcessEntry) bool {
	if m == nil {
		return false
	}
	name := strings.TrimSpace(p.Name)
	if name == "" && p.ExePath != "" {
		name = filepath.Base(p.ExePath)
	}
	exePath := strings.TrimSpace(p.ExePath)
	if exePath == "" {
		exePath = name
	}

	for _, want := range m.exactNames {
		if strings.EqualFold(name, want) {
			return true
		}
	}
	for _, want := range m.exactPaths {
		if strings.EqualFold(exePath, want) {
			return true
		}
	}
	if name != "" {
		for _, re := range m.nameRegexes {
			if re.MatchString(name) {
				return true
			}
		}
	}
	if exePath != "" {
		for _, re := range m.pathRegexes {
			if re.MatchString(exePath) {
				return true
			}
		}
	}
	return false
}

func isExcludedSystemOrOwnProcess(p ProcessEntry, selfPID int) bool {
	if p.PID <= 1 || (selfPID > 0 && p.PID == selfPID) {
		return true
	}
	if clashfxOwnBinaries[strings.ToLower(strings.TrimSpace(p.Name))] {
		return true
	}
	if p.ExePath != "" {
		base := strings.ToLower(strings.TrimSpace(filepath.Base(strings.ReplaceAll(p.ExePath, `\`, "/"))))
		if clashfxOwnBinaries[base] {
			return true
		}
	}
	return false
}

// DisplayNameForProcess derives a human-readable application name for UI prompts.
func DisplayNameForProcess(p ProcessEntry, goos string) string {
	switch goos {
	case "darwin":
		if m := reOuterMacApp.FindStringSubmatch(p.ExePath); len(m) >= 2 && strings.TrimSpace(m[1]) != "" {
			return strings.TrimSpace(m[1])
		}
	case "windows":
		if m := reWinStorePkg.FindStringSubmatch(p.ExePath); len(m) >= 2 {
			switch strings.ToLower(strings.TrimSpace(m[1])) {
			case "anthropic", "claude":
				return "Claude"
			case "openai", "chatgpt":
				return "ChatGPT"
			case "codex":
				return "Codex"
			case "antigravity":
				return "Antigravity"
			}
		}
		if m := reWinAppDirSegment.FindStringSubmatch(p.ExePath); len(m) >= 2 {
			seg := strings.TrimSpace(m[1])
			if strings.EqualFold(seg, "AnthropicClaude") {
				return "Claude"
			}
			return seg
		}
	}

	base := strings.TrimSpace(p.Name)
	if base == "" && p.ExePath != "" {
		base = filepath.Base(strings.ReplaceAll(p.ExePath, `\`, "/"))
	}
	if len(base) > 4 && strings.EqualFold(base[len(base)-4:], ".exe") {
		base = base[:len(base)-4]
	}
	base = strings.TrimSpace(reTrailingHelperApp.ReplaceAllString(base, ""))
	return base
}

// MatchAIProcesses filters running process entries against matcher and returns matched
// processes alongside sorted, case-insensitively deduplicated app display names.
func MatchAIProcesses(procs []ProcessEntry, matcher *ProcessMatcher, selfPID int, goos string) ([]ProcessEntry, []string) {
	if matcher == nil || len(procs) == 0 {
		return nil, []string{}
	}
	var matched []ProcessEntry
	seenNames := map[string]bool{}
	names := []string{}

	for _, p := range procs {
		if isExcludedSystemOrOwnProcess(p, selfPID) {
			continue
		}
		if !matcher.Matches(p) {
			continue
		}
		matched = append(matched, p)
		disp := DisplayNameForProcess(p, goos)
		if disp == "" {
			continue
		}
		key := strings.ToLower(disp)
		if !seenNames[key] {
			seenNames[key] = true
			names = append(names, disp)
		}
	}
	sort.Strings(names)
	return matched, names
}

// ListRunningAIClients inspects active OS processes and returns protected AI client names.
func ListRunningAIClients(cfgDir string) RunningAIClientsResult {
	procs, err := listOSProcesses()
	if err != nil {
		return RunningAIClientsResult{Names: []string{}}
	}
	matcher := CompileProcessMatchers(cfgDir, runtime.GOOS)
	matched, names := MatchAIProcesses(procs, matcher, os.Getpid(), runtime.GOOS)
	return RunningAIClientsResult{
		Names:    names,
		PIDCount: len(matched),
	}
}

// TerminateRunningAIClients terminates all currently running protected AI client processes
// matched by clashfx-ai-lock before TUN/system proxy teardown.
func TerminateRunningAIClients(cfgDir string) (int, error) {
	procs, err := listOSProcesses()
	if err != nil {
		return 0, err
	}
	matcher := CompileProcessMatchers(cfgDir, runtime.GOOS)
	matched, _ := MatchAIProcesses(procs, matcher, os.Getpid(), runtime.GOOS)
	if len(matched) == 0 {
		return 0, nil
	}
	pids := make([]int, 0, len(matched))
	for _, p := range matched {
		pids = append(pids, p.PID)
	}
	killed := terminatePIDs(pids)

	// Reap any child helpers respawned mid-teardown on Windows/Electron before TUN stops.
	time.Sleep(120 * time.Millisecond)
	if retryProcs, rErr := listOSProcesses(); rErr == nil {
		retryMatched, _ := MatchAIProcesses(retryProcs, matcher, os.Getpid(), runtime.GOOS)
		if len(retryMatched) > 0 {
			secondPassPIDs := make([]int, 0, len(retryMatched))
			for _, p := range retryMatched {
				secondPassPIDs = append(secondPassPIDs, p.PID)
			}
			killed += terminatePIDs(secondPassPIDs)
		}
	}
	return killed, nil
}
