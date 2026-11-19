package applog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRotatingFileRotationAndTail(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "mihomo-core.log")

	rf, err := NewRotatingFile(logPath, 256, 3)
	if err != nil {
		t.Fatalf("NewRotatingFile: %v", err)
	}
	defer rf.Close()

	for i := 0; i < 40; i++ {
		rf.Logf("core", "info", "event-number-%02d payload=%s", i, strings.Repeat("x", 24))
	}

	// Verify rotated backups were created and capped at maxBackups=3.
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("expected active log file: %v", err)
	}
	if _, err := os.Stat(logPath + ".1"); err != nil {
		t.Fatalf("expected rotated .1 backup: %v", err)
	}
	if _, err := os.Stat(logPath + ".4"); !os.IsNotExist(err) {
		t.Fatalf("expected .4 backup not to exist, got err=%v", err)
	}

	tail := TailFile(logPath, 5)
	if len(tail) != 5 {
		t.Fatalf("expected 5 tail lines, got %d: %v", len(tail), tail)
	}
	if !strings.Contains(tail[len(tail)-1], "event-number-39") {
		t.Fatalf("expected last line to contain event-number-39, got %q", tail[len(tail)-1])
	}
}

func TestRotatingFileConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "daemon.log")

	rf, err := NewRotatingFile(logPath, 1024, 2)
	if err != nil {
		t.Fatalf("NewRotatingFile: %v", err)
	}
	defer rf.Close()

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 30; i++ {
				rf.WriteStampedLine(fmt.Sprintf("w%d", id), fmt.Sprintf("msg-%d", i))
			}
		}(g)
	}
	wg.Wait()

	lines := TailFile(logPath, 20)
	if len(lines) == 0 {
		t.Fatal("expected non-empty persisted log lines")
	}
}
