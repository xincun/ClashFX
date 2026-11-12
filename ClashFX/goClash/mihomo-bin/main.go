package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/constant/features"
	"github.com/metacubex/mihomo/hub"
	"github.com/metacubex/mihomo/hub/executor"
	"github.com/metacubex/mihomo/hub/route"
	"github.com/metacubex/mihomo/listener"
	"github.com/metacubex/mihomo/log"
	"github.com/metacubex/mihomo/tunnel"
	"gopkg.in/yaml.v3"
)

const recoverySelectionsConfigKey = "x-clashfx-recovery-selections"

func main() {
	os.Exit(runWithStdin(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	return runWithStdin(args, os.Stdin, stdout, stderr)
}

func runWithStdin(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("mihomo-bin", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configFile := flags.String("f", "", "config file path")
	homeDir := flags.String("d", "", "config home directory")
	extUI := flags.String("ext-ui", "", "override external UI directory")
	watchStdin := flags.Bool("watch-stdin", false, "shut down cleanly when stdin reaches EOF")
	validateOnly := flags.Bool("t", false, "check configuration and exit")
	showFeatures := flags.Bool("features", false, "print compiled core capabilities and exit")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	if *showFeatures {
		if err := json.NewEncoder(stdout).Encode(map[string]any{"features": features.Tags()}); err != nil {
			return 1
		}
		return 0
	}

	if *homeDir != "" {
		if !filepath.IsAbs(*homeDir) {
			currentDir, _ := os.Getwd()
			*homeDir = filepath.Join(currentDir, *homeDir)
		}
		constant.SetHomeDir(*homeDir)
	}

	if *configFile != "" {
		if !filepath.IsAbs(*configFile) {
			currentDir, _ := os.Getwd()
			*configFile = filepath.Join(currentDir, *configFile)
		}
		constant.SetConfig(*configFile)
	} else {
		configPath := filepath.Join(constant.Path.HomeDir(), constant.Path.Config())
		constant.SetConfig(configPath)
	}

	cfg, err := executor.Parse()
	if err != nil {
		if *validateOnly {
			fmt.Fprintf(stderr, "Parse config error: %s\n", err.Error())
		} else {
			log.Errorln("Parse config error: %s", err.Error())
		}
		return 1
	}
	if *extUI != "" {
		uiDir := *extUI
		if !filepath.IsAbs(uiDir) {
			currentDir, _ := os.Getwd()
			uiDir = filepath.Join(currentDir, uiDir)
		}
		cfg.Controller.ExternalUI = uiDir
	}
	if err := validateTUNBuildCapability(cfg.General.Tun.Enable, cfg.General.Tun.Stack, features.WithGVisor); err != nil {
		fmt.Fprintf(stderr, "Core capability error: %s\n", err)
		return 1
	}

	recoverySelections, err := parseStartupRecoverySelections(constant.Path.Config())
	if err != nil {
		if *validateOnly {
			fmt.Fprintf(stderr, "Parse config error: %s\n", err.Error())
		} else {
			log.Errorln("Parse config error: %s", err.Error())
		}
		return 1
	}
	if *validateOnly {
		fmt.Fprintf(stdout, "configuration is valid: %s\n", constant.Path.Config())
		return 0
	}

	if runtime.GOOS == "windows" {
		fmt.Fprintf(stdout, "mihomo-bin pid=%d\n", os.Getpid())
	} else {
		fmt.Fprintf(stdout, "mihomo-bin euid=%d uid=%d pid=%d\n", os.Geteuid(), os.Getuid(), os.Getpid())
	}

	if len(recoverySelections) > 0 {
		executor.SetStartupRecoverySelections(recoverySelections)
	}
	if err := hub.ApplyConfigAndValidate(cfg); err != nil {
		log.Errorln("Startup validation error: %s", err.Error())
		return 1
	}
	defer shutdownCore()

	var stdinClosedCh <-chan struct{}
	if *watchStdin && stdin != nil {
		ch := make(chan struct{})
		stdinClosedCh = ch
		go func() {
			_, _ = io.Copy(io.Discard, stdin)
			close(ch)
		}()
	}

	fmt.Fprintf(stdout, "mihomo-bin started, config: %s\n", constant.Path.Config())

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	select {
	case <-sigCh:
	case <-stdinClosedCh:
	}
	return 0
}

func shutdownCore() {
	executor.WithRuntimeConfigLock(func() {
		listener.ReCreateHTTP(0, tunnel.Tunnel)
		listener.ReCreateSocks(0, tunnel.Tunnel)
		listener.ReCreateMixed(0, tunnel.Tunnel)
		listener.ReCreateRedir(0, tunnel.Tunnel)
		listener.ReCreateTProxy(0, tunnel.Tunnel)
		listener.PatchInboundListeners(nil, tunnel.Tunnel, true)
		route.ReCreateServer(&route.Config{})
		executor.Shutdown()
	})
}

func validateTUNBuildCapability(enabled bool, stack constant.TUNStack, withGVisor bool) error {
	if enabled && !withGVisor && (stack == constant.TunGvisor || stack == constant.TunMixed) {
		return fmt.Errorf("unsupported_tun_stack_build: %s TUN requires a core built with -tags with_gvisor", stack)
	}
	return nil
}

func parseStartupRecoverySelections(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read startup recovery selections: %w", err)
	}

	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("parse startup recovery selections: %w", err)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("configuration root must be a mapping")
	}

	var selectionNode *yaml.Node
	root := document.Content[0]
	for index := 0; index+1 < len(root.Content); index += 2 {
		key := root.Content[index]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Value != recoverySelectionsConfigKey {
			continue
		}
		if selectionNode != nil {
			return nil, fmt.Errorf("%s must not appear more than once", recoverySelectionsConfigKey)
		}
		selectionNode = root.Content[index+1]
	}
	if selectionNode == nil {
		return nil, nil
	}
	if selectionNode.Kind != yaml.MappingNode || selectionNode.Tag != "!!map" {
		return nil, fmt.Errorf("%s must be a mapping of group names to targets", recoverySelectionsConfigKey)
	}

	selections := make(map[string]string, len(selectionNode.Content)/2)
	for index := 0; index+1 < len(selectionNode.Content); index += 2 {
		group := selectionNode.Content[index]
		target := selectionNode.Content[index+1]
		if group.Kind != yaml.ScalarNode || group.Tag != "!!str" || group.Value == "" {
			return nil, fmt.Errorf("%s contains an empty or non-string group name", recoverySelectionsConfigKey)
		}
		if target.Kind != yaml.ScalarNode || target.Tag != "!!str" || target.Value == "" {
			return nil, fmt.Errorf("%s target for group %q must be a non-empty string", recoverySelectionsConfigKey, group.Value)
		}
		if _, exists := selections[group.Value]; exists {
			return nil, fmt.Errorf("%s contains duplicate group %q", recoverySelectionsConfigKey, group.Value)
		}
		selections[group.Value] = target.Value
	}
	return selections, nil
}
