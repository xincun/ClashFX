//go:build windows

package subscription

import (
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

func TestConfigureChildProcessHidesWindow(t *testing.T) {
	cmd := exec.Command("mihomo_core.exe", "-t")
	configureChildProcess(cmd)
	if cmd.SysProcAttr == nil {
		t.Fatal("expected non-nil SysProcAttr")
	}
	if !cmd.SysProcAttr.HideWindow {
		t.Fatal("expected HideWindow=true")
	}
	if cmd.SysProcAttr.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Fatalf("expected CREATE_NO_WINDOW in CreationFlags, got 0x%x", cmd.SysProcAttr.CreationFlags)
	}
}
