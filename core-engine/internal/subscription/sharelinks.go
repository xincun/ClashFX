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
	seenNames := make(map[string]int, len(proxies))
	policy := make(map[string]any)
	for _, proxy := range proxies {
		rawName, _ := proxy["name"].(string)
		baseName := strings.TrimSpace(rawName)
		if baseName == "" || isSubscriptionInfoProxyName(baseName) {
			continue
		}
		name := baseName
		if seenNames[name] > 0 {
			for suffix := seenNames[name] + 1; ; suffix++ {
				cand := fmt.Sprintf("%s (%d)", baseName, suffix)
				if seenNames[cand] == 0 {
					seenNames[baseName] = suffix
					name = cand
					break
				}
			}
		}
		seenNames[name] = 1
		proxy["name"] = name
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
			"GEOSITE,cn,DIRECT",
			"GEOIP,LAN,DIRECT,no-resolve",
			"GEOIP,CN,DIRECT,no-resolve",
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
