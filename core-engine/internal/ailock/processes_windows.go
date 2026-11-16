//go:build windows

package ailock

import (
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func queryProcessImagePathWindows(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)

	buf := make([]uint16, 1024)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil || size == 0 {
		return ""
	}
	return windows.UTF16ToString(buf[:size])
}

func listOSProcesses() ([]ProcessEntry, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)

	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	if err := windows.Process32First(snap, &pe); err != nil {
		return nil, err
	}

	var out []ProcessEntry
	for {
		pid := int(pe.ProcessID)
		if pid > 0 {
			name := strings.TrimSpace(windows.UTF16ToString(pe.ExeFile[:]))
			exePath := queryProcessImagePathWindows(pe.ProcessID)
			if name == "" && exePath != "" {
				name = filepath.Base(strings.ReplaceAll(exePath, `\`, "/"))
			}
			out = append(out, ProcessEntry{
				PID:     pid,
				Name:    name,
				ExePath: exePath,
			})
		}
		if err := windows.Process32Next(snap, &pe); err != nil {
			break
		}
	}
	return out, nil
}

func terminatePIDs(pids []int) int {
	terminated := 0
	seen := make(map[int]bool, len(pids))
	for _, pid := range pids {
		if pid <= 0 || seen[pid] {
			continue
		}
		seen[pid] = true
		h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, uint32(pid))
		if err != nil {
			continue
		}
		if tErr := windows.TerminateProcess(h, 1); tErr == nil {
			_, _ = windows.WaitForSingleObject(h, 1000)
			terminated++
		}
		_ = windows.CloseHandle(h)
	}
	return terminated
}
