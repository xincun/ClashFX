package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/clashfx/core-engine/internal/daemon"
)

func main() {
	if len(os.Args) >= 2 && os.Args[1] == "version" {
		fmt.Printf("clashfx-daemon %s (%s/%s)\n", daemon.AppVersion, runtime.GOOS, runtime.GOARCH)
		return
	}

	var (
		pipeEndpoint     = flag.String("pipe", "", "Named Pipe or Unix socket endpoint override")
		dataDir          = flag.String("data-dir", "", "Application data directory override")
		binDir           = flag.String("bin-dir", "", "Trusted binary directory override")
		resourceDir      = flag.String("resource-dir", "", "Bundled resources directory override")
		dashboardDir     = flag.String("dashboard-dir", "", "Bundled MetaCubeXD dashboard directory override")
		mutexKey         = flag.String("mutex-key", "", "Single-instance mutex key override")
		cleanupUninstall = flag.Bool("cleanup-uninstall", false, "Gracefully stop running daemon if active, force-restore OS proxy, and remove auto-start entry")
	)
	flag.Parse()

	if *cleanupUninstall {
		runUninstallCleanup(*pipeEndpoint, *dataDir)
		return
	}

	exePath, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve executable path: %v\n", err)
		os.Exit(1)
	}
	defaultBinDir := filepath.Dir(exePath)
	if *binDir == "" {
		*binDir = defaultBinDir
	}
	if *resourceDir == "" {
		for _, cand := range []string{
			filepath.Join(*binDir, "resources"),
			filepath.Clean(filepath.Join(*binDir, "..", "Resources")),
		} {
			if st, sErr := os.Stat(cand); sErr == nil && st.IsDir() {
				*resourceDir = cand
				break
			}
		}
	}
	if *dashboardDir == "" && *resourceDir != "" {
		cand := filepath.Join(*resourceDir, "dashboard")
		if st, sErr := os.Stat(cand); sErr == nil && st.IsDir() {
			*dashboardDir = cand
		}
	}

	effectiveMutexKey := *mutexKey
	if effectiveMutexKey == "" && (*pipeEndpoint != "" || *dataDir != "") {
		effectiveMutexKey = *pipeEndpoint + "|" + *dataDir
	}
	unlock, err := daemon.AcquireSingleInstanceLock(effectiveMutexKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(2)
	}
	defer unlock()

	srv, err := daemon.NewServer(*pipeEndpoint, daemon.Config{
		AppDataDir:         *dataDir,
		BundledResourceDir: *resourceDir,
		DashboardUIDir:     *dashboardDir,
		BinDir:             *binDir,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "initialize daemon: %v\n", err)
		os.Exit(1)
	}

	if err := srv.Start(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "start daemon IPC server: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("clashfx-daemon ready (%s/%s)\n", runtime.GOOS, runtime.GOARCH)

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		srv.Stop()
	}()

	srv.Wait()
}

func runUninstallCleanup(endpoint, dataDir string) {
	if conn, err := daemon.DialIPC(endpoint); err == nil {
		_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
		params, _ := json.Marshal(map[string]bool{"force_restore_proxy": true})
		req, _ := json.Marshal(daemon.RPCRequest{
			ID:     1,
			Method: "quit_daemon",
			Params: params,
		})
		req = append(req, '\n')
		_, _ = conn.Write(req)
		_, _ = bufio.NewReader(conn).ReadBytes('\n')
		_ = conn.Close()

		// Wait for the running daemon to finish graceful teardown (AI Guard disarm,
		// sysproxy restore, core stop) rather than racing with CleanupUninstallState.
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			probe, dialErr := daemon.DialIPC(endpoint)
			if dialErr != nil {
				break
			}
			_ = probe.Close()
			time.Sleep(150 * time.Millisecond)
		}
	}
	if err := daemon.CleanupUninstallState(dataDir); err != nil {
		fmt.Fprintf(os.Stderr, "[clashfx-daemon] uninstall cleanup warning: %v\n", err)
	}
}
