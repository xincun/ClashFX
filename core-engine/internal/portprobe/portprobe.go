package portprobe

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"
)

const wsaEAcces syscall.Errno = 10013

// IsWindowsReservedPortError reports whether bind failed due to OS-excluded port
// ranges (e.g., Windows WinNAT / Hyper-V / WSL2 dynamic excludedportrange,
// which returns WSAEACCES 10013 even when no socket appears in netstat).
func IsWindowsReservedPortError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, wsaEAcces) || errors.Is(err, os.ErrPermission) {
		return true
	}
	var errno syscall.Errno
	if errors.As(err, &errno) && errno == wsaEAcces {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "forbidden by its access permissions") ||
		strings.Contains(msg, "permission denied")
}

// ProbeLoopbackPort checks TCP (and optionally UDP) availability on 127.0.0.1:port.
func ProbeLoopbackPort(port int, checkUDP bool) (available bool, reservedByOS bool) {
	if port < 1 || port > 65535 {
		return false, false
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	tcpLn, err := net.Listen("tcp", addr)
	if err != nil {
		return false, IsWindowsReservedPortError(err)
	}
	_ = tcpLn.Close()

	if checkUDP {
		udpConn, err := net.ListenPacket("udp", addr)
		if err != nil {
			return false, IsWindowsReservedPortError(err)
		}
		_ = udpConn.Close()
	}
	return true, false
}

// ProbeLoopbackPortForPrivilegedCore checks TCP (and optionally UDP) availability
// on 127.0.0.1:port when the listening process will run under a privileged service
// account (e.g. root via macOS ClashFXService LaunchDaemon).
//
// On macOS, an unprivileged daemon (UID != 0) receives a false EADDRINUSE from
// XNU in_pcbbind_setup when probing a port that only has stale root-owned
// TIME_WAIT / FIN_WAIT_2 sockets left by a recently stopped privileged core, even
// though the root core itself can immediately rebind with SO_REUSEADDR. On other
// platforms (including Windows Hyper-V/WinNAT WSAEACCES detection), this behaves
// identically to ProbeLoopbackPort.
func ProbeLoopbackPortForPrivilegedCore(port int, checkUDP bool) (available bool, reservedByOS bool) {
	return probeLoopbackPortForPrivilegedCore(port, checkUDP)
}

// ProbeDualBindPort checks both wildcard (:port) and loopback (127.0.0.1:port).
func ProbeDualBindPort(port int, checkUDP bool) (available bool, reservedByOS bool) {
	if port < 1 || port > 65535 {
		return false, false
	}
	wildLn, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return false, IsWindowsReservedPortError(err)
	}
	_ = wildLn.Close()
	return ProbeLoopbackPort(port, checkUDP)
}

func FindAvailablePort(first, last int, reserved map[int]bool, checkUDP bool, probe func(int, bool) (bool, bool)) (int, error) {
	if probe == nil {
		probe = ProbeLoopbackPort
	}
	if first < 1 {
		first = 1
	}
	if last > 65535 {
		last = 65535
	}
	for port := first; port <= last; port++ {
		if reserved != nil && reserved[port] {
			continue
		}
		if ok, _ := probe(port, checkUDP); ok {
			return port, nil
		}
	}
	return 0, fmt.Errorf("no free local port in range %d-%d", first, last)
}

func FindAvailablePortReverse(first, last int, reserved map[int]bool, checkUDP bool, probe func(int, bool) (bool, bool)) (int, error) {
	if probe == nil {
		probe = ProbeLoopbackPort
	}
	if first < 1 {
		first = 1
	}
	if last > 65535 {
		last = 65535
	}
	for port := last; port >= first; port-- {
		if reserved != nil && reserved[port] {
			continue
		}
		if ok, _ := probe(port, checkUDP); ok {
			return port, nil
		}
	}
	return 0, fmt.Errorf("no free local port in range %d-%d", first, last)
}
