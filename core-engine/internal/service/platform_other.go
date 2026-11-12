//go:build !windows

package service

import (
	"context"
	"errors"
	"net"
	"os/exec"
)

func configureChildProcess(_ *exec.Cmd) {}

func Call(_ context.Context, _ Request) (Response, error) {
	return Response{}, errors.New("Windows service pipe is only supported on Windows")
}

func ListenSecurePipe() (net.Listener, error) {
	return nil, errors.New("Windows service pipe is only supported on Windows")
}
