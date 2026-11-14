//go:build darwin

package sysproxy

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

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
	cmd := exec.Command(bin, args...)
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
		return lower == "lo0" ||
			strings.HasPrefix(lower, "utun") ||
			strings.HasPrefix(lower, "awdl") ||
			strings.HasPrefix(lower, "llw") ||
			strings.HasPrefix(lower, "bridge")
	}

	var prioritized []darwinNetworkService
	var fallback []darwinNetworkService
	for _, svc := range all {
		if svc.Device != "" && svc.Device == defaultDev {
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

func (b *darwinBackend) ReadState() (OSProxyState, error) {
	services, err := b.activeServices()
	if err != nil {
		return OSProxyState{}, err
	}
	if len(services) == 0 {
		return OSProxyState{}, nil
	}
	primary := services[0].Name

	var st OSProxyState
	if webOut, wErr := b.runCmd("networksetup", "/usr/sbin/networksetup", "-getwebproxy", primary); wErr == nil {
		if enabled, server, ok := parseNetworksetupProxy(webOut); ok && enabled {
			st.ProxyEnable = 1
			st.HasProxyServer = true
			st.ProxyServer = server
		}
	}
	if st.ProxyEnable == 0 {
		if secOut, sErr := b.runCmd("networksetup", "/usr/sbin/networksetup", "-getsecurewebproxy", primary); sErr == nil {
			if enabled, server, ok := parseNetworksetupProxy(secOut); ok && enabled {
				st.ProxyEnable = 1
				st.HasProxyServer = true
				st.ProxyServer = server
			}
		}
	}
	if st.ProxyEnable == 0 {
		if socksOut, kErr := b.runCmd("networksetup", "/usr/sbin/networksetup", "-getsocksfirewallproxy", primary); kErr == nil {
			if enabled, server, ok := parseNetworksetupProxy(socksOut); ok && enabled {
				st.ProxyEnable = 1
				st.HasProxyServer = true
				st.ProxyServer = server
			}
		}
	}

	if bypassOut, bErr := b.runCmd("networksetup", "/usr/sbin/networksetup", "-getproxybypassdomains", primary); bErr == nil {
		if bypass := parseNetworksetupBypass(bypassOut); bypass != "" {
			st.HasProxyOverride = true
			st.ProxyOverride = bypass
		}
	}

	if pacOut, pErr := b.runCmd("networksetup", "/usr/sbin/networksetup", "-getautoproxyurl", primary); pErr == nil {
		if pacURL, enabled := parseNetworksetupAutoProxy(pacOut); enabled && pacURL != "" {
			st.HasAutoConfigURL = true
			st.AutoConfigURL = pacURL
		}
	}
	return st, nil
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
	recordErr := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	for _, svc := range services {
		if st.ProxyEnable == 1 {
			_, err = b.runCmd("networksetup", "/usr/sbin/networksetup", "-setwebproxy", svc.Name, host, port)
			recordErr(err)
			_, err = b.runCmd("networksetup", "/usr/sbin/networksetup", "-setwebproxystate", svc.Name, "on")
			recordErr(err)
			_, err = b.runCmd("networksetup", "/usr/sbin/networksetup", "-setsecurewebproxy", svc.Name, host, port)
			recordErr(err)
			_, err = b.runCmd("networksetup", "/usr/sbin/networksetup", "-setsecurewebproxystate", svc.Name, "on")
			recordErr(err)
			_, err = b.runCmd("networksetup", "/usr/sbin/networksetup", "-setsocksfirewallproxy", svc.Name, host, port)
			recordErr(err)
			_, err = b.runCmd("networksetup", "/usr/sbin/networksetup", "-setsocksfirewallproxystate", svc.Name, "on")
			recordErr(err)

			bypass := FormatDarwinBypass(st.ProxyOverride)
			args := []string{"-setproxybypassdomains", svc.Name}
			if len(bypass) == 0 {
				args = append(args, "Empty")
			} else {
				args = append(args, bypass...)
			}
			_, err = b.runCmd("networksetup", "/usr/sbin/networksetup", args...)
			recordErr(err)

			if !st.HasAutoConfigURL {
				_, _ = b.runCmd("networksetup", "/usr/sbin/networksetup", "-setautoproxystate", svc.Name, "off")
			}
			continue
		}

		_, err = b.runCmd("networksetup", "/usr/sbin/networksetup", "-setwebproxystate", svc.Name, "off")
		recordErr(err)
		_, err = b.runCmd("networksetup", "/usr/sbin/networksetup", "-setsecurewebproxystate", svc.Name, "off")
		recordErr(err)
		_, err = b.runCmd("networksetup", "/usr/sbin/networksetup", "-setsocksfirewallproxystate", svc.Name, "off")
		recordErr(err)

		if st.HasAutoConfigURL && strings.TrimSpace(st.AutoConfigURL) != "" {
			_, err = b.runCmd("networksetup", "/usr/sbin/networksetup", "-setautoproxyurl", svc.Name, strings.TrimSpace(st.AutoConfigURL))
			recordErr(err)
			_, err = b.runCmd("networksetup", "/usr/sbin/networksetup", "-setautoproxystate", svc.Name, "on")
			recordErr(err)
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
	if host == "" || portStr == "" || portStr == "0" {
		return enabled, "", enabled
	}
	return enabled, net.JoinHostPort(host, portStr), true
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
