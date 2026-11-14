//go:build darwin

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"testing"
	"time"
)

func TestDarwinUnixSocketRPCAndPeerAuth(t *testing.T) {
	sockPath := fmt.Sprintf("/tmp/cfx-svc-test-%d-%d.sock", os.Getpid(), time.Now().UnixNano())
	defer os.Remove(sockPath)
	t.Setenv("CLASHFX_SERVICE_SOCK", sockPath)

	ln, err := ListenSecurePipe()
	if err != nil {
		t.Fatalf("ListenSecurePipe failed: %v", err)
	}
	defer ln.Close()

	trustedDir := t.TempDir()
	runner := NewRunner(trustedDir)

	errCh := make(chan error, 1)
	go func() {
		conn, aErr := ln.Accept()
		if aErr != nil {
			errCh <- aErr
			return
		}
		defer conn.Close()
		if vErr := VerifyPipePeer(conn, trustedDir); vErr != nil {
			errCh <- vErr
			return
		}
		var req Request
		if dErr := json.NewDecoder(conn).Decode(&req); dErr != nil {
			errCh <- dErr
			return
		}
		errCh <- json.NewEncoder(conn).Encode(runner.Handle(req))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := Call(ctx, Request{Action: "status"})
	if err != nil {
		t.Fatalf("Call status failed: %v", err)
	}
	if !resp.OK || resp.Status == nil || resp.Status.Running {
		t.Fatalf("unexpected service status response: %+v", resp)
	}
	if srvErr := <-errCh; srvErr != nil {
		t.Fatalf("server handler failed: %v", srvErr)
	}

	// Verify net.Pipe (non-UnixConn) is rejected by VerifyPipePeer
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	if err := VerifyPipePeer(c1, trustedDir); err == nil {
		t.Fatal("expected VerifyPipePeer to reject non-Unix socket connection")
	}
}
