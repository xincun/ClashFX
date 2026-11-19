package service

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const fakeRunnerCoreSource = `package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	// Strictly reject any non-standard flags (such as -watch-stdin) just like stock upstream mihomo!
	for _, arg := range os.Args[1:] {
		if arg == "-watch-stdin" || arg == "-convert-share-links" || arg == "--features" {
			fmt.Fprintf(os.Stderr, "flag provided but not defined: %s\n", arg)
			os.Exit(2)
		}
	}
	switch os.Getenv("CLASHFX_TEST_CORE_MODE") {
	case "ok":
		if os.Getenv("SAFE_PATHS") == "" {
			fmt.Fprintln(os.Stderr, "SAFE_PATHS was not injected into child process")
			os.Exit(3)
		}
		ctrlAddr := os.Getenv("CLASHFX_TEST_CTRL_ADDR")
		if ctrlAddr != "" {
			mux := http.NewServeMux()
			mux.HandleFunc("/version", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte("{\"meta\":true,\"version\":\"v1.19.34\"}"))
			})
			ln, err := net.Listen("tcp", ctrlAddr)
			if err != nil {
				fmt.Fprintf(os.Stderr, "listen %s: %v\n", ctrlAddr, err)
				os.Exit(1)
			}
			defer ln.Close()
			srv := &http.Server{Handler: mux}
			go func() { _ = srv.Serve(ln) }()
		}
		fmt.Printf("time=\"now\" level=info msg=\"RESTful API listening at: %s\"\n", ctrlAddr)
		sigCh := make(chan os.Signal, 2)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
		<-sigCh
	case "fail":
		fmt.Fprintln(os.Stderr, "Parse config error: invalid yaml")
		os.Exit(1)
	}
}
`

func buildFakeRunnerBinary(t *testing.T, outPath string) {
	t.Helper()
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "go.mod"), []byte("module fake_runner\n\ngo 1.21\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "main.go"), []byte(fakeRunnerCoreSource), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-ldflags=-s -w", "-o", outPath, ".")
	cmd.Dir = srcDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile fake runner binary: %v\n%s", err, string(out))
	}
}

func TestRunnerLifecycleAndValidation(t *testing.T) {
	trustedDir := t.TempDir()
	homeDir := t.TempDir()

	// Pick an ephemeral port for external-controller readiness check
	probeLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctrlAddr := probeLn.Addr().String()
	_ = probeLn.Close()

	cfgPath := filepath.Join(homeDir, "config.yaml")
	cfgYAML := fmt.Sprintf("external-controller: %s\n", ctrlAddr)
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0600); err != nil {
		t.Fatal(err)
	}

	fakeExeName := "fake_core"
	if runtime.GOOS == "windows" {
		fakeExeName += ".exe"
	}
	wrapperPath := filepath.Join(trustedDir, fakeExeName)
	buildFakeRunnerBinary(t, wrapperPath)

	runner := NewRunner(trustedDir)
	runner.SetCoreBinaryForTest(fakeExeName, 10*time.Second, 5*time.Second)

	// Reject relative, UNC, filesystem root, trustedDir, and symlinked system log paths
	if resp := runner.Handle(Request{Action: "start_core", HomeDir: "relative", ConfigFile: cfgPath}); resp.OK {
		t.Fatal("expected relative home_dir to be rejected")
	}
	if resp := runner.Handle(Request{Action: "start_core", HomeDir: `\\evil\share`, ConfigFile: cfgPath}); resp.OK {
		t.Fatal("expected UNC path to be rejected")
	}
	if resp := runner.Handle(Request{Action: "start_core", HomeDir: `//evil/share`, ConfigFile: cfgPath}); resp.OK {
		t.Fatal("expected forward-slash UNC path to be rejected")
	}
	if resp := runner.Handle(Request{Action: "start_core", HomeDir: trustedDir, ConfigFile: filepath.Join(trustedDir, "config.yaml")}); resp.OK {
		t.Fatal("expected home_dir == trustedDir to be rejected")
	}
	if runtime.GOOS != "windows" {
		if resp := runner.Handle(Request{Action: "start_core", HomeDir: "/", ConfigFile: cfgPath}); resp.OK {
			t.Fatal("expected filesystem root home_dir to be rejected")
		}
		symLog := filepath.Join(t.TempDir(), "etc-link")
		if err := os.Symlink("/etc", symLog); err == nil {
			if resp := runner.Handle(Request{Action: "start_core", HomeDir: homeDir, ConfigFile: cfgPath, LogDir: filepath.Join(symLog, "sub")}); resp.OK {
				t.Fatal("expected symlinked /etc log_dir to be rejected")
			}
		}
	}
	otherDir := t.TempDir()
	outsideCfg := filepath.Join(otherDir, "outside.yaml")
	_ = os.WriteFile(outsideCfg, []byte(cfgYAML), 0600)
	if resp := runner.Handle(Request{Action: "start_core", HomeDir: homeDir, ConfigFile: outsideCfg}); resp.OK {
		t.Fatal("expected config_file outside home_dir to be rejected")
	}

	// Startup failure surfaces stderr cleanly
	t.Setenv("CLASHFX_TEST_CTRL_ADDR", ctrlAddr)
	t.Setenv("CLASHFX_TEST_CORE_MODE", "fail")
	resp := runner.Handle(Request{Action: "start_core", HomeDir: homeDir, ConfigFile: cfgPath})
	if resp.OK || !strings.Contains(resp.Message, "Parse config error") {
		t.Fatalf("expected startup failure with parse error, got %+v", resp)
	}

	// Successful start with stock Mihomo flags + HTTP /version readiness + status + graceful stop
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
