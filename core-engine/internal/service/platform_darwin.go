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

func (g *childGuard) activePID(cmd *exec.Cmd, exitSig *coreExitSignal) (bool, int) {
	if cmd == nil || cmd.Process == nil || exitSig == nil {
		return false, 0
	}
	select {
	case <-exitSig.done:
		return false, 0
	default:
		return true, cmd.Process.Pid
	}
}

func (g *childGuard) closeHandles() {}

func (g *childGuard) terminate(cmd *exec.Cmd, exitSig *coreExitSignal, stopTimeout time.Duration) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if g != nil && g.pgid > 0 {
		_ = syscall.Kill(-g.pgid, syscall.SIGTERM)
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)

	if exitSig != nil {
		select {
		case <-exitSig.done:
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
	if exitSig != nil {
		select {
		case <-exitSig.done:
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
// Client authorization is enforced per-connection via LOCAL_PEERCRED + LOCAL_PEERPID in VerifyPipePeer.
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
	// Disable automatic unlink-on-close so a shutting-down instance never deletes
	// a newly spawned replacement instance's live socket; caller unlinks by inode match.
	if ul, ok := ln.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(false)
	}
	// Allow local console users to dial the root socket; VerifyPipePeer enforces UID & peer binary.
	if chErr := os.Chmod(sockPath, 0666); chErr != nil {
		_ = ln.Close()
		_ = os.Remove(sockPath)
		return nil, fmt.Errorf("chmod service socket %s: %w", sockPath, chErr)
	}
	return ln, nil
}

// SocketIdentity captures the filesystem device and inode of a bound Unix socket.
type SocketIdentity struct {
	Dev int32
	Ino uint64
	OK  bool
}

// StatSocketIdentity records the current device/inode of sockPath.
func StatSocketIdentity(sockPath string) SocketIdentity {
	info, err := os.Lstat(sockPath)
	if err != nil {
		return SocketIdentity{}
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return SocketIdentity{}
	}
	return SocketIdentity{Dev: st.Dev, Ino: st.Ino, OK: true}
}

// RemoveSocketIfMatches removes sockPath only if it still refers to the same inode we created.
func RemoveSocketIfMatches(sockPath string, id SocketIdentity) {
	if !id.OK {
		return
	}
	cur := StatSocketIdentity(sockPath)
	if cur.OK && cur.Dev == id.Dev && cur.Ino == id.Ino {
		_ = os.Remove(sockPath)
	}
}

func peerExecutablePathDarwin(pid int) (string, error) {
	if pid <= 0 {
		return "", errors.New("invalid peer pid")
	}
	raw, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(raw) <= 4 {
		return "", fmt.Errorf("sysctl kern.procargs2(pid=%d): %w", pid, err)
	}
	// kern.procargs2 layout: [int32 argc][NUL-terminated exec_path][padding/argv/env]
	rest := raw[4:]
	end := 0
	for end < len(rest) && rest[end] != 0 {
		end++
	}
	exe := strings.TrimSpace(string(rest[:end]))
	if exe == "" {
		return "", fmt.Errorf("empty exec_path for pid=%d", pid)
	}
	if resolved, rErr := filepath.EvalSymlinks(exe); rErr == nil {
		exe = resolved
	}
	return filepath.Clean(exe), nil
}

func isAllowedDarwinPeerExecutable(exePath, trustedDir string) bool {
	if strings.TrimSpace(exePath) == "" {
		return false
	}
	cleanExe := resolvePathCanonical(exePath)
	base := filepath.Base(cleanExe)
	if base != "clashfx-daemon" && base != "clashfx-service" {
		return false
	}
	dir := filepath.Dir(cleanExe)
	if trustedDir != "" && dir == resolvePathCanonical(trustedDir) {
		return true
	}
	return strings.HasSuffix(dir, "/ClashFX.app/Contents/MacOS")
}

// VerifyPipePeer authenticates Unix domain socket callers via macOS LOCAL_PEERCRED
// and (when running as root) LOCAL_PEERPID + kern.procargs2 executable verification.
func VerifyPipePeer(conn net.Conn, trustedDir string) error {
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
	var peerPID int
	var pidErr error
	if ctrlErr := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		peerPID, pidErr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
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
	if cred.Uid == 0 {
		return nil
	}
	if euid != 0 && cred.Uid == euid {
		return nil
	}
	if consoleUID, ok := consoleOwnerUID(); ok && cred.Uid == consoleUID {
		if pidErr != nil || peerPID <= 0 {
			return fmt.Errorf("unauthorized Unix socket caller uid=%d (failed to query LOCAL_PEERPID: %v)", cred.Uid, pidErr)
		}
		exePath, exeErr := peerExecutablePathDarwin(peerPID)
		if exeErr != nil {
			return fmt.Errorf("unauthorized Unix socket caller uid=%d pid=%d (%v)", cred.Uid, peerPID, exeErr)
		}
		if !isAllowedDarwinPeerExecutable(exePath, trustedDir) {
			return fmt.Errorf("unauthorized Unix socket caller %q (uid=%d pid=%d)", exePath, cred.Uid, peerPID)
		}
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
// Uses O_NOFOLLOW/O_DIRECTORY + Fchownat(AT_SYMLINK_NOFOLLOW) to prevent symlink TOCTOU attacks.
func repairUserHomeOwnership(cleanHome string) {
	if os.Geteuid() != 0 || strings.TrimSpace(cleanHome) == "" {
		return
	}
	homeFD, err := unix.Open(cleanHome, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return
	}
	defer unix.Close(homeFD)

	var homeSt unix.Stat_t
	if err := unix.Fstat(homeFD, &homeSt); err != nil || homeSt.Uid == 0 {
		return
	}
	uid, gid := int(homeSt.Uid), int(homeSt.Gid)
	for _, name := range []string{"logs", "ruleset", "clashfx-rulesets", "proxies", "rules", "cache.db", ".mihomo_core.log", ".mihomo_core_logs"} {
		var targetSt unix.Stat_t
		if err := unix.Fstatat(homeFD, name, &targetSt, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			continue
		}
		// Never follow symlinks inside cleanHome.
		if targetSt.Mode&unix.S_IFMT == unix.S_IFLNK {
			continue
		}
		if targetSt.Mode&unix.S_IFMT == unix.S_IFDIR {
			dirFD, oErr := unix.Openat(homeFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
			if oErr != nil {
				continue
			}
			_ = unix.Fchown(dirFD, uid, gid)
			dirFile := os.NewFile(uintptr(dirFD), name)
			if entries, rErr := dirFile.ReadDir(-1); rErr == nil {
				for _, entry := range entries {
					if entry.Type()&os.ModeSymlink != 0 {
						continue
					}
					_ = unix.Fchownat(dirFD, entry.Name(), uid, gid, unix.AT_SYMLINK_NOFOLLOW)
				}
			}
			_ = dirFile.Close()
		} else {
			_ = unix.Fchownat(homeFD, name, uid, gid, unix.AT_SYMLINK_NOFOLLOW)
		}
	}
}
