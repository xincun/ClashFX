//go:build !windows

package daemon

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

func DefaultEndpointName() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("clashfx-daemon-%d.sock", os.Getuid()))
}

func AcquireSingleInstanceLock(_ string) (func(), error) {
	return func() {}, nil
}

func ListenIPC(endpoint string) (net.Listener, error) {
	if strings.TrimSpace(endpoint) == "" {
		endpoint = DefaultEndpointName()
	}
	_ = os.Remove(endpoint)
	return net.Listen("unix", endpoint)
}

func DialIPC(endpoint string) (net.Conn, error) {
	if strings.TrimSpace(endpoint) == "" {
		endpoint = DefaultEndpointName()
	}
	return net.Dial("unix", endpoint)
}

func IsPrivilegedServiceInstalled(_ string) bool {
	return false
}

func ElevateAndInstallService(_ string) error {
	return fmt.Errorf("privileged Windows service installation is only supported on Windows")
}

func SetLaunchAtLogin(_ bool, _ string) error {
	return nil
}

func StartPowerResumeMonitor(_ func()) func() {
	return func() {}
}
