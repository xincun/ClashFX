package subscription

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/clashfx/core-engine/internal/profile"
)

const DefaultSubscriptionUserAgent = "clash.meta/v1.19.24"

type UserInfo struct {
	Upload     *int64 `json:"upload,omitempty"`
	Download   *int64 `json:"download,omitempty"`
	Total      *int64 `json:"total,omitempty"`
	Expire     *int64 `json:"expire,omitempty"`
	ExpireText string `json:"expire_text,omitempty"`
}

func (u UserInfo) HasAny() bool {
	return u.Upload != nil || u.Download != nil || u.Total != nil || u.Expire != nil || strings.TrimSpace(u.ExpireText) != ""
}

type ManagedSubscription struct {
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	UserAgent string    `json:"user_agent,omitempty"`
	UpdatedAt *int64    `json:"updated_at,omitempty"`
	UserInfo  *UserInfo `json:"userinfo,omitempty"`
}

type CoreHelper interface {
	ConvertShareLinks(raw []byte) ([]byte, error)
	ValidateConfig(homeDir, configPath string) error
}

type BinaryCoreHelper struct {
	CoreBinaryPath string
	SafePaths      []string
}

func appendSafePathsEnv(baseEnv []string, extraPaths ...string) []string {
	var cleaned []string
	if existing := strings.TrimSpace(os.Getenv("SAFE_PATHS")); existing != "" {
		cleaned = append(cleaned, existing)
	}
	for _, p := range extraPaths {
		if t := strings.TrimSpace(p); t != "" {
			cleaned = append(cleaned, filepath.Clean(t))
		}
	}
	if len(cleaned) == 0 {
		return baseEnv
	}
	if len(baseEnv) == 0 {
		baseEnv = os.Environ()
	}
	out := make([]string, 0, len(baseEnv)+1)
	for _, kv := range baseEnv {
		if strings.HasPrefix(strings.ToUpper(kv), "SAFE_PATHS=") {
			continue
		}
		out = append(out, kv)
	}
	out = append(out, "SAFE_PATHS="+strings.Join(cleaned, string(os.PathListSeparator)))
	return out
}

func (b BinaryCoreHelper) ConvertShareLinks(raw []byte) ([]byte, error) {
	return ConvertShareLinksPayload(raw)
}

func (b BinaryCoreHelper) ValidateConfig(homeDir, configPath string) error {
	cmd := exec.Command(b.CoreBinaryPath, "-t", "-d", homeDir, "-f", configPath)
	configureChildProcess(cmd)
	if len(b.SafePaths) > 0 {
		cmd.Env = appendSafePathsEnv(os.Environ(), b.SafePaths...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		return fmt.Errorf("validate config (%v): %s", err, msg)
	}
	return nil
}

type Manager struct {
	mu          sync.Mutex
	store       *profile.Store
	coreHelper  CoreHelper
	subs        []ManagedSubscription
	updatingSet map[string]bool
}

func NewManager(store *profile.Store, helper CoreHelper) (*Manager, error) {
	m := &Manager{
		store:       store,
		coreHelper:  helper,
		updatingSet: map[string]bool{},
	}
	if err := m.loadLocked(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) List() []ManagedSubscription {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ManagedSubscription, len(m.subs))
	copy(out, m.subs)
	return out
}

func (m *Manager) FindByName(name string) (ManagedSubscription, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, sub := range m.subs {
		if sub.Name == name {
			return sub, true
		}
	}
	return ManagedSubscription{}, false
}

func (m *Manager) AddOrUpdate(name, rawURL, userAgent string) (ManagedSubscription, error) {
	rawURL = strings.TrimSpace(rawURL)
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return ManagedSubscription{}, fmt.Errorf("invalid subscription URL %q", rawURL)
	}
	name = sanitizeProfileName(name)
	if name == "" {
		name = sanitizeProfileName(parsed.Hostname())
	}
	if name == "" {
		name = "subscription"
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.subs {
		if m.subs[i].Name == name {
			m.subs[i].URL = rawURL
			m.subs[i].UserAgent = strings.TrimSpace(userAgent)
			if err := m.saveLocked(); err != nil {
				return ManagedSubscription{}, err
			}
			return m.subs[i], nil
		}
	}
	entry := ManagedSubscription{
		Name:      name,
		URL:       rawURL,
		UserAgent: strings.TrimSpace(userAgent),
	}
	m.subs = append(m.subs, entry)
	if err := m.saveLocked(); err != nil {
		return ManagedSubscription{}, err
	}
	return entry, nil
}

func (m *Manager) Remove(name string, deleteFile bool) error {
	m.mu.Lock()
	idx := -1
	for i, s := range m.subs {
		if s.Name == name {
			idx = i
			break
		}
	}
	if idx >= 0 {
		m.subs = append(m.subs[:idx], m.subs[idx+1:]...)
		if err := m.saveLocked(); err != nil {
			m.mu.Unlock()
			return err
		}
	}
	m.mu.Unlock()
	if deleteFile {
		_ = os.Remove(m.store.ProfilePath(name))
	}
	return nil
}

// Refresh downloads a managed subscription, converts share links if necessary,
// validates the resulting YAML, and atomically writes the profile file.
// When directProxyPort > 0, traffic is pinned through 127.0.0.1:<directProxyPort>
// (ClashFX's internal DIRECT listener) and never falls back to system proxy.
func (m *Manager) Refresh(ctx context.Context, name string, directProxyPort int) (ManagedSubscription, error) {
	m.mu.Lock()
	var target *ManagedSubscription
	for i := range m.subs {
		if m.subs[i].Name == name {
			target = &m.subs[i]
			break
		}
	}
	if target == nil {
		m.mu.Unlock()
		return ManagedSubscription{}, fmt.Errorf("managed subscription %q not found", name)
	}
	if m.updatingSet[name] {
		m.mu.Unlock()
		return ManagedSubscription{}, fmt.Errorf("subscription %q is already updating", name)
	}
	subCopy := *target
	m.updatingSet[name] = true
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		delete(m.updatingSet, name)
		m.mu.Unlock()
	}()

	client, err := newSubscriptionHTTPClient(directProxyPort, 60*time.Second)
	if err != nil {
		return ManagedSubscription{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, subCopy.URL, nil)
	if err != nil {
		return ManagedSubscription{}, err
	}
	ua := strings.TrimSpace(subCopy.UserAgent)
	if ua == "" {
		ua = DefaultSubscriptionUserAgent
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Cache-Control", "no-cache")

	resp, err := client.Do(req)
	if err != nil {
		return ManagedSubscription{}, fmt.Errorf("download subscription %q: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ManagedSubscription{}, fmt.Errorf("download subscription %q returned HTTP %d", name, resp.StatusCode)
	}

	rawBody, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024*1024))
	if err != nil {
		return ManagedSubscription{}, fmt.Errorf("read subscription %q: %w", name, err)
	}
	if len(bytes.TrimSpace(rawBody)) == 0 {
		return ManagedSubscription{}, fmt.Errorf("subscription %q returned empty body", name)
	}

	yamlBytes, err := m.normalizeSubscriptionContent(rawBody)
	if err != nil {
		return ManagedSubscription{}, err
	}

	cfgDir := m.store.ActiveConfigDir()
	tmpFile, err := os.CreateTemp(cfgDir, ".sub-validate-*.yaml")
	if err != nil {
		return ManagedSubscription{}, err
	}
	tmpPath := tmpFile.Name()
	_ = tmpFile.Close()
	defer os.Remove(tmpPath)

	if err := os.WriteFile(tmpPath, yamlBytes, 0600); err != nil {
		return ManagedSubscription{}, err
	}
	if m.coreHelper != nil {
		if err := m.coreHelper.ValidateConfig(cfgDir, tmpPath); err != nil {
			return ManagedSubscription{}, err
		}
	}

	finalName := subCopy.Name
	if suggested := suggestedProfileName(resp.Header.Get("Content-Disposition")); suggested != "" && subCopy.UpdatedAt == nil {
		// On initial import, adopt server-suggested name if non-colliding.
		if _, exists := m.FindByName(suggested); !exists {
			finalName = suggested
		}
	}

	destPath := m.store.ProfilePath(finalName)
	m.store.MarkInternalWrite(destPath)
	if err := profile.WriteFileAtomic(destPath, yamlBytes, 0600); err != nil {
		return ManagedSubscription{}, err
	}
	if finalName != subCopy.Name {
		_ = m.store.RenameProfileReferences(subCopy.Name, finalName)
	}

	now := time.Now().Unix()
	headerInfo := ParseUserinfoHeader(resp.Header.Get("Subscription-Userinfo"))
	bodyInfo := ParseUserinfoBody(rawBody)
	info := MergeUserInfo(headerInfo, bodyInfo)

	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.subs {
		if m.subs[i].Name == subCopy.Name {
			m.subs[i].Name = finalName
			m.subs[i].UpdatedAt = &now
			if info != nil {
				m.subs[i].UserInfo = info
			}
			_ = m.saveLocked()
			return m.subs[i], nil
		}
	}
	return subCopy, nil
}

func (m *Manager) UpdateExternalResource(ctx context.Context, rawURL, destFileName string, minBytes int) error {
	return m.UpdateExternalResourceViaProxy(ctx, rawURL, destFileName, minBytes, 0)
}

func (m *Manager) UpdateExternalResourceViaProxy(ctx context.Context, rawURL, destFileName string, minBytes int, localProxyPort int) error {
	data, err := m.DownloadExternalResourceViaProxy(ctx, rawURL, destFileName, minBytes, localProxyPort)
	if err != nil {
		return err
	}
	dst := filepath.Join(m.store.ActiveConfigDir(), destFileName)
	return profile.WriteFileAtomic(dst, data, 0644)
}

func (m *Manager) DownloadExternalResourceViaProxy(ctx context.Context, rawURL, destFileName string, minBytes int, localProxyPort int) ([]byte, error) {
	downloadOnce := func(proxyPort int) ([]byte, error) {
		client, err := newSubscriptionHTTPClient(proxyPort, 90*time.Second)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSpace(rawURL), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", DefaultSubscriptionUserAgent)
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("HTTP %d downloading %s", resp.StatusCode, destFileName)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 128*1024*1024))
		if err != nil {
			return nil, err
		}
		if len(data) < minBytes {
			return nil, fmt.Errorf("downloaded %s too small (%d bytes)", destFileName, len(data))
		}
		return data, nil
	}

	var (
		data []byte
		err  error
	)
	if localProxyPort > 0 {
		data, err = downloadOnce(localProxyPort)
	}
	if len(data) == 0 {
		if directData, dErr := downloadOnce(0); dErr == nil {
			data = directData
			err = nil
		} else if err == nil {
			err = dErr
		}
	}
	if err != nil {
		return nil, err
	}
	return data, nil
}

func (m *Manager) normalizeSubscriptionContent(raw []byte) ([]byte, error) {
	doc, err := profile.UnmarshalYAMLMap(raw)
	if err == nil && (doc["proxies"] != nil || doc["proxy-providers"] != nil || doc["proxy-groups"] != nil) {
		return raw, nil
	}
	// Check if raw is share links or base64-encoded share links
	if looksLikeShareLinksOrBase64(raw) {
		if m.coreHelper != nil {
			return m.coreHelper.ConvertShareLinks(raw)
		}
		return ConvertShareLinksPayload(raw)
	}
	return nil, fmt.Errorf("subscription response is neither a valid Clash YAML profile nor supported share links")
}

func looksLikeShareLinksOrBase64(raw []byte) bool {
	text := strings.TrimSpace(string(raw))
	schemes := []string{
		"ss://", "ssr://", "vmess://", "vless://", "trojan://",
		"hysteria://", "hysteria2://", "hy2://", "tuic://",
		"socks://", "socks5://", "socks5h://", "anytls://", "mierus://",
	}
	for _, s := range schemes {
		if strings.Contains(strings.ToLower(text), s) {
			return true
		}
	}
	compact := strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, text)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if decoded, err := enc.DecodeString(compact); err == nil && len(decoded) > 0 {
			decLower := strings.ToLower(string(decoded))
			for _, s := range schemes {
				if strings.Contains(decLower, s) {
					return true
				}
			}
		}
	}
	return false
}

func ParseUserinfoHeader(raw string) *UserInfo {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var info UserInfo
	for _, part := range strings.Split(raw, ";") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(kv[0]))
		valStr := strings.TrimSpace(kv[1])
		switch key {
		case "upload":
			if v, err := strconv.ParseInt(valStr, 10, 64); err == nil {
				info.Upload = &v
			}
		case "download":
			if v, err := strconv.ParseInt(valStr, 10, 64); err == nil {
				info.Download = &v
			}
		case "total":
			if v, err := strconv.ParseInt(valStr, 10, 64); err == nil {
				info.Total = &v
			}
		case "expire":
			if f, err := strconv.ParseFloat(valStr, 64); err == nil && f > 0 {
				sec := int64(f)
				info.Expire = &sec
			}
		}
	}
	if !info.HasAny() {
		return nil
	}
	return &info
}

func MergeUserInfo(primary, fallback *UserInfo) *UserInfo {
	if primary == nil && fallback == nil {
		return nil
	}
	var out UserInfo
	if primary != nil {
		out = *primary
	}
	if fallback != nil {
		if out.Upload == nil && out.Download == nil {
			out.Upload = fallback.Upload
			out.Download = fallback.Download
		}
		if out.Total == nil {
			out.Total = fallback.Total
		}
		if out.Expire == nil {
			out.Expire = fallback.Expire
		}
		if strings.TrimSpace(out.ExpireText) == "" {
			out.ExpireText = fallback.ExpireText
		}
	}
	if !out.HasAny() {
		return nil
	}
	return &out
}

func ParseUserinfoBody(raw []byte) *UserInfo {
	text := string(raw)
	compact := strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, strings.TrimSpace(text))
	if compact != "" && !strings.Contains(text, "proxies:") {
		for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
			if dec, err := enc.DecodeString(compact); err == nil && len(dec) > 0 {
				text = string(dec)
				break
			}
		}
	}

	var info UserInfo
	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		if decoded, err := url.QueryUnescape(line); err == nil && decoded != "" {
			line = decoded
		}
		if total, ok := matchByteValueAfter(line, []string{"总流量", "Total Traffic", "TOT", "Total"}); ok {
			info.Total = &total
		}
		if used, ok := matchByteValueAfter(line, []string{"已用流量", "已使用", "Used"}); ok && info.Upload == nil && info.Download == nil {
			info.Download = &used
		}
		if rem, ok := matchByteValueAfter(line, []string{"剩余流量", "Remaining Traffic", "Remaining"}); ok {
			if info.Total != nil && info.Upload == nil && info.Download == nil {
				used := *info.Total - rem
				if used < 0 {
					used = 0
				}
				info.Download = &used
			}
		}
		if exp := matchExpireLabel(line); exp != "" {
			info.ExpireText = exp
		}
	}
	if !info.HasAny() {
		return nil
	}
	return &info
}

func matchByteValueAfter(line string, labels []string) (int64, bool) {
	type unitMult struct {
		suffix string
		mult   float64
	}
	units := []unitMult{
		{"PB", 1125899906842624},
		{"TB", 1099511627776},
		{"GB", 1073741824},
		{"MB", 1048576},
		{"KB", 1024},
		{"B", 1},
	}
	lowerLine := strings.ToLower(line)
	for _, lbl := range labels {
		idx := strings.Index(lowerLine, strings.ToLower(lbl))
		if idx < 0 {
			continue
		}
		tail := strings.TrimLeft(line[idx+len(lbl):], " ：:=|/-\t")
		numEnd := 0
		hasDot := false
		for numEnd < len(tail) {
			ch := tail[numEnd]
			if ch >= '0' && ch <= '9' {
				numEnd++
			} else if ch == '.' && !hasDot {
				hasDot = true
				numEnd++
			} else {
				break
			}
		}
		if numEnd == 0 {
			continue
		}
		val, err := strconv.ParseFloat(tail[:numEnd], 64)
		if err != nil {
			continue
		}
		unitTail := strings.ToUpper(strings.TrimSpace(tail[numEnd:]))
		for _, u := range units {
			if strings.HasPrefix(unitTail, u.suffix) {
				return int64(val * u.mult), true
			}
		}
		return int64(val), true
	}
	return 0, false
}

func matchExpireLabel(line string) string {
	lowerLine := strings.ToLower(line)
	for _, lbl := range []string{"套餐到期", "到期时间", "Expires", "Expiry", "Expire"} {
		idx := strings.Index(lowerLine, strings.ToLower(lbl))
		if idx < 0 {
			continue
		}
		tail := strings.TrimSpace(strings.TrimLeft(line[idx+len(lbl):], " ：:=|/-\t"))
		if tail != "" {
			if cut := strings.IndexAny(tail, ";|,"); cut > 0 {
				tail = strings.TrimSpace(tail[:cut])
			}
			return tail
		}
	}
	return ""
}

func newSubscriptionHTTPClient(directProxyPort int, timeout time.Duration) (*http.Client, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if directProxyPort > 0 {
		proxyURL, err := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", directProxyPort))
		if err != nil {
			return nil, err
		}
		// Never fall back to environment/OS proxy when forceDirect is requested.
		tr.Proxy = http.ProxyURL(proxyURL)
	}
	return &http.Client{
		Transport: tr,
		Timeout:   timeout,
	}, nil
}

func suggestedProfileName(contentDisposition string) string {
	if strings.TrimSpace(contentDisposition) == "" {
		return ""
	}
	_, params, err := mime.ParseMediaType(contentDisposition)
	if err != nil {
		return ""
	}
	fn := params["filename"]
	if fn == "" {
		return ""
	}
	fn = strings.TrimSuffix(fn, filepath.Ext(fn))
	return sanitizeProfileName(fn)
}

func sanitizeProfileName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimSuffix(name, ".yaml")
	name = strings.TrimSuffix(name, ".yml")
	replacer := strings.NewReplacer(
		"/", "_", "\\", "_", ":", "_", "*", "_",
		"?", "_", "\"", "_", "<", "_", ">", "_", "|", "_",
	)
	name = strings.Trim(replacer.Replace(name), ". ")
	if profile.IsProfileMixinFileName(name + ".yaml") {
		return ""
	}
	return name
}

func (m *Manager) filePathLocked() string {
	return filepath.Join(m.store.AppDataDir(), "subscriptions.json")
}

func (m *Manager) loadLocked() error {
	data, err := os.ReadFile(m.filePathLocked())
	if os.IsNotExist(err) {
		m.subs = nil
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(data, &m.subs)
}

func (m *Manager) saveLocked() error {
	data, err := json.MarshalIndent(m.subs, "", "  ")
	if err != nil {
		return err
	}
	return profile.WriteFileAtomic(m.filePathLocked(), data, 0600)
}
