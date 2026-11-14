//go:build darwin

package features

import (
	"reflect"
	"testing"
)

func TestParseDarwinNetworkOutputs(t *testing.T) {
	routeOut := `
   route to: default
destination: default
    gateway: 192.168.2.1
  interface: en0
`
	orderOut := `
An asterisk (*) denotes that a network service is disabled.
(1) AX88179A
(Hardware Port: AX88179A, Device: en7)

(*) Disabled USB
(Hardware Port: Disabled USB, Device: en8)

(3) Wi-Fi
(Hardware Port: Wi-Fi, Device: en0)
`
	summaryOut := `
  InterfaceType : WiFi
  Router : 192.168.2.1
  SSID : Office-5G
  DomainName : corp.local
`
	scutilDNSOut := `
resolver #1
  search domain[0] : corp.local
  search domain[1] : campus.edu
  nameserver[0] : 192.168.2.1
`
	sig := parseDarwinNetworkOutputs(routeOut, orderOut, summaryOut, scutilDNSOut)
	if !reflect.DeepEqual(sig.Interfaces, []string{"en0"}) {
		t.Fatalf("Interfaces = %v, want [en0]", sig.Interfaces)
	}
	if !reflect.DeepEqual(sig.Gateways, []string{"192.168.2.1"}) {
		t.Fatalf("Gateways = %v, want [192.168.2.1]", sig.Gateways)
	}
	if !reflect.DeepEqual(sig.ProfileNames, []string{"Wi-Fi", "Office-5G"}) {
		t.Fatalf("ProfileNames = %v, want [Wi-Fi Office-5G]", sig.ProfileNames)
	}
	if !reflect.DeepEqual(sig.DNSSuffixes, []string{"corp.local", "campus.edu"}) {
		t.Fatalf("DNSSuffixes = %v, want [corp.local campus.edu]", sig.DNSSuffixes)
	}
	if rule, ok := MatchSuspendNetwork(sig, []string{"Home-WiFi", "Office-5G"}); !ok || rule != "Office-5G" {
		t.Fatalf("MatchSuspendNetwork(Office-5G) = (%q, %v), want (Office-5G, true)", rule, ok)
	}
	// Redacted SSID must be ignored rather than treated as an SSID rule.
	redactedSig := parseDarwinNetworkOutputs(routeOut, orderOut, "  SSID : <redacted>\n", "")
	for _, p := range redactedSig.ProfileNames {
		if p == "<redacted>" {
			t.Fatalf("redacted SSID leaked into ProfileNames: %v", redactedSig.ProfileNames)
		}
	}
}
