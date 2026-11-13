package subscription

import (
	"fmt"
	"net"
	"strings"

	"github.com/metacubex/mihomo/common/convert"
	"gopkg.in/yaml.v3"
)

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

// ConvertShareLinksPayload converts raw or base64-encoded V2Ray/VLESS/SS/Trojan/Hysteria2/TUIC
// share links directly in-process into a standard Mihomo YAML profile, eliminating any dependency
// on custom CLI flags in mihomo_core.exe.
func ConvertShareLinksPayload(raw []byte) ([]byte, error) {
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
