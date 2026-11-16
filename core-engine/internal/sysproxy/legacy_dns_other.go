//go:build !darwin

package sysproxy

// CleanLegacyTunDNS is a macOS-only migration helper and returns nil on other platforms.
func CleanLegacyTunDNS() ([]string, error) {
	return nil, nil
}
