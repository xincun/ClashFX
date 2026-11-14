//go:build darwin

package features

import (
	"net"
	"os/exec"
	"strings"
)

func DetectActiveNetworkSignature() ActiveNetworkSignature {
	var defaultIf string
	var routeOut, orderOut, summaryOut, dnsOut string

	if b, err := exec.Command("/sbin/route", "-n", "get", "default").Output(); err == nil {
		routeOut = string(b)
		defaultIf = parseRouteInterface(routeOut)
	}
	if b, err := exec.Command("/usr/sbin/networksetup", "-listnetworkserviceorder").Output(); err == nil {
		orderOut = string(b)
	}
	if defaultIf != "" {
		if b, err := exec.Command("/usr/sbin/ipconfig", "getsummary", defaultIf).Output(); err == nil {
			summaryOut = string(b)
		}
	}
	if b, err := exec.Command("/usr/sbin/scutil", "--dns").Output(); err == nil {
		dnsOut = string(b)
	}

	sig := parseDarwinNetworkOutputs(routeOut, orderOut, summaryOut, dnsOut)
	seenIf := map[string]bool{}
	for _, existing := range sig.Interfaces {
		seenIf[strings.ToLower(existing)] = true
	}
	if ifaces, err := net.Interfaces(); err == nil {
		for _, iface := range ifaces {
			if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
				continue
			}
			lower := strings.ToLower(iface.Name)
			if strings.HasPrefix(lower, "utun") || strings.HasPrefix(lower, "awdl") || strings.HasPrefix(lower, "llw") || strings.HasPrefix(lower, "bridge") {
				continue
			}
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

	defaultIf := ""
	for _, rawLine := range strings.Split(routeOut, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(rawLine), ":")
		if !ok {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(k))
		val := strings.TrimSpace(v)
		switch key {
		case "interface":
			defaultIf = val
			addToken(&sig.Interfaces, seenIf, val)
		case "gateway":
			addToken(&sig.Gateways, seenGW, val)
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
