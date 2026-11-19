//go:build windows

package daemon

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/user"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"github.com/clashfx/core-engine/internal/service"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc/mgr"
)

const runRegistryKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`
const runRegistryValueName = "ClashFX"

type deviceNotifySubscribeParams struct {
	callback uintptr
	context  uintptr
}

var (
	modshell32                                 = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteExW                        = modshell32.NewProc("ShellExecuteExW")
	modpowrprof                                = windows.NewLazySystemDLL("powrprof.dll")
	procPowerRegisterSuspendResumeNotification = modpowrprof.NewProc("PowerRegisterSuspendResumeNotification")

	powerNotifyMu   sync.Mutex
	activePowerSubs = map[uintptr]*deviceNotifySubscribeParams{}
)

func DefaultEndpointName() string {
	short := sanitizedUsername()
	return `\\.\pipe\ClashFXDaemon-` + short
}

func sanitizedUsername() string {
	u, err := user.Current()
	raw := "default"
	if err == nil && u != nil && u.Username != "" {
		raw = u.Username
		if idx := strings.LastIndex(raw, `\`); idx >= 0 {
			raw = raw[idx+1:]
		}
	}
	var b strings.Builder
	for _, r := range raw {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "default"
	}
	return b.String()
}

func mutexObjectSuffix(customKey string) string {
	trimmed := strings.TrimSpace(customKey)
	if trimmed == "" {
		return sanitizedUsername()
	}
	// Win32 mutex names treat backslashes after "Local\" as object namespace separators.
	sum := sha256.Sum256([]byte(strings.ToLower(trimmed)))
	return fmt.Sprintf("custom-%x", sum[:8])
}

func AcquireSingleInstanceLock(customKey string) (func(), error) {
	suffix := mutexObjectSuffix(customKey)
	namePtr, err := windows.UTF16PtrFromString(`Local\ClashFXDaemonMutex-` + suffix)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateMutex(nil, true, namePtr)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		if h != 0 {
			_ = windows.CloseHandle(h)
		}
		return nil, fmt.Errorf("clashfx-daemon is already running for %s", suffix)
	}
	if err != nil {
		return nil, fmt.Errorf("create daemon mutex: %w", err)
	}
	return func() {
		_ = windows.ReleaseMutex(h)
		_ = windows.CloseHandle(h)
	}, nil
}

func ListenIPC(endpoint string) (net.Listener, error) {
	if strings.TrimSpace(endpoint) == "" {
		endpoint = DefaultEndpointName()
	}
	if !strings.HasPrefix(endpoint, `\\.\pipe\`) {
		endpoint = `\\.\pipe\` + endpoint
	}
	sddl := buildCurrentUserPipeSDDL()
	cfg := &winio.PipeConfig{
		SecurityDescriptor: sddl,
		InputBufferSize:    128 * 1024,
		OutputBufferSize:   256 * 1024,
	}
	return winio.ListenPipe(endpoint, cfg)
}

func DialIPC(endpoint string) (net.Conn, error) {
	if strings.TrimSpace(endpoint) == "" {
		endpoint = DefaultEndpointName()
	}
	if !strings.HasPrefix(endpoint, `\\.\pipe\`) {
		endpoint = `\\.\pipe\` + endpoint
	}
	return winio.DialPipe(endpoint, nil)
}

func buildCurrentUserPipeSDDL() string {
	tok := windows.GetCurrentProcessToken()
	tu, err := tok.GetTokenUser()
	if err == nil && tu != nil && tu.User.Sid != nil {
		sidStr := tu.User.Sid.String()
		if strings.HasPrefix(sidStr, "S-1-") {
			return fmt.Sprintf("D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GA;;;%s)", sidStr)
		}
	}
	return "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;AU)"
}

// IsPrivilegedServiceRegistered reports whether the Windows SCM service exists
// (non-blocking; does not dial IPC).
func IsPrivilegedServiceRegistered(serviceName string) bool {
	if strings.TrimSpace(os.Getenv("CLASHFX_DISABLE_PRIVILEGED_SERVICE")) == "1" {
		return false
	}
	if flag.Lookup("test.v") != nil && strings.TrimSpace(os.Getenv("CLASHFX_TEST_REAL_SERVICE")) != "1" {
		return false
	}
	registered, _ := queryServiceRegistrationAndRunning(serviceName)
	return registered
}

func IsPrivilegedServiceInstalled(serviceName string) bool {
	if strings.TrimSpace(os.Getenv("CLASHFX_DISABLE_PRIVILEGED_SERVICE")) == "1" {
		return false
	}
	if flag.Lookup("test.v") != nil && strings.TrimSpace(os.Getenv("CLASHFX_TEST_REAL_SERVICE")) != "1" {
		return false
	}
	registered, running := queryServiceRegistrationAndRunning(serviceName)
	if !registered {
		return false
	}
	// If SCM reports Running or StartPending, do not falsely treat a transient busy IPC
	// (e.g. Runner.mu held during start_core/upgrade_core) as "service uninstalled".
	if running {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	resp, callErr := service.Call(ctx, service.Request{Action: "status"})
	return callErr == nil && resp.OK
}

func queryServiceRegistrationAndRunning(serviceName string) (registered bool, running bool) {
	m, err := mgr.Connect()
	if err == nil {
		defer m.Disconnect()
		s, openErr := m.OpenService(serviceName)
		if openErr != nil {
			return false, false
		}
		defer s.Close()
		st, qErr := s.Query()
		if qErr == nil && (uint32(st.State) == windows.SERVICE_RUNNING || uint32(st.State) == windows.SERVICE_START_PENDING) {
			return true, true
		}
		return true, false
	}
	// Connect with read-only SC_MANAGER_CONNECT via OpenSCManager if full access denied for Medium-IL!
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return false, false
	}
	defer windows.CloseServiceHandle(scm)
	namePtr, err := windows.UTF16PtrFromString(serviceName)
	if err != nil {
		return false, false
	}
	svcHandle, err := windows.OpenService(scm, namePtr, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return false, false
	}
	defer windows.CloseServiceHandle(svcHandle)
	var status windows.SERVICE_STATUS
	if err := windows.QueryServiceStatus(svcHandle, &status); err == nil {
		if status.CurrentState == windows.SERVICE_RUNNING || status.CurrentState == windows.SERVICE_START_PENDING {
			return true, true
		}
	}
	return true, false
}

// ElevateAndInstallService triggers a one-time UAC prompt via ShellExecuteExW("runas")
// to register and start clashfx-service.exe, waiting for completion.
func ElevateAndInstallService(serviceExePath string) error {
	type shellExecuteInfo struct {
		cbSize       uint32
		fMask        uint32
		hwnd         uintptr
		lpVerb       *uint16
		lpFile       *uint16
		lpParameters *uint16
		lpDirectory  *uint16
		nShow        int32
		hInstApp     uintptr
		lpIDList     uintptr
		lpClass      *uint16
		hkeyClass    uintptr
		dwHotKey     uint32
		hIcon        uintptr
		hProcess     windows.Handle
	}
	const seeMaskNoCloseProcess = 0x00000040

	verb, _ := windows.UTF16PtrFromString("runas")
	file, err := windows.UTF16PtrFromString(serviceExePath)
	if err != nil {
		return err
	}
	params, _ := windows.UTF16PtrFromString("install")

	sei := shellExecuteInfo{
		fMask:        seeMaskNoCloseProcess,
		lpVerb:       verb,
		lpFile:       file,
		lpParameters: params,
		nShow:        windows.SW_HIDE,
	}
	sei.cbSize = uint32(unsafe.Sizeof(sei))

	r1, _, callErr := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&sei)))
	if r1 == 0 {
		return fmt.Errorf("UAC service installation cancelled or failed: %w", callErr)
	}
	if sei.hProcess != 0 {
		defer windows.CloseHandle(sei.hProcess)
		_, _ = windows.WaitForSingleObject(sei.hProcess, 30_000)
		var code uint32
		if err := windows.GetExitCodeProcess(sei.hProcess, &code); err == nil && code != 0 {
			return fmt.Errorf("clashfx-service install exited with code %d", code)
		}
	}
	return nil
}

func SetLaunchAtLogin(enabled bool, uiExePath string) error {
	if strings.TrimSpace(os.Getenv("CLASHFX_DISABLE_LAUNCH_AGENT")) == "1" {
		return nil
	}
	if flag.Lookup("test.v") != nil && strings.TrimSpace(os.Getenv("CLASHFX_TEST_REAL_LAUNCH_AGENT")) != "1" {
		return nil
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runRegistryKeyPath, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open HKCU Run registry key: %w", err)
	}
	defer k.Close()

	if !enabled {
		err := k.DeleteValue(runRegistryValueName)
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return err
	}
	if strings.TrimSpace(uiExePath) == "" {
		return nil
	}
	quoted := fmt.Sprintf(`"%s" --minimized`, strings.Trim(uiExePath, `"`))
	return k.SetStringValue(runRegistryValueName, quoted)
}

// StartPowerResumeMonitor subscribes to Windows Modern Standby / S3 resume events
// via PowerRegisterSuspendResumeNotification so system proxy & TUN recover immediately on wake.
func StartPowerResumeMonitor(onWake func()) func() {
	if onWake == nil {
		return func() {}
	}
	if err := modpowrprof.Load(); err != nil {
		return func() {}
	}
	if err := procPowerRegisterSuspendResumeNotification.Find(); err != nil {
		return func() {}
	}
	const (
		deviceNotifyCallback = 2
		pbtApmResumeSuspend  = 0x0007
		pbtApmResumeAuto     = 0x0012
	)
	cb := syscall.NewCallback(func(_ uintptr, eventType uint32, _ uintptr) uintptr {
		if eventType == pbtApmResumeAuto || eventType == pbtApmResumeSuspend {
			go onWake()
		}
		return 0
	})
	// Pin params in activePowerSubs so Go GC never collects the struct referenced by powrprof.dll.
	params := &deviceNotifySubscribeParams{callback: cb}
	var regHandle uintptr
	powerNotifyMu.Lock()
	ret, _, _ := procPowerRegisterSuspendResumeNotification.Call(
		deviceNotifyCallback,
		uintptr(unsafe.Pointer(params)),
		uintptr(unsafe.Pointer(&regHandle)),
	)
	if ret != 0 || regHandle == 0 {
		powerNotifyMu.Unlock()
		return func() {}
	}
	activePowerSubs[regHandle] = params
	powerNotifyMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			unreg := modpowrprof.NewProc("PowerUnregisterSuspendResumeNotification")
			if unreg.Find() == nil && regHandle != 0 {
				_, _, _ = unreg.Call(regHandle)
			}
			powerNotifyMu.Lock()
			delete(activePowerSubs, regHandle)
			powerNotifyMu.Unlock()
		})
	}
}
