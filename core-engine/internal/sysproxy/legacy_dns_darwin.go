//go:build darwin

package sysproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	LegacyDarwinTunDNS            = "198.18.0.2"
	darwinSystemConfigPlistPath   = "/Library/Preferences/SystemConfiguration/preferences.plist"
	darwinDNSCommandTimeout       = 4 * time.Second
)

type darwinServiceDNS struct {
	Name    string
	Servers []string
}

// CleanLegacyTunDNS removes stale 198.18.0.2 static DNS entries left behind by
// legacy ClashFX Helper versions across all macOS network services (including
// hidden Ethernet/Thunderbolt adapters in preferences.plist).
func CleanLegacyTunDNS() ([]string, error) {
	if strings.TrimSpace(os.Getenv("CLASHFX_DISABLE_OS_SYSPROXY")) == "1" {
		return nil, nil
	}
	cmdDir := strings.TrimSpace(os.Getenv("CLASHFX_DARWIN_CMD_DIR"))
	if flag.Lookup("test.v") != nil &&
		cmdDir == "" &&
		strings.TrimSpace(os.Getenv("CLASHFX_TEST_REAL_OS_SYSPROXY")) != "1" {
		return nil, nil
	}
	plistPath := darwinSystemConfigPlistPath
	if cmdDir != "" {
		plistPath = strings.TrimSpace(os.Getenv("CLASHFX_DARWIN_PREFS_PLIST"))
	}
	return cleanLegacyTunDNS(plistPath, cmdDir)
}

func resolveLegacyDNSCmd(cmdDir, name, defaultAbs string) string {
	if cmdDir != "" {
		candidate := filepath.Join(cmdDir, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		if name != "plutil" {
			return candidate
		}
	}
	return defaultAbs
}

func runTimedCmd(bin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), darwinDNSCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin = nil
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg != "" {
			return stdout.String(), fmt.Errorf("%s %v: %w (%s)", filepath.Base(bin), args, err, msg)
		}
		return stdout.String(), fmt.Errorf("%s %v: %w", filepath.Base(bin), args, err)
	}
	return stdout.String(), nil
}

func discoverServicesFromPlist(plistPath, cmdDir string) ([]darwinServiceDNS, error) {
	if strings.TrimSpace(plistPath) == "" {
		return nil, os.ErrNotExist
	}
	if _, err := os.Stat(plistPath); err != nil {
		return nil, err
	}
	plutilBin := resolveLegacyDNSCmd(cmdDir, "plutil", "/usr/bin/plutil")
	out, err := runTimedCmd(plutilBin, "-convert", "json", "-o", "-", plistPath)
	if err != nil {
		return nil, err
	}
	var doc struct {
		NetworkServices map[string]struct {
			UserDefinedName string `json:"UserDefinedName"`
			DNS             struct {
				ServerAddresses []string `json:"ServerAddresses"`
			} `json:"DNS"`
		} `json:"NetworkServices"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		return nil, err
	}

	keys := make([]string, 0, len(doc.NetworkServices))
	for k := range doc.NetworkServices {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	services := make([]darwinServiceDNS, 0, len(keys))
	for _, k := range keys {
		entry := doc.NetworkServices[k]
		name := strings.TrimSpace(entry.UserDefinedName)
		if name == "" || len(entry.DNS.ServerAddresses) == 0 {
			continue
		}
		services = append(services, darwinServiceDNS{
			Name:    name,
			Servers: entry.DNS.ServerAddresses,
		})
	}
	return services, nil
}

func discoverServicesFromNetworksetup(cmdDir string) ([]darwinServiceDNS, error) {
	nsBin := resolveLegacyDNSCmd(cmdDir, "networksetup", "/usr/sbin/networksetup")
	listOut, err := runTimedCmd(nsBin, "-listallnetworkservices")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, rawLine := range strings.Split(listOut, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "An asterisk (*)") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "*"))
		if line != "" {
			names = append(names, line)
		}
	}
	services := make([]darwinServiceDNS, 0, len(names))
	for _, name := range names {
		dnsOut, getErr := runTimedCmd(nsBin, "-getdnsservers", name)
		if getErr != nil {
			continue
		}
		var servers []string
		for _, raw := range strings.Split(dnsOut, "\n") {
			s := strings.TrimSpace(raw)
			if s == "" || strings.HasPrefix(strings.ToLower(s), "there aren't any") {
				continue
			}
			servers = append(servers, s)
		}
		if len(servers) > 0 {
			services = append(services, darwinServiceDNS{
				Name:    name,
				Servers: servers,
			})
		}
	}
	return services, nil
}

func stripLegacyTunDNS(servers []string) (remaining []string, changed bool) {
	remaining = make([]string, 0, len(servers))
	for _, raw := range servers {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		if s == LegacyDarwinTunDNS {
			changed = true
			continue
		}
		remaining = append(remaining, s)
	}
	return remaining, changed
}

func cleanLegacyTunDNS(plistPath, cmdDir string) ([]string, error) {
	services, err := discoverServicesFromPlist(plistPath, cmdDir)
	if err != nil {
		var fbErr error
		services, fbErr = discoverServicesFromNetworksetup(cmdDir)
		if fbErr != nil {
			return nil, fbErr
		}
	}

	nsBin := resolveLegacyDNSCmd(cmdDir, "networksetup", "/usr/sbin/networksetup")
	seen := make(map[string]bool, len(services))
	var cleaned []string
	var firstErr error

	for _, svc := range services {
		if seen[svc.Name] {
			continue
		}
		remaining, changed := stripLegacyTunDNS(svc.Servers)
		if !changed {
			continue
		}
		seen[svc.Name] = true
		args := []string{"-setdnsservers", svc.Name}
		if len(remaining) == 0 {
			args = append(args, "Empty")
		} else {
			args = append(args, remaining...)
		}
		if _, setErr := runTimedCmd(nsBin, args...); setErr != nil {
			if firstErr == nil {
				firstErr = setErr
			}
			continue
		}
		cleaned = append(cleaned, svc.Name)
	}

	if len(cleaned) > 0 {
		dscache := resolveLegacyDNSCmd(cmdDir, "dscacheutil", "/usr/bin/dscacheutil")
		killall := resolveLegacyDNSCmd(cmdDir, "killall", "/usr/bin/killall")
		_, _ = runTimedCmd(dscache, "-flushcache")
		_, _ = runTimedCmd(killall, "-HUP", "mDNSResponder")
	}

	return cleaned, firstErr
}
