package portprobe

import (
	"errors"
	"net"
	"os"
	"syscall"
	"testing"
)

func TestWindowsReservedPortClassificationAndSkip(t *testing.T) {
	if !IsWindowsReservedPortError(syscall.Errno(10013)) {
		t.Fatal("expected WSAEACCES (10013) to be recognized as OS-reserved port")
	}
	if !IsWindowsReservedPortError(os.ErrPermission) {
		t.Fatal("expected os.ErrPermission to be recognized as OS-reserved port")
	}
	if !IsWindowsReservedPortError(errors.New("bind: An attempt was made to access a socket in a way forbidden by its access permissions.")) {
		t.Fatal("expected WinNAT permission error message to be recognized")
	}
	if IsWindowsReservedPortError(errors.New("address already in use")) {
		t.Fatal("EADDRINUSE should not be classified as OS-reserved permission error")
	}

	// Simulate Hyper-V reserved block 21000..21004; allocator must skip to 21005.
	fakeProbe := func(port int, _ bool) (bool, bool) {
		if port >= 21000 && port <= 21004 {
			return false, true
		}
		return true, false
	}
	got, err := FindAvailablePort(21000, 21020, map[int]bool{21005: true}, true, fakeProbe)
	if err != nil || got != 21006 {
		t.Fatalf("FindAvailablePort = %d, %v; want 21006", got, err)
	}

	rev, err := FindAvailablePortReverse(20000, 20999, map[int]bool{20999: true}, false, func(port int, _ bool) (bool, bool) {
		if port == 20998 {
			return false, true
		}
		return true, false
	})
	if err != nil || rev != 20997 {
		t.Fatalf("FindAvailablePortReverse = %d, %v; want 20997", rev, err)
	}

	// Real socket check: occupied TCP port is reported unavailable.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	busyPort := ln.Addr().(*net.TCPAddr).Port
	if ok, _ := ProbeLoopbackPort(busyPort, false); ok {
		t.Fatalf("occupied port %d reported available", busyPort)
	}
}
