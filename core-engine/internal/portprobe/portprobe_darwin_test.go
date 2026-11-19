//go:build darwin

package portprobe

import (
	"errors"
	"net"
	"syscall"
	"testing"
)

type fakeTimeoutErr struct{}

func (fakeTimeoutErr) Error() string   { return "i/o timeout" }
func (fakeTimeoutErr) Timeout() bool   { return true }
func (fakeTimeoutErr) Temporary() bool { return true }

func TestDarwinPrivilegedProbeDisambiguatesCrossUIDTimeWaitFromActiveListener(t *testing.T) {
	eaddrinuse := &net.OpError{Op: "listen", Net: "tcp", Err: syscall.EADDRINUSE}
	econnrefused := &net.OpError{Op: "dial", Net: "tcp4", Err: syscall.ECONNREFUSED}
	wsaAcces := &net.OpError{Op: "listen", Net: "tcp", Err: syscall.Errno(10013)}

	newDummyUDP := func(addr string) (net.PacketConn, error) {
		return net.ListenPacket("udp", "127.0.0.1:0")
	}

	// 1. Non-root (UID 501) hits EADDRINUSE due to root-owned TIME_WAIT, dial returns ECONNREFUSED -> available!
	dialCalled := false
	ok, reserved := probeLoopbackPortDarwinPrivileged(
		7890,
		false,
		func() int { return 501 },
		func(string) (net.Listener, error) { return nil, eaddrinuse },
		func(string) (net.Conn, error) {
			dialCalled = true
			return nil, econnrefused
		},
		newDummyUDP,
	)
	if !dialCalled || !ok || reserved {
		t.Fatalf("cross-UID TIME_WAIT case: dialCalled=%v ok=%v reserved=%v; want true, true, false", dialCalled, ok, reserved)
	}

	// 2. Non-root hits EADDRINUSE with checkUDP=true: TCP is TIME_WAIT only, UDP is free -> available!
	ok, reserved = probeLoopbackPortDarwinPrivileged(
		1053,
		true,
		func() int { return 501 },
		func(string) (net.Listener, error) { return nil, eaddrinuse },
		func(string) (net.Conn, error) { return nil, econnrefused },
		newDummyUDP,
	)
	if !ok || reserved {
		t.Fatalf("cross-UID TIME_WAIT with free UDP: ok=%v reserved=%v; want true, false", ok, reserved)
	}

	// 3. Non-root hits EADDRINUSE with checkUDP=true: TCP is TIME_WAIT only, but UDP is busy -> unavailable!
	ok, reserved = probeLoopbackPortDarwinPrivileged(
		1053,
		true,
		func() int { return 501 },
		func(string) (net.Listener, error) { return nil, eaddrinuse },
		func(string) (net.Conn, error) { return nil, econnrefused },
		func(string) (net.PacketConn, error) { return nil, eaddrinuse },
	)
	if ok || reserved {
		t.Fatalf("cross-UID TIME_WAIT with busy UDP: ok=%v reserved=%v; want false, false", ok, reserved)
	}

	// 4. Non-root hits EADDRINUSE and dial succeeds (active LISTEN socket by another process) -> unavailable!
	ok, reserved = probeLoopbackPortDarwinPrivileged(
		7890,
		false,
		func() int { return 501 },
		func(string) (net.Listener, error) { return nil, eaddrinuse },
		func(string) (net.Conn, error) {
			c1, c2 := net.Pipe()
			_ = c2.Close()
			return c1, nil
		},
		newDummyUDP,
	)
	if ok || reserved {
		t.Fatalf("active listener case: ok=%v reserved=%v; want false, false", ok, reserved)
	}

	// 5. Non-root hits EADDRINUSE and dial times out (not ECONNREFUSED) -> conservatively unavailable!
	ok, reserved = probeLoopbackPortDarwinPrivileged(
		7890,
		false,
		func() int { return 501 },
		func(string) (net.Listener, error) { return nil, eaddrinuse },
		func(string) (net.Conn, error) { return nil, fakeTimeoutErr{} },
		newDummyUDP,
	)
	if ok || reserved {
		t.Fatalf("dial timeout case: ok=%v reserved=%v; want false, false", ok, reserved)
	}

	// 6. Already running as root (UID 0): EADDRINUSE is never bypassed because root can already bind over TIME_WAIT.
	dialCalled = false
	ok, reserved = probeLoopbackPortDarwinPrivileged(
		7890,
		false,
		func() int { return 0 },
		func(string) (net.Listener, error) { return nil, eaddrinuse },
		func(string) (net.Conn, error) {
			dialCalled = true
			return nil, econnrefused
		},
		newDummyUDP,
	)
	if dialCalled || ok || reserved {
		t.Fatalf("root EADDRINUSE case: dialCalled=%v ok=%v reserved=%v; want false, false, false", dialCalled, ok, reserved)
	}

	// 7. Permission/OS-reserved errors (WSAEACCES / EACCES) never fall back to dial and preserve reservedByOS=true.
	for _, permErr := range []error{wsaAcces, errors.New("bind: permission denied")} {
		dialCalled = false
		ok, reserved = probeLoopbackPortDarwinPrivileged(
			19090,
			false,
			func() int { return 501 },
			func(string) (net.Listener, error) { return nil, permErr },
			func(string) (net.Conn, error) {
				dialCalled = true
				return nil, econnrefused
			},
			newDummyUDP,
		)
		if dialCalled || ok || !reserved {
			t.Fatalf("reserved error %v: dialCalled=%v ok=%v reserved=%v; want false, false, true", permErr, dialCalled, ok, reserved)
		}
	}

	// 8. Real socket verification on loopback: free port is available, listening port is unavailable.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if ok, _ := ProbeLoopbackPortForPrivilegedCore(port, false); ok {
		_ = ln.Close()
		t.Fatalf("real listening port %d reported available", port)
	}
	_ = ln.Close()
	if ok, _ := ProbeLoopbackPortForPrivilegedCore(port, false); !ok {
		t.Fatalf("closed port %d reported unavailable", port)
	}
}
