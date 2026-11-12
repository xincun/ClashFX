//go:build windows

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"syscall"
	"time"

	"github.com/Microsoft/go-winio"
)

func configureChildProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
}

// Call sends a single JSON request to the privileged Windows Service Named Pipe.
func Call(ctx context.Context, req Request) (Response, error) {
	timeout := 15 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		if rem := time.Until(deadline); rem > 0 {
			timeout = rem
		}
	}
	conn, err := winio.DialPipeContext(ctx, ServicePipeName)
	if err != nil {
		return Response{}, fmt.Errorf("connect %s: %w", ServicePipeName, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return Response{}, fmt.Errorf("send service request: %w", err)
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return Response{}, fmt.Errorf("decode service response: %w", err)
	}
	return resp, nil
}

// ListenSecurePipe creates the ClashFXService Named Pipe protected by an explicit SDDL DACL:
//   - Protected DACL (D:P)
//   - SYSTEM (SY): Generic All
//   - Built-in Administrators (BA): Generic All
//   - Authenticated Users (AU): Generic Read + Generic Write (blocks Anonymous & untrusted AppContainers)
func ListenSecurePipe() (net.Listener, error) {
	cfg := &winio.PipeConfig{
		SecurityDescriptor: "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;AU)",
		InputBufferSize:    16 * 1024,
		OutputBufferSize:   16 * 1024,
	}
	return winio.ListenPipe(ServicePipeName, cfg)
}
