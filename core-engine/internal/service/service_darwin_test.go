//go:build darwin

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
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

	// Verify kern.procargs2 resolves our own executable path and peer allowlist filters properly.
	selfExe, err := peerExecutablePathDarwin(os.Getpid())
	if err != nil || selfExe == "" {
		t.Fatalf("peerExecutablePathDarwin(self) failed: %v (%q)", err, selfExe)
	}
	if isAllowedDarwinPeerExecutable("/usr/bin/python3", trustedDir) {
		t.Fatal("expected python3 to be rejected as darwin service peer")
	}
	if !isAllowedDarwinPeerExecutable("/Applications/ClashFX.app/Contents/MacOS/clashfx-daemon", trustedDir) {
		t.Fatal("expected ClashFX.app clashfx-daemon to be allowed as darwin service peer")
	}
	if !isAllowedDarwinPeerExecutable(filepath.Join(trustedDir, "clashfx-service"), trustedDir) {
		t.Fatal("expected trustedDir clashfx-service to be allowed as darwin service peer")
	}

	// Verify socket identity guard prevents unlinking a replaced socket inode.
	sockID := StatSocketIdentity(sockPath)
	if !sockID.OK {
		t.Fatal("expected valid socket identity")
	}
	_ = ln.Close()
	if err := os.Remove(sockPath); err != nil {
		t.Fatalf("remove old socket: %v", err)
	}
	if err := os.WriteFile(sockPath, []byte("new-instance"), 0600); err != nil {
		t.Fatalf("write replacement file: %v", err)
	}
	RemoveSocketIfMatches(sockPath, sockID)
	if _, err := os.Stat(sockPath); err != nil {
		t.Fatalf("RemoveSocketIfMatches deleted a different inode: %v", err)
	}

	// Verify macOS TUN health helper ignores link-local fe80:: utun addrs and accepts Mihomo 198.18.x.x.
	_, llNet, _ := net.ParseCIDR("fe80::1/64")
	if ifaceHasMihomoTunAddr([]net.Addr{llNet}, false) {
		t.Fatal("expected link-local IPv6 utun address not to be treated as Mihomo TUN")
	}
	_, tunNet, _ := net.ParseCIDR("198.18.0.1/30")
	if !ifaceHasMihomoTunAddr([]net.Addr{llNet, tunNet}, false) {
		t.Fatal("expected 198.18.0.1/30 utun address to be recognized as Mihomo TUN")
	}
}
