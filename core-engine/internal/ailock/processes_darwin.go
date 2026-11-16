//go:build darwin

package ailock

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func listOSProcesses() ([]ProcessEntry, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/ps", "-axo", "pid=,comm=")
	cmd.Stdin = nil
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return nil, err
	}

	lines := strings.Split(stdout.String(), "\n")
	out := make([]ProcessEntry, 0, len(lines))
	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		sep := strings.IndexAny(line, " \t")
		if sep <= 0 {
			continue
		}
		pid, err := strconv.Atoi(line[:sep])
		if err != nil || pid <= 0 {
			continue
		}
		exePath := strings.TrimSpace(line[sep+1:])
		if exePath == "" {
			continue
		}
		out = append(out, ProcessEntry{
			PID:     pid,
			Name:    filepath.Base(exePath),
			ExePath: exePath,
		})
	}
	return out, nil
}

func isProcessAliveDarwin(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func terminatePIDs(pids []int) int {
	if len(pids) == 0 {
		return 0
	}
	unique := make([]int, 0, len(pids))
	seen := make(map[int]bool, len(pids))
	for _, pid := range pids {
		if pid > 1 && !seen[pid] {
			seen[pid] = true
			unique = append(unique, pid)
			_ = syscall.Kill(pid, syscall.SIGTERM)
		}
	}

	deadline := time.Now().Add(1200 * time.Millisecond)
	for time.Now().Before(deadline) {
		anyAlive := false
		for _, pid := range unique {
			if isProcessAliveDarwin(pid) {
				anyAlive = true
				break
			}
		}
		if !anyAlive {
			break
		}
		time.Sleep(80 * time.Millisecond)
	}

	for _, pid := range unique {
		if isProcessAliveDarwin(pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}

	killDeadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(killDeadline) {
		anyAlive := false
		for _, pid := range unique {
			if isProcessAliveDarwin(pid) {
				anyAlive = true
				break
			}
		}
		if !anyAlive {
			break
		}
		time.Sleep(40 * time.Millisecond)
	}

	terminated := 0
	for _, pid := range unique {
		if !isProcessAliveDarwin(pid) {
			terminated++
		}
	}
	return terminated
}
