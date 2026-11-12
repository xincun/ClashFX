package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/clashfx/core-engine/internal/daemon"
)

func main() {
	if len(os.Args) >= 2 && os.Args[1] == "version" {
		fmt.Printf("clashfx-daemon %s (%s/%s)\n", daemon.AppVersion, runtime.GOOS, runtime.GOARCH)
		return
	}

	var (
		pipeEndpoint = flag.String("pipe", "", "Named Pipe or Unix socket endpoint override")
		dataDir      = flag.String("data-dir", "", "Application data directory override")
		binDir       = flag.String("bin-dir", "", "Trusted binary directory override")
		resourceDir  = flag.String("resource-dir", "", "Bundled resources directory override")
		dashboardDir = flag.String("dashboard-dir", "", "Bundled MetaCubeXD dashboard directory override")
		mutexKey     = flag.String("mutex-key", "", "Single-instance mutex key override")
	)
	flag.Parse()

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
		cand := filepath.Join(*binDir, "resources")
		if st, sErr := os.Stat(cand); sErr == nil && st.IsDir() {
			*resourceDir = cand
		}
	}
	if *dashboardDir == "" {
		cand := filepath.Join(*binDir, "resources", "dashboard")
		if st, sErr := os.Stat(cand); sErr == nil && st.IsDir() {
			*dashboardDir = cand
		}
	}

	unlock, err := daemon.AcquireSingleInstanceLock(*mutexKey)
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
