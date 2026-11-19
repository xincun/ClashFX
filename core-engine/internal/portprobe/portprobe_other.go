//go:build !darwin

package portprobe

func probeLoopbackPortForPrivilegedCore(port int, checkUDP bool) (available bool, reservedByOS bool) {
	return ProbeLoopbackPort(port, checkUDP)
}
