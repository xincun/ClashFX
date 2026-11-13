package features

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"

	"github.com/clashfx/core-engine/internal/profile"
)

type SingleNodeProbeReport struct {
	NodeName     string `json:"node_name"`
	ProviderName string `json:"provider_name,omitempty"`
	BenchmarkURL string `json:"benchmark_url"`
	Healthy      bool   `json:"healthy"`
	DelayMs      int    `json:"delay_ms,omitempty"`
	Kind         string `json:"kind,omitempty"`
	Code         string `json:"code,omitempty"`
	RawMessage   string `json:"raw_message,omitempty"`
	SummaryZH    string `json:"summary_zh"`
	SummaryEN    string `json:"summary_en"`
	AdviceZH     string `json:"advice_zh,omitempty"`
	AdviceEN     string `json:"advice_en,omitempty"`
	Formatted    string `json:"formatted"`
}

// ProbeSingleNode executes a non-intrusive single-node benchmark probe against
// stock Mihomo's delay endpoint (without changing any proxy group selection)
// and classifies the returned error message into actionable guidance.
func ProbeSingleNode(
	ctx context.Context,
	apiPort int,
	secret string,
	nodeName string,
	providerName string,
	benchmarkURL string,
	timeoutMs int,
	lang string,
) (SingleNodeProbeReport, error) {
	nodeName = strings.TrimSpace(nodeName)
	if nodeName == "" {
		return SingleNodeProbeReport{}, fmt.Errorf("node_name is required")
	}
	benchmarkURL = strings.TrimSpace(benchmarkURL)
	if benchmarkURL == "" {
		benchmarkURL = "https://cp.cloudflare.com/generate_204"
	}
	parsed, err := url.Parse(benchmarkURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return SingleNodeProbeReport{}, fmt.Errorf("benchmark_url must be a valid http/https URL")
	}
	if timeoutMs <= 0 {
		timeoutMs = 5000
	}

	q := url.Values{}
	q.Set("url", benchmarkURL)
	q.Set("timeout", fmt.Sprintf("%d", timeoutMs))

	var endpoint string
	if strings.TrimSpace(providerName) != "" {
		endpoint = fmt.Sprintf(
			"http://127.0.0.1:%d/providers/proxies/%s/%s/delay?%s",
			apiPort,
			url.PathEscape(strings.TrimSpace(providerName)),
			url.PathEscape(nodeName),
			q.Encode(),
		)
	} else {
		endpoint = fmt.Sprintf(
			"http://127.0.0.1:%d/proxies/%s/delay?%s",
			apiPort,
			url.PathEscape(nodeName),
			q.Encode(),
		)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return SingleNodeProbeReport{}, err
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}

	client := &http.Client{Timeout: time.Duration(timeoutMs+2500) * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return SingleNodeProbeReport{}, fmt.Errorf("request core delay endpoint: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))

	report := SingleNodeProbeReport{
		NodeName:     nodeName,
		ProviderName: providerName,
		BenchmarkURL: sanitizeDiagnosticURL(benchmarkURL),
	}

	if resp.StatusCode == http.StatusOK {
		var okPayload struct {
			Delay int `json:"delay"`
		}
		if json.Unmarshal(body, &okPayload) == nil && okPayload.Delay > 0 {
			report.Healthy = true
			report.DelayMs = okPayload.Delay
			report.Kind = "ok"
			report.Code = "ok"
			report.SummaryZH = fmt.Sprintf("连通正常（%d ms）", okPayload.Delay)
			report.SummaryEN = fmt.Sprintf("Reachable (%d ms)", okPayload.Delay)
			report.Formatted = formatSingleNodeReport(report, lang)
			return report, nil
		}
	}

	var failPayload struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &failPayload)
	rawMsg := strings.TrimSpace(failPayload.Message)
	if rawMsg == "" {
		rawMsg = strings.TrimSpace(string(body))
	}
	report.RawMessage = redactLogLine(rawMsg)
	report.Kind, report.Code = classifyStockMihomoError(resp.StatusCode, rawMsg)

	report.SummaryZH, report.AdviceZH, report.SummaryEN, report.AdviceEN = explainProbeCode(report.Code)
	report.Formatted = formatSingleNodeReport(report, lang)
	return report, nil
}

func classifyStockMihomoError(statusCode int, msg string) (kind, code string) {
	lower := strings.ToLower(strings.TrimSpace(msg))
	switch {
	case strings.Contains(lower, "x509: certificate is valid for") || strings.Contains(lower, "hostname") || strings.Contains(lower, "doesn't match"):
		return "tls", "tls_name_mismatch"
	case strings.Contains(lower, "x509: certificate signed by unknown authority") || strings.Contains(lower, "unknown authority"):
		return "tls", "tls_unknown_authority"
	case strings.Contains(lower, "x509: certificate has expired") || strings.Contains(lower, "not yet valid"):
		return "tls", "tls_expired"
	case strings.Contains(lower, "tls") || strings.Contains(lower, "x509") || strings.Contains(lower, "remote error:"):
		return "tls", "tls_failure"
	case strings.Contains(lower, "no such host") || strings.Contains(lower, "nxdomain") || strings.Contains(lower, "server misbehaving") || strings.Contains(lower, "dns"):
		if strings.Contains(lower, "timeout") || strings.Contains(lower, "i/o timeout") {
			return "dns", "dns_timeout"
		}
		return "dns", "dns_resolution_failed"
	case strings.Contains(lower, "quic") || strings.Contains(lower, "hysteria") || strings.Contains(lower, "tuic"):
		return "quic", "quic_handshake_failed"
	case strings.Contains(lower, "auth") || strings.Contains(lower, "password") || strings.Contains(lower, "407") || strings.Contains(lower, "invalid user"):
		return "auth", "authentication_failed"
	case strings.Contains(lower, "connection refused") || strings.Contains(lower, "actively refused") || strings.Contains(lower, "network is unreachable") || strings.Contains(lower, "no route to host"):
		return "network", "dial_failed"
	case strings.Contains(lower, "connection reset") || strings.Contains(lower, "forcibly closed") || strings.Contains(lower, "unexpected eof") || strings.Contains(lower, "eof"):
		return "http", "http_request_failed"
	case statusCode == http.StatusGatewayTimeout || strings.Contains(lower, "timeout") || strings.Contains(lower, "deadline exceeded"):
		return "timeout", "probe_timeout"
	default:
		return "probe", "probe_failed"
	}
}

func explainProbeCode(code string) (sumZH, advZH, sumEN, advEN string) {
	switch code {
	case "dns_timeout":
		return "DNS 解析超时", "检查节点远端或本地上游 DNS 是否可达，或切换增强模式 Fake-IP。",
			"DNS resolution timed out", "Check upstream DNS reachability or enable Enhanced Mode Fake-IP."
	case "dns_resolution_failed":
		return "DNS 域名解析失败", "节点服务器域名可能已失效或当前网络无法解析该节点域名。",
			"DNS resolution failed", "The proxy server domain could not be resolved on the current network."
	case "tls_unknown_authority":
		return "TLS 证书颁发机构不受信任", "检查系统根证书、是否存在企业防火墙劫持，或节点 SNI 配置是否正确。",
			"Untrusted TLS certificate authority", "Verify SNI configuration or check for TLS middlebox interception."
	case "tls_expired":
		return "TLS 证书已过期或系统时间偏差", "检查本机时间是否同步，或联系订阅服务商更新过期证书。",
			"TLS certificate expired or clock skew", "Verify system clock synchronization or contact provider to renew certificate."
	case "tls_name_mismatch":
		return "TLS 证书域名不匹配（SNI 错误）", "节点的 servername / sni 字段与远端返回的证书不匹配。",
			"TLS hostname mismatch (SNI)", "The configured servername/sni does not match the server certificate."
	case "tls_failure":
		return "TLS 握手失败", "远端端口可能阻断了当前 TLS 指纹/ALPN，建议刷新订阅或更换节点。",
			"TLS handshake failed", "TLS negotiation failed; try refreshing subscription or switching nodes."
	case "quic_handshake_failed":
		return "QUIC / UDP 握手失败（Hysteria2 / TUIC）", "当前运营商或路由器可能限流/阻断了 UDP 443，建议改用 TCP 类节点。",
			"QUIC / UDP handshake failed", "UDP may be throttled or blocked on this network; try a TCP-based node."
	case "authentication_failed":
		return "节点鉴权失败（密码 / UUID 错误）", "节点密码或凭据可能已轮换，请立即执行直连刷新订阅。",
			"Proxy authentication failed", "Credentials may have rotated; refresh your subscription."
	case "dial_failed":
		return "TCP 建连失败（连接被拒绝或路由不可达）", "远端节点端口未开放或 IP 已不可达。",
			"TCP dial failed (refused or unreachable)", "Remote server port is closed or unreachable."
	case "http_request_failed":
		return "隧道建立后 HTTP 测速请求失败", "出口节点到测速目标网站之间的连接被重置或失败。",
			"HTTP benchmark request failed over tunnel", "Tunnel connected, but upstream HTTP request to benchmark target failed."
	case "probe_timeout":
		return "测速整体超时", "节点高丢包或无响应，建议切换其他可用节点。",
			"Benchmark probe timed out", "Node timed out before completing handshake/response."
	case "zero_delay":
		return "返回了无效的 0ms 延迟", "内核未获得有效往返时间样本，请重试。",
			"Invalid zero-millisecond measurement", "Core returned zero RTT; retry probe."
	default:
		return "节点探测失败", "建议刷新订阅并检查内核日志获取更多上下文。",
			"Node probe failed", "Refresh subscription or inspect logs for details."
	}
}

func formatSingleNodeReport(r SingleNodeProbeReport, lang string) string {
	isEN := strings.HasPrefix(strings.ToLower(strings.TrimSpace(lang)), "en")
	var b strings.Builder
	if isEN {
		fmt.Fprintf(&b, "Node: %s\n", r.NodeName)
		if r.ProviderName != "" {
			fmt.Fprintf(&b, "Provider: %s\n", r.ProviderName)
		}
		fmt.Fprintf(&b, "Benchmark URL: %s\n", r.BenchmarkURL)
		fmt.Fprintf(&b, "Result: %s\n", r.SummaryEN)
		if !r.Healthy {
			fmt.Fprintf(&b, "Classification: %s / %s\n", r.Kind, r.Code)
			if r.RawMessage != "" {
				fmt.Fprintf(&b, "Core Detail: %s\n", r.RawMessage)
			}
			if r.AdviceEN != "" {
				fmt.Fprintf(&b, "Suggestion: %s\n", r.AdviceEN)
			}
		}
	} else {
		fmt.Fprintf(&b, "节点名称: %s\n", r.NodeName)
		if r.ProviderName != "" {
			fmt.Fprintf(&b, "所属 Provider: %s\n", r.ProviderName)
		}
		fmt.Fprintf(&b, "测速地址: %s\n", r.BenchmarkURL)
		fmt.Fprintf(&b, "诊断结论: %s\n", r.SummaryZH)
		if !r.Healthy {
			fmt.Fprintf(&b, "错误分类: %s / %s\n", r.Kind, r.Code)
			if r.RawMessage != "" {
				fmt.Fprintf(&b, "内核返回: %s\n", r.RawMessage)
			}
			if r.AdviceZH != "" {
				fmt.Fprintf(&b, "排查建议: %s\n", r.AdviceZH)
			}
		}
	}
	return strings.TrimSpace(b.String())
}

func sanitizeDiagnosticURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "[invalid-url]"
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

type DiagnosticBundleInput struct {
	AppVersion       string
	CoreRunning      bool
	CoreViaService   bool
	ServiceInstalled bool
	MixedPort        int
	APIPort          int
	DNSPort          int
	Settings         profile.AppSettings
	SysProxyServer   string
	SysProxyEnabled  bool
	SuspendedNet     string
	RecentCoreLogs   []string
}

// BuildRedactedDiagnosticReport creates a safe clipboard report with zero secrets,
// subscription URLs, credentials, or raw proxy server addresses.
func BuildRedactedDiagnosticReport(in DiagnosticBundleInput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "=== ClashFX Windows Diagnostic Report ===\n")
	fmt.Fprintf(&b, "Timestamp: %s\n", time.Now().Format(time.RFC3339))
	fmt.Fprintf(&b, "Version: %s (%s/%s)\n", in.AppVersion, runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(&b, "Core Running: %v (via_service=%v, service_installed=%v)\n", in.CoreRunning, in.CoreViaService, in.ServiceInstalled)
	fmt.Fprintf(&b, "Selected Profile: %s (custom_dir=%v)\n", in.Settings.SelectedProfile, strings.TrimSpace(in.Settings.CustomConfigDir) != "")
	fmt.Fprintf(&b, "Outbound Mode: %s | Enhanced TUN: %v (stack=%s, mtu=%d, as_is=%v, bypass_cn=%v)\n",
		in.Settings.OutboundMode, in.Settings.EnhancedMode, in.Settings.TunStack, in.Settings.TunMTU,
		in.Settings.CustomConfigAsIs, in.Settings.BypassCNApps)
	fmt.Fprintf(&b, "Ports: mixed=%d api=%d dns=%d (api_secret_set=%v)\n",
		in.MixedPort, in.APIPort, in.DNSPort, in.Settings.APISecret != "")
	fmt.Fprintf(&b, "System Proxy: want=%v os_enabled=%v os_server=%s suspended_by_net=%q\n",
		in.Settings.SystemProxyEnabled, in.SysProxyEnabled, in.SysProxyServer, in.SuspendedNet)
	fmt.Fprintf(&b, "AI Proxy Lock: enabled=%v target_configured=%v\n",
		in.Settings.AILockEnabled, strings.TrimSpace(in.Settings.AILockTarget) != "")
	if len(in.RecentCoreLogs) > 0 {
		fmt.Fprintf(&b, "--- Recent Core Logs (Redacted, last %d lines) ---\n", len(in.RecentCoreLogs))
		for _, line := range in.RecentCoreLogs {
			fmt.Fprintf(&b, "%s\n", redactLogLine(line))
		}
	}
	return strings.TrimSpace(b.String())
}

func redactLogLine(line string) string {
	// Strip query strings or Bearer tokens if any appear in logs
	if idx := strings.Index(strings.ToLower(line), "bearer "); idx >= 0 {
		return line[:idx] + "Bearer [REDACTED]"
	}
	if idx := strings.Index(line, "?"); idx >= 0 && strings.Contains(line, "http") {
		return line[:idx] + "?[REDACTED]"
	}
	return line
}
