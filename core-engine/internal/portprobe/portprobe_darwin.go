//go:build darwin

package portprobe

import (
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"time"
)

const darwinLoopbackDialTimeout = 150 * time.Millisecond

func probeLoopbackPortForPrivilegedCore(port int, checkUDP bool) (available bool, reservedByOS bool) {
	return probeLoopbackPortDarwinPrivileged(
		port,
		checkUDP,
		os.Geteuid,
		func(addr string) (net.Listener, error) {
			return net.Listen("tcp", addr)
		},
		func(addr string) (net.Conn, error) {
			return net.DialTimeout("tcp4", addr, darwinLoopbackDialTimeout)
		},
		func(addr string) (net.PacketConn, error) {
			return net.ListenPacket("udp", addr)
		},
	)
}

func probeLoopbackPortDarwinPrivileged(
	port int,
	checkUDP bool,
	geteuid func() int,
	listenTCP func(addr string) (net.Listener, error),
	dialTCP func(addr string) (net.Conn, error),
	listenUDP func(addr string) (net.PacketConn, error),
) (available bool, reservedByOS bool) {
	if port < 1 || port > 65535 {
		return false, false
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	tcpLn, err := listenTCP(addr)
	if err == nil {
		_ = tcpLn.Close()
	} else {
		if IsWindowsReservedPortError(err) {
			return false, true
		}
		// Only disambiguate EADDRINUSE when an unprivileged caller probes a port
		// for a root-owned core. If no active TCP listener responds on loopback
		// (ECONNREFUSED), the EADDRINUSE stems from stale cross-UID TIME_WAIT /
		// FIN_WAIT_2 sockets that root mihomo_core can immediately rebind.
		if geteuid() == 0 || !errors.Is(err, syscall.EADDRINUSE) {
			return false, false
		}
		conn, dialErr := dialTCP(addr)
		if dialErr == nil {
			_ = conn.Close()
			return false, false
		}
		if !errors.Is(dialErr, syscall.ECONNREFUSED) {
			return false, false
		}
	}

	if checkUDP {
		udpConn, err := listenUDP(addr)
		if err != nil {
			return false, IsWindowsReservedPortError(err)
		}
		_ = udpConn.Close()
	}
	return true, false
}
