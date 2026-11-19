//go:build darwin

package sysproxy

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const darwinSysProxyCmdTimeout = 4 * time.Second

type darwinBackend struct {
	cmdDir string
}

type darwinNetworkService struct {
	Name   string
	Device string
}

func NewOSBackend() Backend {
	if strings.TrimSpace(os.Getenv("CLASHFX_DISABLE_OS_SYSPROXY")) == "1" {
		return &memoryBackend{}
	}
	if flag.Lookup("test.v") != nil &&
		strings.TrimSpace(os.Getenv("CLASHFX_DARWIN_CMD_DIR")) == "" &&
		strings.TrimSpace(os.Getenv("CLASHFX_TEST_REAL_OS_SYSPROXY")) != "1" {
		return &memoryBackend{}
	}
	return &darwinBackend{
		cmdDir: strings.TrimSpace(os.Getenv("CLASHFX_DARWIN_CMD_DIR")),
	}
}

func (b *darwinBackend) resolveCmd(name, defaultAbs string) string {
	dir := b.cmdDir
	if dir == "" {
		dir = strings.TrimSpace(os.Getenv("CLASHFX_DARWIN_CMD_DIR"))
	}
	if dir != "" {
		return filepath.Join(dir, name)
	}
	return defaultAbs
}

func (b *darwinBackend) runCmd(name, defaultAbs string, args ...string) (string, error) {
	bin := b.resolveCmd(name, defaultAbs)
	ctx, cancel := context.WithTimeout(context.Background(), darwinSysProxyCmdTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg != "" {
			return stdout.String(), fmt.Errorf("%s %v: %w (%s)", name, args, err, msg)
		}
		return stdout.String(), fmt.Errorf("%s %v: %w", name, args, err)
	}
	return stdout.String(), nil
}

func (b *darwinBackend) defaultRouteDevice() string {
	out, err := b.runCmd("route", "/sbin/route", "-n", "get", "default")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(k), "interface") {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func (b *darwinBackend) activeServices() ([]darwinNetworkService, error) {
	out, err := b.runCmd("networksetup", "/usr/sbin/networksetup", "-listnetworkserviceorder")
	if err != nil {
		return nil, err
	}
	return parseActiveNetworkServices(out, b.defaultRouteDevice()), nil
}

func parseActiveNetworkServices(orderOutput, defaultDev string) []darwinNetworkService {
	var all []darwinNetworkService
	var curName string
	hasCur := false

	for _, rawLine := range strings.Split(orderOutput, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "(*)") {
			hasCur = false
			continue
		}
		if strings.HasPrefix(line, "(") && !strings.HasPrefix(line, "(Hardware Port:") {
			if closeIdx := strings.IndexByte(line, ')'); closeIdx > 0 {
				numPart := strings.TrimSpace(line[1:closeIdx])
				if _, err := strconv.Atoi(numPart); err == nil {
					curName = strings.TrimSpace(line[closeIdx+1:])
					hasCur = curName != ""
					continue
				}
			}
		}
		if hasCur && strings.HasPrefix(line, "(Hardware Port:") && strings.HasSuffix(line, ")") {
			dev := ""
			inner := line[1 : len(line)-1]
			if idx := strings.LastIndex(inner, "Device:"); idx >= 0 {
				dev = strings.TrimSpace(inner[idx+len("Device:"):])
			}
			all = append(all, darwinNetworkService{Name: curName, Device: dev})
			hasCur = false
		}
	}

	isVirtualDev := func(dev string) bool {
		lower := strings.ToLower(strings.TrimSpace(dev))
		return lower == "" ||
			lower == "lo0" ||
			strings.HasPrefix(lower, "utun") ||
			strings.HasPrefix(lower, "awdl") ||
			strings.HasPrefix(lower, "llw") ||
			strings.HasPrefix(lower, "bridge") ||
			strings.HasPrefix(lower, "ipsec") ||
			strings.HasPrefix(lower, "ppp")
	}

	var prioritized []darwinNetworkService
	var fallback []darwinNetworkService
	for _, svc := range all {
		if svc.Device != "" && !isVirtualDev(svc.Device) && svc.Device == defaultDev {
			prioritized = append([]darwinNetworkService{svc}, prioritized...)
			continue
		}
		if !isVirtualDev(svc.Device) {
			prioritized = append(prioritized, svc)
			continue
		}
		fallback = append(fallback, svc)
	}
	if len(prioritized) > 0 {
		return prioritized
	}
	return fallback
}

func (b *darwinBackend) readServiceState(svcName string) OSProxyState {
	var st OSProxyState
	if webOut, wErr := b.runCmd("networksetup", "/usr/sbin/networksetup", "-getwebproxy", svcName); wErr == nil {
		if enabled, server, ok := parseNetworksetupProxy(webOut); ok && enabled {
			st.ProxyEnable = 1
			st.HasProxyServer = true
			st.ProxyServer = server
		}
	}
	if st.ProxyEnable == 0 {
		if secOut, sErr := b.runCmd("networksetup", "/usr/sbin/networksetup", "-getsecurewebproxy", svcName); sErr == nil {
			if enabled, server, ok := parseNetworksetupProxy(secOut); ok && enabled {
				st.ProxyEnable = 1
				st.HasProxyServer = true
				st.ProxyServer = server
			}
		}
	}
	if st.ProxyEnable == 0 {
		if socksOut, kErr := b.runCmd("networksetup", "/usr/sbin/networksetup", "-getsocksfirewallproxy", svcName); kErr == nil {
			if enabled, server, ok := parseNetworksetupProxy(socksOut); ok && enabled {
				st.ProxyEnable = 1
				st.HasProxyServer = true
				st.ProxyServer = server
			}
		}
	}

	if bypassOut, bErr := b.runCmd("networksetup", "/usr/sbin/networksetup", "-getproxybypassdomains", svcName); bErr == nil {
		if bypass := parseNetworksetupBypass(bypassOut); bypass != "" {
			st.HasProxyOverride = true
			st.ProxyOverride = bypass
		}
	}

	if pacOut, pErr := b.runCmd("networksetup", "/usr/sbin/networksetup", "-getautoproxyurl", svcName); pErr == nil {
		if pacURL, enabled := parseNetworksetupAutoProxy(pacOut); enabled && pacURL != "" {
			st.HasAutoConfigURL = true
			st.AutoConfigURL = pacURL
		}
	}
	return st
}

func (b *darwinBackend) ReadState() (OSProxyState, error) {
	services, err := b.activeServices()
	if err != nil {
		return OSProxyState{}, err
	}
	if len(services) == 0 {
		return OSProxyState{}, nil
	}
	// Inspect services in priority order; if TUN hijacked the default route so services[0]
	// is an unplugged adapter while Wi-Fi/Ethernet still has a proxy/PAC configured, return
	// the first service with an active proxy or PAC rather than missing it.
	first := b.readServiceState(services[0].Name)
	if first.ProxyEnable == 1 || first.HasAutoConfigURL {
		return first, nil
	}
	for _, svc := range services[1:] {
		cand := b.readServiceState(svc.Name)
		if cand.ProxyEnable == 1 || cand.HasAutoConfigURL {
			return cand, nil
		}
	}
	return first, nil
}

func (b *darwinBackend) WriteState(st OSProxyState) error {
	services, err := b.activeServices()
	if err != nil {
		return err
	}
	if len(services) == 0 {
		return nil
	}

	var host, port string
	if st.ProxyEnable == 1 {
		var pErr error
		host, port, pErr = parseProxyHostPort(st.ProxyServer)
		if pErr != nil {
			return pErr
		}
	}

	var firstErr error
	succeededServices := 0

	for _, svc := range services {
		var svcErr error
		recordSvcErr := func(e error) {
			if e != nil && svcErr == nil {
				svcErr = e
			}
		}

		if st.ProxyEnable == 1 {
			_, err = b.runCmd("networksetup", "/usr/sbin/networksetup", "-setwebproxy", svc.Name, host, port)
			recordSvcErr(err)
			_, err = b.runCmd("networksetup", "/usr/sbin/networksetup", "-setwebproxystate", svc.Name, "on")
			recordSvcErr(err)
			_, err = b.runCmd("networksetup", "/usr/sbin/networksetup", "-setsecurewebproxy", svc.Name, host, port)
			recordSvcErr(err)
			_, err = b.runCmd("networksetup", "/usr/sbin/networksetup", "-setsecurewebproxystate", svc.Name, "on")
			recordSvcErr(err)
			_, err = b.runCmd("networksetup", "/usr/sbin/networksetup", "-setsocksfirewallproxy", svc.Name, host, port)
			recordSvcErr(err)
			_, err = b.runCmd("networksetup", "/usr/sbin/networksetup", "-setsocksfirewallproxystate", svc.Name, "on")
			recordSvcErr(err)

			bypass := FormatDarwinBypass(st.ProxyOverride)
			args := []string{"-setproxybypassdomains", svc.Name}
			if len(bypass) == 0 {
				args = append(args, "Empty")
			} else {
				args = append(args, bypass...)
			}
			_, err = b.runCmd("networksetup", "/usr/sbin/networksetup", args...)
			recordSvcErr(err)

			if st.HasAutoConfigURL && strings.TrimSpace(st.AutoConfigURL) != "" {
				_, _ = b.runCmd("networksetup", "/usr/sbin/networksetup", "-setautoproxyurl", svc.Name, strings.TrimSpace(st.AutoConfigURL))
				_, _ = b.runCmd("networksetup", "/usr/sbin/networksetup", "-setautoproxystate", svc.Name, "on")
			} else {
				_, _ = b.runCmd("networksetup", "/usr/sbin/networksetup", "-setautoproxystate", svc.Name, "off")
			}
		} else {
			_, err = b.runCmd("networksetup", "/usr/sbin/networksetup", "-setwebproxystate", svc.Name, "off")
			recordSvcErr(err)
			_, err = b.runCmd("networksetup", "/usr/sbin/networksetup", "-setsecurewebproxystate", svc.Name, "off")
			recordSvcErr(err)
			_, err = b.runCmd("networksetup", "/usr/sbin/networksetup", "-setsocksfirewallproxystate", svc.Name, "off")
			recordSvcErr(err)

			if st.HasAutoConfigURL && strings.TrimSpace(st.AutoConfigURL) != "" {
				_, err = b.runCmd("networksetup", "/usr/sbin/networksetup", "-setautoproxyurl", svc.Name, strings.TrimSpace(st.AutoConfigURL))
				recordSvcErr(err)
				_, err = b.runCmd("networksetup", "/usr/sbin/networksetup", "-setautoproxystate", svc.Name, "on")
				recordSvcErr(err)
			} else {
				_, _ = b.runCmd("networksetup", "/usr/sbin/networksetup", "-setautoproxystate", svc.Name, "off")
			}

			if st.HasProxyOverride {
				bypass := FormatDarwinBypass(st.ProxyOverride)
				args := []string{"-setproxybypassdomains", svc.Name}
				if len(bypass) == 0 {
					args = append(args, "Empty")
				} else {
					args = append(args, bypass...)
				}
				_, _ = b.runCmd("networksetup", "/usr/sbin/networksetup", args...)
			} else {
				_, _ = b.runCmd("networksetup", "/usr/sbin/networksetup", "-setproxybypassdomains", svc.Name, "Empty")
			}
		}

		if svcErr == nil {
			succeededServices++
		} else if firstErr == nil {
			firstErr = svcErr
		}
	}
	if succeededServices > 0 {
		return nil
	}
	return firstErr
}

func parseNetworksetupProxy(out string) (enabled bool, server string, ok bool) {
	var host, portStr string
	for _, line := range strings.Split(out, "\n") {
		k, v, has := strings.Cut(line, ":")
		if !has {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(k))
		val := strings.TrimSpace(v)
		switch key {
		case "enabled":
			enabled = strings.EqualFold(val, "yes") || strings.EqualFold(val, "true") || val == "1"
		case "server":
			host = val
		case "port":
			portStr = val
		}
	}
	if !enabled || host == "" || portStr == "" || portStr == "0" {
		return false, "", false
	}
	return true, net.JoinHostPort(host, portStr), true
}

func parseNetworksetupBypass(out string) string {
	var items []string
	for _, rawLine := range strings.Split(out, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "there aren't any") || strings.HasPrefix(lower, "empty") {
			return ""
		}
		items = append(items, line)
	}
	return strings.Join(items, ",")
}

func parseNetworksetupAutoProxy(out string) (pacURL string, enabled bool) {
	for _, line := range strings.Split(out, "\n") {
		k, v, has := strings.Cut(line, ":")
		if !has {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(k))
		val := strings.TrimSpace(v)
		switch key {
		case "url":
			if val != "(null)" && val != "" {
				pacURL = val
			}
		case "enabled":
			enabled = strings.EqualFold(val, "yes") || strings.EqualFold(val, "true") || val == "1"
		}
	}
	return pacURL, enabled
}

func parseProxyHostPort(raw string) (string, string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", "", errors.New("empty proxy_server when proxy_enable=1")
	}
	for _, part := range strings.Split(s, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if idx := strings.IndexByte(part, '='); idx >= 0 {
			part = strings.TrimSpace(part[idx+1:])
		}
		host, portStr, err := net.SplitHostPort(part)
		if err != nil {
			continue
		}
		p, pErr := strconv.Atoi(portStr)
		if pErr == nil && p >= 1 && p <= 65535 {
			return host, portStr, nil
		}
	}
	return "", "", fmt.Errorf("invalid proxy_server %q", raw)
}
