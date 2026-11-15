//go:build darwin

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

	"golang.org/x/sys/unix"
)

const (
	DarwinLaunchDaemonLabel  = "com.clashfx.service"
	DarwinLaunchDaemonPlist  = "/Library/LaunchDaemons/com.clashfx.service.plist"
	DefaultDarwinServiceSock = "/var/run/clashfx-service.sock"
)

func ServiceSocketPath() string {
	if custom := strings.TrimSpace(os.Getenv("CLASHFX_SERVICE_SOCK")); custom != "" {
		return custom
	}
	return DefaultDarwinServiceSock
}

func configureChildProcess(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
}

type childGuard struct {
	pgid int
}

func attachChildGuard(cmd *exec.Cmd) *childGuard {
	if cmd == nil || cmd.Process == nil {
		return &childGuard{}
	}
	return &childGuard{pgid: cmd.Process.Pid}
}

func (g *childGuard) activePID(cmd *exec.Cmd, doneCh <-chan error) (bool, int) {
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

func (g *childGuard) closeHandles() {}

func (g *childGuard) terminate(cmd *exec.Cmd, doneCh <-chan error, stopTimeout time.Duration) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if g != nil && g.pgid > 0 {
		_ = syscall.Kill(-g.pgid, syscall.SIGTERM)
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)

	if doneCh != nil {
		select {
		case <-doneCh:
			return nil
		case <-time.After(stopTimeout):
		}
	}

	var stopErr error
	if g != nil && g.pgid > 0 {
		_ = syscall.Kill(-g.pgid, syscall.SIGKILL)
	}
	if killErr := cmd.Process.Kill(); killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
		stopErr = fmt.Errorf("kill unresponsive core: %w", killErr)
	}
	if doneCh != nil {
		select {
		case <-doneCh:
		case <-time.After(2 * time.Second):
		}
	}
	return stopErr
}

// Call sends a single JSON request over the macOS privileged service Unix socket.
func Call(ctx context.Context, req Request) (Response, error) {
	timeout := 15 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		if rem := time.Until(deadline); rem > 0 {
			timeout = rem
		}
	}
	sockPath := ServiceSocketPath()
	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "unix", sockPath)
	if err != nil {
		return Response{}, fmt.Errorf("connect %s: %w", sockPath, err)
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

// ListenSecurePipe creates the root-supervised Unix socket for ClashFXService.
// Client authorization is enforced per-connection via LOCAL_PEERCRED in VerifyPipePeer.
func ListenSecurePipe() (net.Listener, error) {
	sockPath := ServiceSocketPath()
	if err := os.MkdirAll(filepath.Dir(sockPath), 0755); err != nil {
		return nil, fmt.Errorf("create service socket dir: %w", err)
	}
	_ = os.Remove(sockPath)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", sockPath, err)
	}
	// Allow local console users to dial the root socket; VerifyPipePeer enforces UID.
	if chErr := os.Chmod(sockPath, 0666); chErr != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("chmod service socket %s: %w", sockPath, chErr)
	}
	return ln, nil
}

// VerifyPipePeer authenticates Unix domain socket callers via macOS LOCAL_PEERCRED.
// Allowed callers: root (UID 0), same effective UID, or the active /dev/console GUI user.
func VerifyPipePeer(conn net.Conn, _ string) error {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return fmt.Errorf("unexpected connection type %T (expected Unix socket)", conn)
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return fmt.Errorf("inspect peer syscall conn: %w", err)
	}

	var cred *unix.Xucred
	var credErr error
	if ctrlErr := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	}); ctrlErr != nil {
		return fmt.Errorf("socket control error: %w", ctrlErr)
	}
	if credErr != nil {
		return fmt.Errorf("query LOCAL_PEERCRED: %w", credErr)
	}
	if cred == nil {
		return errors.New("missing peer credentials")
	}

	euid := uint32(os.Geteuid())
	if cred.Uid == 0 || cred.Uid == euid {
		return nil
	}
	if consoleUID, ok := consoleOwnerUID(); ok && cred.Uid == consoleUID {
		return nil
	}
	return fmt.Errorf("unauthorized Unix socket caller uid=%d", cred.Uid)
}

func consoleOwnerUID() (uint32, bool) {
	info, err := os.Stat("/dev/console")
	if err != nil {
		return 0, false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return st.Uid, true
}

func prepareUpgradedCoreFile(path string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	if os.Geteuid() == 0 {
		_ = os.Chown(path, 0, 0)
	}
	_ = exec.Command("/usr/bin/xattr", "-d", "com.apple.quarantine", path).Run()
}

func refreshCorePlatformPostInstall(corePath string) {
	if os.Geteuid() != 0 || strings.TrimSpace(corePath) == "" {
		return
	}
	const fw = "/usr/libexec/ApplicationFirewall/socketfilterfw"
	if _, err := os.Stat(fw); err != nil {
		return
	}
	for _, arg := range []string{"--add", "--unblockapp"} {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		_ = exec.CommandContext(ctx, fw, arg, corePath).Run()
		cancel()
	}
}

// repairUserHomeOwnership restores user ownership on runtime directories/files created
// inside the desktop user's ~/.config/clashfx directory while mihomo_core runs as root.
func repairUserHomeOwnership(cleanHome string) {
	if os.Geteuid() != 0 || strings.TrimSpace(cleanHome) == "" {
		return
	}
	info, err := os.Stat(cleanHome)
	if err != nil {
		return
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid == 0 {
		return
	}
	uid, gid := int(st.Uid), int(st.Gid)
	for _, name := range []string{"ruleset", "clashfx-rulesets", "proxies", "rules", "cache.db", ".mihomo_core.log", ".mihomo_core_logs"} {
		target := filepath.Join(cleanHome, name)
		linfo, lerr := os.Lstat(target)
		if lerr != nil {
			continue
		}
		_ = os.Lchown(target, uid, gid)
		if linfo.IsDir() {
			if entries, rerr := os.ReadDir(target); rerr == nil {
				for _, entry := range entries {
					_ = os.Lchown(filepath.Join(target, entry.Name()), uid, gid)
				}
			}
		}
	}
}
