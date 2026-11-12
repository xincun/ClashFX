package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/metacubex/mihomo/common/convert"
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
	convertShareLinks := flags.Bool("convert-share-links", false, "convert share-link subscription from stdin to ClashFX YAML")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	if *showFeatures {
		if err := json.NewEncoder(stdout).Encode(map[string]any{"features": features.Tags()}); err != nil {
			return 1
		}
		return 0
	}

	if *convertShareLinks {
		raw, err := io.ReadAll(stdin)
		if err != nil {
			fmt.Fprintf(stderr, "Read share links error: %s\n", err)
			return 1
		}
		rendered, err := convertShareLinksPayload(raw)
		if err != nil {
			fmt.Fprintf(stderr, "Convert share links error: %s\n", err)
			return 1
		}
		_, _ = stdout.Write(rendered)
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

func isSubscriptionInfoProxyName(name string) bool {
	for _, marker := range []string{"剩余流量", "套餐到期", "过滤掉", "官网", "订阅", "用户群"} {
		if strings.Contains(name, marker) {
			return true
		}
	}
	lower := strings.ToLower(strings.TrimSpace(name))
	for _, prefix := range []string{"traffic", "expire", "expired", "remaining traffic", "subscription"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func convertShareLinksPayload(raw []byte) ([]byte, error) {
	proxies, err := convert.ConvertsV2Ray(raw)
	if err != nil {
		return nil, err
	}
	filtered := make([]map[string]any, 0, len(proxies))
	names := make([]string, 0, len(proxies))
	policy := make(map[string]any)
	for _, proxy := range proxies {
		name, _ := proxy["name"].(string)
		if name == "" || isSubscriptionInfoProxyName(name) {
			continue
		}
		filtered = append(filtered, proxy)
		names = append(names, name)
		if server, ok := proxy["server"].(string); ok && server != "" && net.ParseIP(server) == nil {
			policy[server] = "https://223.5.5.5/dns-query"
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("converted subscription did not contain proxy names")
	}
	const benchmarkURL = "https://cp.cloudflare.com/generate_204"
	dns := map[string]any{
		"enable":             true,
		"listen":             "127.0.0.1:1053",
		"ipv6":               true,
		"enhanced-mode":      "redir-host",
		"default-nameserver": []string{"114.114.114.114", "223.5.5.5", "119.29.29.29"},
		"nameserver":         []string{"https://223.5.5.5/dns-query", "https://doh.pub/dns-query", "119.29.29.29", "223.5.5.5", "tls://223.5.5.5:853", "tls://223.6.6.6:853"},
		"fallback":           []string{"https://223.5.5.5/dns-query", "https://doh.pub/dns-query", "tls://1.1.1.1:853", "tls://8.8.8.8:853"},
		"fallback-filter":    map[string]any{"geoip": false},
	}
	if len(policy) > 0 {
		dns["nameserver-policy"] = policy
	}
	doc := map[string]any{
		"mode":                "rule",
		"log-level":           "info",
		"mixed-port":          7890,
		"allow-lan":           false,
		"bind-address":        "*",
		"ipv6":                true,
		"udp":                 true,
		"unified-delay":       true,
		"cfw-latency-timeout": 8000,
		"cfw-latency-url":     benchmarkURL,
		"dns":                 dns,
		"proxies":             filtered,
		"proxy-groups": []map[string]any{
			{"name": "Proxy", "type": "select", "proxies": append([]string{"Auto", "DIRECT"}, names...)},
			{"name": "Auto", "type": "url-test", "proxies": names, "url": benchmarkURL, "interval": 300, "tolerance": 200},
		},
		"rules": []string{
			"DOMAIN,localhost,DIRECT",
			"DOMAIN-SUFFIX,local,DIRECT",
			"DOMAIN-SUFFIX,cn,DIRECT",
			"IP-CIDR,127.0.0.0/8,DIRECT,no-resolve",
			"IP-CIDR,10.0.0.0/8,DIRECT,no-resolve",
			"IP-CIDR,172.16.0.0/12,DIRECT,no-resolve",
			"IP-CIDR,192.168.0.0/16,DIRECT,no-resolve",
			"IP-CIDR,100.64.0.0/10,DIRECT,no-resolve",
			"IP-CIDR,169.254.0.0/16,DIRECT,no-resolve",
			"IP-CIDR6,::1/128,DIRECT,no-resolve",
			"IP-CIDR6,fc00::/7,DIRECT,no-resolve",
			"IP-CIDR6,fe80::/10,DIRECT,no-resolve",
			"MATCH,Proxy",
		},
	}
	body, err := yaml.Marshal(doc)
	if err != nil {
		return nil, err
	}
	header := "# clashfx-generated: share-links\n# clashfx-template-version: 8\n"
	return append([]byte(header), body...), nil
}
