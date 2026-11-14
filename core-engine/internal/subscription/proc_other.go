//go:build !windows

package subscription

import "os/exec"

func configureChildProcess(_ *exec.Cmd) {}
