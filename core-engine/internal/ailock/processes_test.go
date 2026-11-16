package ailock

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/clashfx/core-engine/internal/profile"
)

func TestMatchAIProcessesAndDisplayNames(t *testing.T) {
	cfgDir := t.TempDir()
	rulesetDir := filepath.Join(cfgDir, profile.RulesetDirName)
	if err := os.MkdirAll(rulesetDir, 0700); err != nil {
		t.Fatal(err)
	}
	customRuleset := profile.DefaultAILockRulesetText() + "\nPROCESS-NAME,CustomAICLI\n"
	if err := os.WriteFile(filepath.Join(rulesetDir, profile.AILockRuleProviderName+".list"), []byte(customRuleset), 0600); err != nil {
		t.Fatal(err)
	}

	// macOS bundle + space-containing helper path + CLI + excluded ClashFX processes
	macMatcher := CompileProcessMatchers(cfgDir, "darwin")
	macProcs := []ProcessEntry{
		{PID: 1, Name: "launchd", ExePath: "/sbin/launchd"},
		{PID: 100, Name: "ClashFX", ExePath: "/Applications/ClashFX.app/Contents/MacOS/ClashFX"},
		{PID: 101, Name: "mihomo_core", ExePath: "/Library/Application Support/ClashFX/bin/mihomo_core"},
		{PID: 201, Name: "Antigravity", ExePath: "/Applications/Antigravity.app/Contents/MacOS/Antigravity"},
		{PID: 202, Name: "Antigravity Helper (Renderer)", ExePath: "/Applications/Antigravity.app/Contents/Frameworks/Antigravity Helper (Renderer).app/Contents/MacOS/Antigravity Helper (Renderer)"},
		{PID: 203, Name: "browser_crashpad_handler", ExePath: "/Applications/ChatGPT.app/Contents/Frameworks/Codex Framework.framework/Versions/155.0/Helpers/browser_crashpad_handler"},
		{PID: 204, Name: "claude", ExePath: "/Users/devtest/.local/bin/claude"},
		{PID: 205, Name: "CustomAICLI", ExePath: "/usr/local/bin/CustomAICLI"},
		{PID: 206, Name: "Safari", ExePath: "/Applications/Safari.app/Contents/MacOS/Safari"},
	}

	matchedMac, macNames := MatchAIProcesses(macProcs, macMatcher, 999999, "darwin")
	if len(matchedMac) != 5 {
		t.Fatalf("matchedMac count = %d (%+v), want 5", len(matchedMac), matchedMac)
	}
	wantMacNames := []string{"Antigravity", "ChatGPT", "CustomAICLI", "claude"}
	if !reflect.DeepEqual(macNames, wantMacNames) {
		t.Fatalf("macNames = %v, want %v", macNames, wantMacNames)
	}

	// Windows paths & helper dedup
	winMatcher := CompileProcessMatchers(cfgDir, "windows")
	winProcs := []ProcessEntry{
		{PID: 300, Name: "clashfx-ui.exe", ExePath: `C:\Program Files\ClashFX\clashfx-ui.exe`},
		{PID: 301, Name: "claude.exe", ExePath: `C:\Users\alice\AppData\Local\AnthropicClaude\app-1.0\claude.exe`},
		{PID: 302, Name: "Claude Helper.exe", ExePath: `C:\Users\alice\AppData\Local\AnthropicClaude\app-1.0\Claude Helper.exe`},
		{PID: 303, Name: "codex.exe", ExePath: `C:\Users\alice\.local\bin\codex.exe`},
		{PID: 304, Name: "browser_crashpad_handler.exe", ExePath: `C:\Program Files\WindowsApps\OpenAI.ChatGPT-Desktop_1.2025.1.0_x64__2p2nqsd0c76g0\app\browser_crashpad_handler.exe`},
		{PID: 305, Name: "notepad.exe", ExePath: `C:\Windows\System32\notepad.exe`},
	}

	matchedWin, winNames := MatchAIProcesses(winProcs, winMatcher, 999999, "windows")
	if len(matchedWin) != 4 {
		t.Fatalf("matchedWin count = %d (%+v), want 4", len(matchedWin), matchedWin)
	}
	wantWinNames := []string{"ChatGPT", "Claude", "codex"}
	if !reflect.DeepEqual(winNames, wantWinNames) {
		t.Fatalf("winNames = %v, want %v", winNames, wantWinNames)
	}
}

func TestTerminatePIDsIsolatedSubprocess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sleep subprocess test runs on non-Windows hosts")
	}
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_, _ = cmd.Process.Wait()
	})

	// Reap zombie asynchronously so kill(pid, 0) observes ESRCH once terminated.
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- cmd.Wait()
	}()

	matcher := &ProcessMatcher{exactNames: []string{"sleep"}}
	matched, names := MatchAIProcesses([]ProcessEntry{{PID: pid, Name: "sleep", ExePath: "/bin/sleep"}}, matcher, os.Getpid(), runtime.GOOS)
	if len(matched) != 1 || len(names) != 1 || names[0] != "sleep" {
		t.Fatalf("unexpected match result: matched=%+v names=%v", matched, names)
	}

	killed := terminatePIDs([]int{matched[0].PID})
	if killed != 1 {
		t.Fatalf("terminatePIDs returned %d, want 1", killed)
	}

	select {
	case <-waitDone:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for terminated subprocess to exit")
	}
}
