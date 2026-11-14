//go:build windows

package daemon

import (
	"strings"
	"testing"
)

func TestMutexObjectSuffixSanitizesPaths(t *testing.T) {
	key := `\\.\pipe\ClashFXDaemon-Verify|C:\Temp\clashfx-verify-ui\data-arm64`
	got := mutexObjectSuffix(key)
	if strings.ContainsAny(got, `\/|: `) {
		t.Fatalf("unexpected separator in mutex suffix: %q", got)
	}
	if !strings.HasPrefix(got, "custom-") {
		t.Fatalf("expected custom- prefix, got %q", got)
	}
	if same := mutexObjectSuffix(`\\.\PIPE\clashfxdaemon-verify|c:\temp\clashfx-verify-ui\data-arm64`); same != got {
		t.Fatalf("expected case-insensitive match, got %q vs %q", same, got)
	}
	if def := mutexObjectSuffix("   "); def == "" || strings.HasPrefix(def, "custom-") {
		t.Fatalf("expected username suffix for blank key, got %q", def)
	}
}

func TestAcquireSingleInstanceLockWithPathKey(t *testing.T) {
	key := `ClashFXDaemon-UnitTest|C:\Temp\clashfx-unit\data`
	unlock, err := AcquireSingleInstanceLock(key)
	if err != nil {
		t.Fatalf("AcquireSingleInstanceLock failed: %v", err)
	}
	defer unlock()

	if _, err := AcquireSingleInstanceLock(key); err == nil {
		t.Fatalf("expected second AcquireSingleInstanceLock call to fail while held")
	}
}
