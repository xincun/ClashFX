//go:build windows

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

var (
	modkernel32                      = windows.NewLazySystemDLL("kernel32.dll")
	procGetNamedPipeClientProcessId  = modkernel32.NewProc("GetNamedPipeClientProcessId")
)

func configureChildProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
}

type jobBasicProcessIDList struct {
	NumberOfAssignedProcesses uint32
	NumberOfProcessIdsInList  uint32
	ProcessIdList             [16]uintptr
}

// childGuard binds mihomo_core.exe (and any self-upgraded/restarted child it spawns)
// into a Win32 Job Object with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE, eliminating any
// need for custom CLI flags like -watch-stdin.
type childGuard struct {
	job windows.Handle
}

func attachChildGuard(cmd *exec.Cmd) *childGuard {
	if cmd == nil || cmd.Process == nil {
		return &childGuard{}
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return &childGuard{}
	}

	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return &childGuard{}
	}

	proc, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(cmd.Process.Pid),
	)
	if err != nil {
		_ = windows.CloseHandle(job)
		return &childGuard{}
	}
	assignErr := windows.AssignProcessToJobObject(job, proc)
	_ = windows.CloseHandle(proc)
	if assignErr != nil {
		_ = windows.CloseHandle(job)
		return &childGuard{}
	}

	return &childGuard{job: job}
}

func (g *childGuard) activePID(cmd *exec.Cmd, doneCh <-chan error) (bool, int) {
	if g != nil && g.job != 0 {
		var list jobBasicProcessIDList
		var retLen uint32
		err := windows.QueryInformationJobObject(
			g.job,
			windows.JobObjectBasicProcessIdList,
			uintptr(unsafe.Pointer(&list)),
			uint32(unsafe.Sizeof(list)),
			&retLen,
		)
		if err == nil || errors.Is(err, windows.ERROR_MORE_DATA) {
			if list.NumberOfAssignedProcesses > 0 && list.NumberOfProcessIdsInList > 0 {
				return true, int(list.ProcessIdList[0])
			}
			if list.NumberOfAssignedProcesses == 0 {
				return false, 0
			}
		}
	}
	if cmd == nil || cmd.Process == nil || doneCh == nil {
		return false, 0
	}
	select {
	case <-doneCh:
		return false, 0
	default:
		return true, cmd.Process.Pid
	}
}

func (g *childGuard) closeHandles() {
	if g != nil && g.job != 0 {
		_ = windows.CloseHandle(g.job)
		g.job = 0
	}
}

func (g *childGuard) terminate(cmd *exec.Cmd, doneCh <-chan error, _ time.Duration) error {
	var stopErr error
	if g != nil && g.job != 0 {
		if err := windows.TerminateJobObject(g.job, 0); err != nil && !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			stopErr = fmt.Errorf("terminate core job object: %w", err)
		}
		_ = windows.CloseHandle(g.job)
		g.job = 0
	} else if cmd != nil && cmd.Process != nil {
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			stopErr = fmt.Errorf("kill core process: %w", err)
		}
	}
	if doneCh != nil {
		select {
		case <-doneCh:
		case <-time.After(2 * time.Second):
		}
	}
	return stopErr
}

// Call sends a single JSON request to the privileged Windows Service Named Pipe.
func Call(ctx context.Context, req Request) (Response, error) {
	timeout := 15 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		if rem := time.Until(deadline); rem > 0 {
			timeout = rem
		}
	}
	conn, err := winio.DialPipeContext(ctx, ServicePipeName)
	if err != nil {
		return Response{}, fmt.Errorf("connect %s: %w", ServicePipeName, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return Response{}, fmt.Errorf("send service request: %w", err)
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return Response{}, fmt.Errorf("decode service response: %w", err)
	}
	return resp, nil
}

// ListenSecurePipe creates the ClashFXService Named Pipe protected by an explicit SDDL DACL:
//   - Protected DACL (D:P)
//   - SYSTEM (SY): Generic All
//   - Built-in Administrators (BA): Generic All
//   - Authenticated Users (AU): Generic Read + Generic Write (blocks Anonymous & untrusted AppContainers)
func ListenSecurePipe() (net.Listener, error) {
	cfg := &winio.PipeConfig{
		SecurityDescriptor: "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;AU)",
		InputBufferSize:    16 * 1024,
		OutputBufferSize:   16 * 1024,
	}
	return winio.ListenPipe(ServicePipeName, cfg)
}

// VerifyPipePeer ensures only trusted ClashFX binaries (clashfx-daemon.exe or
// clashfx-service.exe) located inside trustedDir can invoke privileged service actions.
func VerifyPipePeer(conn net.Conn, trustedDir string) error {
	pipeConn, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return errors.New("unauthorized pipe caller: invalid pipe handle")
	}
	var clientPID uint32
	r1, _, callErr := procGetNamedPipeClientProcessId.Call(
		pipeConn.Fd(),
		uintptr(unsafe.Pointer(&clientPID)),
	)
	if r1 == 0 || clientPID == 0 {
		return fmt.Errorf("unauthorized pipe caller: query client PID failed: %w", callErr)
	}

	proc, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, clientPID)
	if err != nil {
		return fmt.Errorf("unauthorized pipe caller: open client process %d: %w", clientPID, err)
	}
	defer windows.CloseHandle(proc)

	buf := make([]uint16, windows.MAX_PATH*4)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(proc, 0, &buf[0], &size); err != nil {
		return fmt.Errorf("unauthorized pipe caller: query image path: %w", err)
	}
	clientExe := windows.UTF16ToString(buf[:size])
	if resolved, err := filepath.EvalSymlinks(clientExe); err == nil {
		clientExe = resolved
	}
	cleanTrusted := filepath.Clean(trustedDir)
	if resolvedDir, err := filepath.EvalSymlinks(cleanTrusted); err == nil {
		cleanTrusted = resolvedDir
	}

	clientDir := filepath.Clean(filepath.Dir(clientExe))
	clientBase := strings.ToLower(filepath.Base(clientExe))
	if !strings.EqualFold(clientDir, cleanTrusted) ||
		(clientBase != "clashfx-daemon.exe" && clientBase != "clashfx-service.exe") {
		return fmt.Errorf("unauthorized pipe caller %q (must be clashfx-daemon.exe inside %q)", clientExe, cleanTrusted)
	}
	return nil
}

func prepareUpgradedCoreFile(_ string) {}

func refreshCorePlatformPostInstall(_ string) {}

func repairUserHomeOwnership(_ string) {}
