package service

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunnerHelperProcess(t *testing.T) {
	mode := os.Getenv("CLASHFX_TEST_CORE_MODE")
	if mode == "" {
		return
	}
	switch mode {
	case "ok":
		fmt.Println("mihomo-bin pid=12345")
		fmt.Printf("%s /tmp/config.yaml\n", ReadyMarker)
		_, _ = io.Copy(io.Discard, os.Stdin)
		fmt.Println("Mihomo shutting down")
		os.Exit(0)
	case "fail":
		fmt.Fprintln(os.Stderr, "Parse config error: invalid yaml")
		os.Exit(1)
	}
}

func TestRunnerLifecycleAndValidation(t *testing.T) {
	trustedDir := t.TempDir()
	homeDir := t.TempDir()
	cfgPath := filepath.Join(homeDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("mixed-port: 7890\n"), 0600); err != nil {
		t.Fatal(err)
	}

	// Copy current test binary into trustedDir as fake mihomo_core wrapper script/binary
	selfExe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wrapperPath := filepath.Join(trustedDir, "fake_core.sh")
	script := fmt.Sprintf("#!/bin/sh\nexec %q -test.run=^TestRunnerHelperProcess$ -- \"$@\"\n", selfExe)
	if err := os.WriteFile(wrapperPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	runner := NewRunner(trustedDir)
	runner.SetCoreBinaryForTest("fake_core.sh", 3*time.Second, 3*time.Second)

	// Reject relative or UNC paths
	if resp := runner.Handle(Request{Action: "start_core", HomeDir: "relative", ConfigFile: cfgPath}); resp.OK {
		t.Fatal("expected relative home_dir to be rejected")
	}
	if resp := runner.Handle(Request{Action: "start_core", HomeDir: `\\evil\share`, ConfigFile: cfgPath}); resp.OK {
		t.Fatal("expected UNC path to be rejected")
	}

	// Startup failure surfaces stderr cleanly
	t.Setenv("CLASHFX_TEST_CORE_MODE", "fail")
	resp := runner.Handle(Request{Action: "start_core", HomeDir: homeDir, ConfigFile: cfgPath})
	if resp.OK || !strings.Contains(resp.Message, "Parse config error") {
		t.Fatalf("expected startup failure with parse error, got %+v", resp)
	}

	// Successful start + status + graceful stop
	t.Setenv("CLASHFX_TEST_CORE_MODE", "ok")
	resp = runner.Handle(Request{Action: "start_core", HomeDir: homeDir, ConfigFile: cfgPath})
	if !resp.OK || resp.Status == nil || !resp.Status.Running || resp.Status.PID <= 0 {
		t.Fatalf("expected running core, got %+v", resp)
	}
	firstPID := resp.Status.PID

	// Restart replaces previous process cleanly
	resp = runner.Handle(Request{Action: "start_core", HomeDir: homeDir, ConfigFile: cfgPath})
	if !resp.OK || resp.Status == nil || !resp.Status.Running || resp.Status.PID == firstPID {
		t.Fatalf("expected restarted core with new PID (prev=%d), got %+v", firstPID, resp)
	}

	stopResp := runner.Handle(Request{Action: "stop_core"})
	if !stopResp.OK || stopResp.Status == nil || stopResp.Status.Running {
		t.Fatalf("expected stopped core, got %+v", stopResp)
	}
}
