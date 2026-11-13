//go:build !windows

package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func configureChildProcess(_ *exec.Cmd) {}

type childGuard struct{}

func attachChildGuard(_ *exec.Cmd) *childGuard {
	return &childGuard{}
}

func (g *childGuard) activePID(cmd *exec.Cmd, doneCh <-chan error) (bool, int) {
	if cmd == nil || cmd.Process == nil || doneCh == nil {
		return false, 0
	}
	select {
	case <-doneCh:
		return false, 0
	default:
		return true, cmd.Process.Pid
	}
}

func (g *childGuard) closeHandles() {}

func (g *childGuard) terminate(cmd *exec.Cmd, doneCh <-chan error, stopTimeout time.Duration) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	if doneCh != nil {
		select {
		case <-doneCh:
			return nil
		case <-time.After(stopTimeout):
		}
	}
	var stopErr error
	if killErr := cmd.Process.Kill(); killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
		stopErr = fmt.Errorf("kill unresponsive core: %w", killErr)
	}
	if doneCh != nil {
		select {
		case <-doneCh:
		case <-time.After(2 * time.Second):
		}
	}
	return stopErr
}

func Call(_ context.Context, _ Request) (Response, error) {
	return Response{}, errors.New("Windows service pipe is only supported on Windows")
}

func ListenSecurePipe() (net.Listener, error) {
	return nil, errors.New("Windows service pipe is only supported on Windows")
}

func VerifyPipePeer(_ net.Conn, _ string) error {
	return nil
}
