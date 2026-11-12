//go:build windows

package features

import (
	"net"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// DetectActiveNetworkSignature queries active non-loopback/non-TUN adapters via
// Win32 GetAdaptersAddresses and Windows NetworkList registry profiles — zero
// Location permission required and < 1ms execution time.
func DetectActiveNetworkSignature() ActiveNetworkSignature {
	var sig ActiveNetworkSignature
	seenIf := map[string]bool{}
	seenDNS := map[string]bool{}
	seenGW := map[string]bool{}

	size := uint32(16 * 1024)
	const flags = windows.GAA_FLAG_INCLUDE_GATEWAYS | windows.GAA_FLAG_SKIP_ANYCAST | windows.GAA_FLAG_SKIP_MULTICAST
	for attempt := 0; attempt < 3; attempt++ {
		buf := make([]byte, size)
		first := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, flags, 0, first, &size)
		if err == windows.ERROR_BUFFER_OVERFLOW {
			continue
		}
		if err != nil {
			break
		}
		for aa := first; aa != nil; aa = aa.Next {
			if aa.OperStatus != windows.IfOperStatusUp || aa.IfType == windows.IF_TYPE_SOFTWARE_LOOPBACK {
				continue
			}
			friendly := strings.TrimSpace(windows.UTF16PtrToString(aa.FriendlyName))
			desc := strings.ToLower(strings.TrimSpace(windows.UTF16PtrToString(aa.Description)))
			if strings.HasPrefix(strings.ToLower(friendly), "clashfx") || strings.Contains(desc, "wintun") {
				continue
			}
			if friendly != "" && !seenIf[friendly] {
				seenIf[friendly] = true
				sig.Interfaces = append(sig.Interfaces, friendly)
			}
			dns := strings.TrimSpace(windows.UTF16PtrToString(aa.DnsSuffix))
			if dns != "" && !seenDNS[dns] {
				seenDNS[dns] = true
				sig.DNSSuffixes = append(sig.DNSSuffixes, dns)
			}
			for gw := aa.FirstGatewayAddress; gw != nil; gw = gw.Next {
				if ip := sockaddrToIP(gw.Address); ip != "" && !seenGW[ip] {
					seenGW[ip] = true
					sig.Gateways = append(sig.Gateways, ip)
				}
			}
		}
		break
	}

	// Read known Windows Network Profile Names from NetworkList\Profiles (readable by ordinary users)
	if len(sig.Gateways) > 0 || len(sig.Interfaces) > 0 {
		if names := readRecentNetworkProfileNames(); len(names) > 0 {
			sig.ProfileNames = names
		}
	}
	return sig
}

func sockaddrToIP(sa windows.SocketAddress) string {
	if sa.Sockaddr == nil {
		return ""
	}
	addr, err := sa.Sockaddr.Sockaddr()
	if err != nil {
		return ""
	}
	switch v := addr.(type) {
	case *syscall.SockaddrInet4:
		return net.IP(v.Addr[:]).String()
	case *syscall.SockaddrInet6:
		return net.IP(v.Addr[:]).String()
	default:
		return ""
	}
}

func readRecentNetworkProfileNames() []string {
	const profilesKey = `SOFTWARE\Microsoft\Windows NT\CurrentVersion\NetworkList\Profiles`
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, profilesKey, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return nil
	}
	defer k.Close()
	subs, err := k.ReadSubKeyNames(32)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, sub := range subs {
		sk, err := registry.OpenKey(k, sub, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		name, _, err := sk.GetStringValue("ProfileName")
		_ = sk.Close()
		name = strings.TrimSpace(name)
		if err == nil && name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}
