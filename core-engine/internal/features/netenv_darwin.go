//go:build darwin

package features

import (
	"context"
	"net"
	"os/exec"
	"strings"
	"time"
)

func runDarwinNetCmd(bin string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, bin, args...).Output()
	if err != nil {
		return ""
	}
	return string(b)
}

func isDarwinVirtualIf(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	return lower == "" ||
		lower == "lo0" ||
		strings.HasPrefix(lower, "utun") ||
		strings.HasPrefix(lower, "awdl") ||
		strings.HasPrefix(lower, "llw") ||
		strings.HasPrefix(lower, "bridge") ||
		strings.HasPrefix(lower, "ipsec") ||
		strings.HasPrefix(lower, "ppp")
}

func parseDarwinOrderDevices(orderOut string) []string {
	var devs []string
	var curSvc string
	for _, rawLine := range strings.Split(orderOut, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "(*)") {
			if strings.HasPrefix(line, "(*)") {
				curSvc = ""
			}
			continue
		}
		if strings.HasPrefix(line, "(") && !strings.HasPrefix(line, "(Hardware Port:") {
			if closeIdx := strings.IndexByte(line, ')'); closeIdx > 0 {
				curSvc = strings.TrimSpace(line[closeIdx+1:])
			}
			continue
		}
		if curSvc != "" && strings.HasPrefix(line, "(Hardware Port:") && strings.HasSuffix(line, ")") {
			inner := line[1 : len(line)-1]
			if idx := strings.LastIndex(inner, "Device:"); idx >= 0 {
				dev := strings.TrimSpace(inner[idx+len("Device:"):])
				if !isDarwinVirtualIf(dev) {
					devs = append(devs, dev)
				}
			}
			curSvc = ""
		}
	}
	return devs
}

func DetectActiveNetworkSignature() ActiveNetworkSignature {
	routeOut := runDarwinNetCmd("/sbin/route", "-n", "get", "default")
	defaultIf := parseRouteInterface(routeOut)
	orderOut := runDarwinNetCmd("/usr/sbin/networksetup", "-listnetworkserviceorder")

	// When Enhanced Mode (TUN) is active, `route -n get default` points at utunN.
	// Fall back to the highest-priority physical interface with an active ipconfig summary.
	activeIf := defaultIf
	var summaryOut string
	if !isDarwinVirtualIf(activeIf) {
		summaryOut = runDarwinNetCmd("/usr/sbin/ipconfig", "getsummary", activeIf)
	} else {
		activeIf = ""
		for _, dev := range parseDarwinOrderDevices(orderOut) {
			cand := runDarwinNetCmd("/usr/sbin/ipconfig", "getsummary", dev)
			if strings.Contains(cand, "Router :") || strings.Contains(cand, "SSID :") || strings.Contains(cand, "IPv4") {
				activeIf = dev
				summaryOut = cand
				break
			}
		}
	}
	dnsOut := runDarwinNetCmd("/usr/sbin/scutil", "--dns")

	sig := parseDarwinNetworkOutputsWithActiveIf(routeOut, orderOut, activeIf, summaryOut, dnsOut)
	seenIf := map[string]bool{}
	for _, existing := range sig.Interfaces {
		seenIf[strings.ToLower(existing)] = true
	}
	if ifaces, err := net.Interfaces(); err == nil {
		for _, iface := range ifaces {
			if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
				continue
			}
			if isDarwinVirtualIf(iface.Name) {
				continue
			}
			lower := strings.ToLower(iface.Name)
			if !seenIf[lower] {
				seenIf[lower] = true
				sig.Interfaces = append(sig.Interfaces, iface.Name)
			}
		}
	}
	return sig
}

func parseRouteInterface(routeOut string) string {
	for _, rawLine := range strings.Split(routeOut, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(rawLine), ":")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(k), "interface") {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func parseDarwinNetworkOutputs(routeOut, serviceOrderOut, summaryOut, scutilDNSOut string) ActiveNetworkSignature {
	return parseDarwinNetworkOutputsWithActiveIf(routeOut, serviceOrderOut, "", summaryOut, scutilDNSOut)
}

func parseDarwinNetworkOutputsWithActiveIf(routeOut, serviceOrderOut, overrideIf, summaryOut, scutilDNSOut string) ActiveNetworkSignature {
	var sig ActiveNetworkSignature
	seenProfile := map[string]bool{}
	seenIf := map[string]bool{}
	seenGW := map[string]bool{}
	seenDNS := map[string]bool{}

	addToken := func(dst *[]string, seen map[string]bool, val string) {
		t := strings.TrimSpace(val)
		if t == "" || t == "<redacted>" || strings.EqualFold(t, "(null)") {
			return
		}
		key := strings.ToLower(t)
		if seen[key] {
			return
		}
		seen[key] = true
		*dst = append(*dst, t)
	}

	defaultIf := strings.TrimSpace(overrideIf)
	if !isDarwinVirtualIf(defaultIf) {
		addToken(&sig.Interfaces, seenIf, defaultIf)
	}
	routeIf := ""
	for _, rawLine := range strings.Split(routeOut, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(rawLine), ":")
		if !ok {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(k))
		val := strings.TrimSpace(v)
		switch key {
		case "interface":
			routeIf = val
			if !isDarwinVirtualIf(val) {
				if defaultIf == "" {
					defaultIf = val
				}
				addToken(&sig.Interfaces, seenIf, val)
			}
		case "gateway":
			if !isDarwinVirtualIf(routeIf) {
				addToken(&sig.Gateways, seenGW, val)
			}
		}
	}

	// Map hardware service name for default interface (or all non-virtual services).
	var curSvc string
	for _, rawLine := range strings.Split(serviceOrderOut, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "(*)") {
			if strings.HasPrefix(line, "(*)") {
				curSvc = ""
			}
			continue
		}
		if strings.HasPrefix(line, "(") && !strings.HasPrefix(line, "(Hardware Port:") {
			if closeIdx := strings.IndexByte(line, ')'); closeIdx > 0 {
				curSvc = strings.TrimSpace(line[closeIdx+1:])
			}
			continue
		}
		if curSvc != "" && strings.HasPrefix(line, "(Hardware Port:") && strings.HasSuffix(line, ")") {
			dev := ""
			inner := line[1 : len(line)-1]
			if idx := strings.LastIndex(inner, "Device:"); idx >= 0 {
				dev = strings.TrimSpace(inner[idx+len("Device:"):])
			}
			if defaultIf != "" && dev == defaultIf {
				addToken(&sig.ProfileNames, seenProfile, curSvc)
			}
			curSvc = ""
		}
	}

	for _, rawLine := range strings.Split(summaryOut, "\n") {
		trimmed := strings.TrimSpace(rawLine)
		k, v, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		key := strings.TrimSpace(k)
		val := strings.TrimSpace(v)
		switch {
		case strings.EqualFold(key, "SSID"):
			addToken(&sig.ProfileNames, seenProfile, val)
		case strings.EqualFold(key, "Router"):
			addToken(&sig.Gateways, seenGW, val)
		case strings.EqualFold(key, "DomainName"):
			addToken(&sig.DNSSuffixes, seenDNS, val)
		}
	}

	for _, rawLine := range strings.Split(scutilDNSOut, "\n") {
		trimmed := strings.TrimSpace(rawLine)
		k, v, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(k))
		if strings.HasPrefix(key, "search domain[") {
			addToken(&sig.DNSSuffixes, seenDNS, v)
		}
	}

	return sig
}
