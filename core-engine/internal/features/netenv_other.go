//go:build !windows && !darwin

package features

import "net"

func DetectActiveNetworkSignature() ActiveNetworkSignature {
	var sig ActiveNetworkSignature
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp != 0 && iface.Flags&net.FlagLoopback == 0 {
			sig.Interfaces = append(sig.Interfaces, iface.Name)
		}
	}
	return sig
}
