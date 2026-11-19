package ailock

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/clashfx/core-engine/internal/profile"
)

func TestDiscoverProtectedExecutablesWindowsAndLanguageServer(t *testing.T) {
	cfgDir := t.TempDir()
	rulesetDir := filepath.Join(cfgDir, profile.RulesetDirName)
	if err := os.MkdirAll(rulesetDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(rulesetDir, profile.AILockRuleProviderName+".list"),
		[]byte(profile.DefaultAILockRulesetText()),
		0600,
	); err != nil {
		t.Fatal(err)
	}

	running := []ProcessEntry{
		{PID: 100, Name: "clashfx-daemon.exe", ExePath: `C:\Program Files\ClashFX\clashfx-daemon.exe`},
		{PID: 101, Name: "mihomo_core.exe", ExePath: `C:\Program Files\ClashFX\mihomo_core.exe`},
		{PID: 201, Name: "Antigravity.exe", ExePath: `C:\Users\alice\AppData\Local\Programs\Antigravity\Antigravity.exe`},
		{PID: 202, Name: "language_server_windows_x64.exe", ExePath: `C:\Users\alice\AppData\Local\Programs\Antigravity\resources\app\extensions\antigravity\bin\language_server_windows_x64.exe`},
		{PID: 203, Name: "language_server_windows_arm64.exe", ExePath: `D:\CustomTools\MyIDE\bin\language_server_windows_arm64.exe`},
		{PID: 204, Name: "claude.exe", ExePath: `C:\Users\alice\.local\bin\claude.exe`},
		{PID: 205, Name: "DingTalk.exe", ExePath: `C:\Program Files (x86)\DingDing\DingTalk.exe`},
		{PID: 206, Name: "chrome.exe", ExePath: `C:\Program Files\Google\Chrome\Application\chrome.exe`},
	}

	found := DiscoverProtectedExecutablesWithInputs(cfgDir, "windows", nil, running)
	wantPresent := []string{
		`C:\Users\alice\AppData\Local\Programs\Antigravity\Antigravity.exe`,
		`C:\Users\alice\AppData\Local\Programs\Antigravity\resources\app\extensions\antigravity\bin\language_server_windows_x64.exe`,
		`D:\CustomTools\MyIDE\bin\language_server_windows_arm64.exe`,
		`C:\Users\alice\.local\bin\claude.exe`,
	}
	for _, want := range wantPresent {
		ok := false
		for _, got := range found {
			if strings.EqualFold(got, want) {
				ok = true
				break
			}
		}
		if !ok {
			t.Fatalf("expected %q in discovered list, got %v", want, found)
		}
	}
	for _, got := range found {
		low := strings.ToLower(got)
		if strings.Contains(low, "dingtalk") || strings.Contains(low, "chrome") || strings.Contains(low, "clashfx") || strings.Contains(low, "mihomo") {
			t.Fatalf("unexpected non-AI or own binary discovered: %q", got)
		}
	}
}
