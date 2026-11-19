//go:build windows

package features

import (
	"bytes"
	"encoding/binary"
	"net"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	modiphlpapi        = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetIpNetTable  = modiphlpapi.NewProc("GetIpNetTable")
	modwlanapi         = windows.NewLazySystemDLL("wlanapi.dll")
	procWlanOpenHandle = modwlanapi.NewProc("WlanOpenHandle")
	procWlanClose      = modwlanapi.NewProc("WlanCloseHandle")
	procWlanEnumIfs    = modwlanapi.NewProc("WlanEnumInterfaces")
	procWlanQueryIf    = modwlanapi.NewProc("WlanQueryInterface")
	procWlanFreeMem    = modwlanapi.NewProc("WlanFreeMemory")
)

// DetectActiveNetworkSignature queries ONLY currently connected non-loopback/non-TUN
// adapters via Win32 GetAdaptersAddresses, active WLAN connection attributes, and
// active gateway ARP MAC -> NetworkList\Signatures -> Profiles mapping.
// Historical disconnected network profiles are never returned.
func DetectActiveNetworkSignature() ActiveNetworkSignature {
	var sig ActiveNetworkSignature
	seenProfile := map[string]bool{}
	seenIf := map[string]bool{}
	seenDNS := map[string]bool{}
	seenGW := map[string]bool{}
	activeGWv4 := map[uint32]bool{}

	addProfile := func(name string) {
		name = strings.TrimSpace(name)
		if name != "" && !seenProfile[strings.ToLower(name)] {
			seenProfile[strings.ToLower(name)] = true
			sig.ProfileNames = append(sig.ProfileNames, name)
		}
	}

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
				ipStr, rawV4 := sockaddrToIP(gw.Address)
				if ipStr != "" && !seenGW[ipStr] {
					seenGW[ipStr] = true
					sig.Gateways = append(sig.Gateways, ipStr)
				}
				if rawV4 != 0 {
					activeGWv4[rawV4] = true
				}
			}
		}
		break
	}

	// 1. Query currently connected Wi-Fi SSID / profile name if Wireless LAN Service is active
	for _, ssid := range queryConnectedWlanSSIDs() {
		addProfile(ssid)
	}

	// 2. Map active gateway MACs from kernel ARP cache to NetworkList\Signatures -> ProfileGuid -> ProfileName
	if len(activeGWv4) > 0 {
		if macs := lookupGatewayMACsFromARPTable(activeGWv4); len(macs) > 0 {
			for _, name := range matchActiveNetworkProfilesByGatewayMAC(macs) {
				addProfile(name)
			}
		}
	}

	return sig
}

func sockaddrToIP(sa windows.SocketAddress) (string, uint32) {
	if sa.Sockaddr == nil {
		return "", 0
	}
	addr, err := sa.Sockaddr.Sockaddr()
	if err != nil {
		return "", 0
	}
	switch v := addr.(type) {
	case *syscall.SockaddrInet4:
		ip := net.IP(v.Addr[:])
		raw := binary.LittleEndian.Uint32(v.Addr[:])
		return ip.String(), raw
	case *syscall.SockaddrInet6:
		return net.IP(v.Addr[:]).String(), 0
	default:
		return "", 0
	}
}

type mibIpNetRow struct {
	Index       uint32
	PhysAddrLen uint32
	PhysAddr    [8]byte
	Addr        uint32
	Type        uint32
}

func lookupGatewayMACsFromARPTable(gatewayIPs map[uint32]bool) [][]byte {
	if procGetIpNetTable.Find() != nil {
		return nil
	}
	var size uint32 = 8 * 1024
	for attempt := 0; attempt < 3; attempt++ {
		buf := make([]byte, size)
		r1, _, _ := procGetIpNetTable.Call(
			uintptr(unsafe.Pointer(&buf[0])),
			uintptr(unsafe.Pointer(&size)),
			0,
		)
		if r1 == uintptr(windows.ERROR_INSUFFICIENT_BUFFER) {
			continue
		}
		if r1 != 0 || len(buf) < 4 {
			return nil
		}
		count := binary.LittleEndian.Uint32(buf[:4])
		const rowSize = int(unsafe.Sizeof(mibIpNetRow{}))
		maxRows := (len(buf) - 4) / rowSize
		if int(count) > maxRows {
			count = uint32(maxRows)
		}
		rows := unsafe.Slice((*mibIpNetRow)(unsafe.Pointer(&buf[4])), count)
		var out [][]byte
		for i := range rows {
			row := &rows[i]
			// Type 2 = MIB_IPNET_TYPE_INVALID
			if row.Type == 2 || row.PhysAddrLen != 6 || !gatewayIPs[row.Addr] {
				continue
			}
			mac := make([]byte, 6)
			copy(mac, row.PhysAddr[:6])
			dup := false
			for _, existing := range out {
				if bytes.Equal(existing, mac) {
					dup = true
					break
				}
			}
			if !dup {
				out = append(out, mac)
			}
		}
		return out
	}
	return nil
}

func matchActiveNetworkProfilesByGatewayMAC(activeMACs [][]byte) []string {
	const baseKey = `SOFTWARE\Microsoft\Windows NT\CurrentVersion\NetworkList`
	profileGUIDs := map[string]bool{}

	for _, subTree := range []string{baseKey + `\Signatures\Unmanaged`, baseKey + `\Signatures\Managed`} {
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, subTree, registry.ENUMERATE_SUB_KEYS)
		if err != nil {
			continue
		}
		subs, err := k.ReadSubKeyNames(-1)
		if err == nil {
			for _, sub := range subs {
				sk, oErr := registry.OpenKey(k, sub, registry.QUERY_VALUE)
				if oErr != nil {
					continue
				}
				gwMAC, _, mErr := sk.GetBinaryValue("DefaultGatewayMac")
				guid, _, gErr := sk.GetStringValue("ProfileGuid")
				_ = sk.Close()
				if mErr != nil || gErr != nil || len(gwMAC) != 6 || strings.TrimSpace(guid) == "" {
					continue
				}
				for _, want := range activeMACs {
					if bytes.Equal(gwMAC, want) {
						profileGUIDs[strings.TrimSpace(guid)] = true
						break
					}
				}
			}
		}
		_ = k.Close()
	}

	if len(profileGUIDs) == 0 {
		return nil
	}

	var names []string
	for guid := range profileGUIDs {
		pk, err := registry.OpenKey(registry.LOCAL_MACHINE, baseKey+`\Profiles\`+guid, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		name, _, nErr := pk.GetStringValue("ProfileName")
		_ = pk.Close()
		if nErr == nil && strings.TrimSpace(name) != "" {
			names = append(names, strings.TrimSpace(name))
		}
	}
	return names
}

type wlanInterfaceInfo struct {
	InterfaceGuid           windows.GUID
	StrInterfaceDescription [256]uint16
	IsState                 uint32
}

func queryConnectedWlanSSIDs() []string {
	if modwlanapi.Load() != nil || procWlanOpenHandle.Find() != nil || procWlanEnumIfs.Find() != nil || procWlanQueryIf.Find() != nil {
		return nil
	}
	var negVer uint32
	var hClient uintptr
	if r1, _, _ := procWlanOpenHandle.Call(2, 0, uintptr(unsafe.Pointer(&negVer)), uintptr(unsafe.Pointer(&hClient))); r1 != 0 || hClient == 0 {
		return nil
	}
	defer procWlanClose.Call(hClient, 0)

	var pIfList uintptr
	if r1, _, _ := procWlanEnumIfs.Call(hClient, 0, uintptr(unsafe.Pointer(&pIfList))); r1 != 0 || pIfList == 0 {
		return nil
	}
	defer procWlanFreeMem.Call(pIfList)

	numItems := *(*uint32)(unsafe.Pointer(pIfList))
	if numItems == 0 || numItems > 32 {
		return nil
	}
	ifs := unsafe.Slice((*wlanInterfaceInfo)(unsafe.Pointer(pIfList+8)), numItems)

	const (
		wlanInterfaceStateConnected       = 1
		wlanIntfOpcodeCurrentConnection   = 7
		wlanConnAttrSSIDLengthByteOffset  = 520
		wlanConnAttrSSIDBytesOffset       = 524
		wlanConnAttrMinBytesForAssocSSID  = 556
	)

	var out []string
	for i := range ifs {
		if ifs[i].IsState != wlanInterfaceStateConnected {
			continue
		}
		var dataSize uint32
		var pData uintptr
		var opcodeType uint32
		r1, _, _ := procWlanQueryIf.Call(
			hClient,
			uintptr(unsafe.Pointer(&ifs[i].InterfaceGuid)),
			wlanIntfOpcodeCurrentConnection,
			0,
			uintptr(unsafe.Pointer(&dataSize)),
			uintptr(unsafe.Pointer(&pData)),
			uintptr(unsafe.Pointer(&opcodeType)),
		)
		if r1 != 0 || pData == 0 {
			continue
		}
		if dataSize >= wlanConnAttrMinBytesForAssocSSID {
			raw := unsafe.Slice((*byte)(unsafe.Pointer(pData)), dataSize)
			ssidLen := binary.LittleEndian.Uint32(raw[wlanConnAttrSSIDLengthByteOffset : wlanConnAttrSSIDLengthByteOffset+4])
			if ssidLen > 0 && ssidLen <= 32 {
				ssid := strings.TrimSpace(string(raw[wlanConnAttrSSIDBytesOffset : wlanConnAttrSSIDBytesOffset+ssidLen]))
				if ssid != "" {
					out = append(out, ssid)
				}
			} else {
				profUTF16 := unsafe.Slice((*uint16)(unsafe.Pointer(pData+8)), 256)
				if prof := strings.TrimSpace(windows.UTF16ToString(profUTF16)); prof != "" {
					out = append(out, prof)
				}
			}
		}
		procWlanFreeMem.Call(pData)
	}
	return out
}
