//go:build windows

package subscription

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// configureChildProcess prevents Windows from flashing a temporary console
// window when clashfx-daemon validates configs via mihomo_core.exe -t.
func configureChildProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
}
