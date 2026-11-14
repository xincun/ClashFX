//go:build darwin

package profile

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	legacyDarwinBundleID  = "com.clashfx.app"
	appleRefEpochOffset   = int64(978307200) // Seconds between 1970-01-01 and 2001-01-01 (Apple NSDate referenceDate)
	disableLegacyPlistEnv = "CLASHFX_DISABLE_LEGACY_PLIST_MIGRATION"
)

type legacyUserInfo struct {
	Upload     *int64 `json:"upload,omitempty"`
	Download   *int64 `json:"download,omitempty"`
	Total      *int64 `json:"total,omitempty"`
	Expire     *int64 `json:"expire,omitempty"`
	ExpireText string `json:"expire_text,omitempty"`
}

type legacyManagedSubscription struct {
	Name      string          `json:"name"`
	URL       string          `json:"url"`
	UserAgent string          `json:"user_agent,omitempty"`
	UpdatedAt *int64          `json:"updated_at,omitempty"`
	UserInfo  *legacyUserInfo `json:"userinfo,omitempty"`
}

type legacySwiftSubscriptionInfo struct {
	Upload     *int64   `json:"upload"`
	Download   *int64   `json:"download"`
	Total      *int64   `json:"total"`
	Expire     *float64 `json:"expire"`
	ExpireText string   `json:"expireText"`
}

type legacySwiftRemoteConfig struct {
	URL              string                       `json:"url"`
	Name             string                       `json:"name"`
	UserAgent        string                       `json:"userAgent"`
	UpdateTime       any                          `json:"updateTime"`
	SubscriptionInfo *legacySwiftSubscriptionInfo `json:"subscriptionInfo"`
}

func migrateLegacyDarwinPreferences(appDataDir string, settings *AppSettings) {
	if settings == nil || strings.TrimSpace(os.Getenv(disableLegacyPlistEnv)) == "1" {
		return
	}
	if flag.Lookup("test.v") != nil && strings.TrimSpace(os.Getenv("CLASHFX_TEST_REAL_LEGACY_PLIST")) != "1" {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return
	}
	plistPath := filepath.Join(home, "Library", "Preferences", legacyDarwinBundleID+".plist")
	_ = MigrateLegacyDarwinPlistFile(plistPath, appDataDir, settings)
}

// MigrateLegacyDarwinPlistFile imports legacy ClashFX NSUserDefaults values
// and encoded kRemoteConfigs subscriptions from a binary or XML .plist file.
func MigrateLegacyDarwinPlistFile(plistPath, appDataDir string, settings *AppSettings) error {
	if settings == nil {
		return nil
	}
	if _, err := os.Stat(plistPath); err != nil {
		return err
	}
	cmd := exec.Command("/usr/bin/plutil", "-convert", "xml1", "-o", "-", plistPath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("plutil convert %s: %w (%s)", plistPath, err, strings.TrimSpace(stderr.String()))
	}
	dict, err := parseXMLPlistDict(stdout.Bytes())
	if err != nil {
		return err
	}
	applyLegacyDictToSettings(dict, settings)
	return migrateLegacyRemoteConfigs(dict, appDataDir)
}

func applyLegacyDictToSettings(dict map[string]any, s *AppSettings) {
	if v, ok := dictString(dict, "selectConfigName"); ok {
		s.SelectedProfile = v
	}
	if v, ok := dictString(dict, "selectOutBoundMode"); ok {
		switch strings.ToLower(v) {
		case "rule", "global", "direct":
			s.OutboundMode = strings.ToLower(v)
		}
	}
	if v, ok := dictBool(dict, "enhancedMode"); ok {
		s.EnhancedMode = v
	}
	if v, ok := dictBool(dict, "enhancedModeUseCustomConfig"); ok {
		s.CustomConfigAsIs = v
	}
	if v, ok := dictBool(dict, "bypassChineseApps"); ok {
		s.BypassCNApps = v
	}
	if v, ok := dictBool(dict, "claudeProxyLockEnabled"); ok {
		s.AILockEnabled = v
	}
	if v, ok := dictString(dict, "claudeProxyLockTarget"); ok {
		s.AILockTarget = v
	}
	if v, ok := dictInt(dict, "proxyPort"); ok && v > 0 && v <= 65535 {
		s.PortOverride = v
	}
	if v, ok := dictInt(dict, "apiPort"); ok && v > 0 && v <= 65535 {
		s.APIPort = v
	}
	if v, ok := dictBool(dict, "apiPortAllowLan"); ok {
		s.AllowLan = v
	}
	if v, ok := dictBool(dict, "enableIPV6"); ok {
		s.IPv6 = v
	}
	if v, ok := dictString(dict, "appLanguage"); ok {
		s.Language = NormalizeLanguageCode(v)
	}
	if v, ok := dictString(dict, "selectedMenuIconID"); ok {
		s.TrayIconPreset = NormalizeTrayIconPreset(v)
	}
	if list, ok := dictStringSlice(dict, "disableSSIDList"); ok {
		s.SuspendNetworks = list
	}
	if v, ok := dictInt(dict, "tunMTU"); ok && v >= 1280 && v <= 9000 {
		s.TunMTU = uint32(v)
	}
	if v, ok := dictString(dict, "tunInterfaceName"); ok {
		s.TunInterfaceName = v
	}
	if v, ok := dictString(dict, "tunRouteExcludeRawText"); ok {
		s.TunRouteExcludes = v
	} else if list, ok := dictStringSlice(dict, "tunRouteExcludeList"); ok && len(list) > 0 {
		s.TunRouteExcludes = strings.Join(list, ",")
	}
	if list, ok := dictStringSlice(dict, "proxyIgnoreList"); ok && len(list) > 0 {
		s.ProxyBypassList = strings.Join(list, ",")
	}
	if v, ok := dictString(dict, "benchMarkUrl"); ok {
		s.BenchmarkURL = v
	}
	if v, ok := dictString(dict, "mmdbDownloadUrl"); ok {
		s.MMDBDownloadURL = v
	}
	if v, ok := dictBool(dict, "isLabChannel"); ok {
		if v {
			s.UpdateChannel = "lab"
		} else {
			s.UpdateChannel = "stable"
		}
	}
	if v, ok := dictBool(dict, "autoUpdateEnable"); ok {
		s.AutoUpdateSubs = v
	}
}

func migrateLegacyRemoteConfigs(dict map[string]any, appDataDir string) error {
	if strings.TrimSpace(appDataDir) == "" {
		return nil
	}
	subsPath := filepath.Join(appDataDir, "subscriptions.json")
	if _, err := os.Stat(subsPath); err == nil {
		return nil
	}
	raw, ok := dict["kRemoteConfigs"].([]byte)
	if !ok || len(raw) == 0 {
		return nil
	}
	var legacy []legacySwiftRemoteConfig
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return err
	}
	if len(legacy) == 0 {
		return nil
	}
	out := make([]legacyManagedSubscription, 0, len(legacy))
	for _, item := range legacy {
		name := strings.TrimSpace(item.Name)
		subURL := strings.TrimSpace(item.URL)
		if name == "" || subURL == "" {
			continue
		}
		rec := legacyManagedSubscription{
			Name:      name,
			URL:       subURL,
			UserAgent: strings.TrimSpace(item.UserAgent),
		}
		if secs := parseSwiftDateSeconds(item.UpdateTime); secs != nil {
			rec.UpdatedAt = secs
		}
		if item.SubscriptionInfo != nil {
			ui := &legacyUserInfo{
				Upload:     item.SubscriptionInfo.Upload,
				Download:   item.SubscriptionInfo.Download,
				Total:      item.SubscriptionInfo.Total,
				ExpireText: strings.TrimSpace(item.SubscriptionInfo.ExpireText),
			}
			if item.SubscriptionInfo.Expire != nil {
				exp := int64(*item.SubscriptionInfo.Expire)
				ui.Expire = &exp
			}
			if ui.Upload != nil || ui.Download != nil || ui.Total != nil || ui.Expire != nil || ui.ExpireText != "" {
				rec.UserInfo = ui
			}
		}
		out = append(out, rec)
	}
	if len(out) == 0 {
		return nil
	}
	return writeJSONAtomic(subsPath, out)
}

func parseSwiftDateSeconds(raw any) *int64 {
	switch v := raw.(type) {
	case float64:
		// Swift JSONEncoder default encodes Date as seconds since 2001-01-01 (< 1e9 for modern dates).
		unixSec := int64(v)
		if unixSec > 0 && unixSec < 1_000_000_000 {
			unixSec += appleRefEpochOffset
		}
		if unixSec > 0 {
			return &unixSec
		}
	}
	return nil
}

func dictString(d map[string]any, key string) (string, bool) {
	s, ok := d[key].(string)
	if !ok {
		return "", false
	}
	s = strings.TrimSpace(s)
	return s, s != ""
}

func dictBool(d map[string]any, key string) (bool, bool) {
	b, ok := d[key].(bool)
	return b, ok
}

func dictInt(d map[string]any, key string) (int, bool) {
	switch v := d[key].(type) {
	case int64:
		return int(v), true
	case int:
		return v, true
	case float64:
		return int(v), true
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n, true
		}
	}
	return 0, false
}

func dictStringSlice(d map[string]any, key string) ([]string, bool) {
	arr, ok := d[key].([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(arr))
	for _, elem := range arr {
		if s, ok := elem.(string); ok {
			if t := strings.TrimSpace(s); t != "" {
				out = append(out, t)
			}
		}
	}
	return out, true
}

func parseXMLPlistDict(xmlBytes []byte) (map[string]any, error) {
	dec := xml.NewDecoder(bytes.NewReader(xmlBytes))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil, fmt.Errorf("empty plist")
		}
		if err != nil {
			return nil, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local == "plist" {
			continue
		}
		if se.Name.Local != "dict" {
			return nil, fmt.Errorf("expected root plist dict, got %s", se.Name.Local)
		}
		v, err := parsePlistValue(dec, se)
		if err != nil {
			return nil, err
		}
		m, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("unexpected root type %T", v)
		}
		return m, nil
	}
}

func parsePlistValue(dec *xml.Decoder, start xml.StartElement) (any, error) {
	switch start.Name.Local {
	case "true":
		return true, dec.Skip()
	case "false":
		return false, dec.Skip()
	case "string", "date":
		var s string
		if err := dec.DecodeElement(&s, &start); err != nil {
			return nil, err
		}
		return s, nil
	case "integer":
		var s string
		if err := dec.DecodeElement(&s, &start); err != nil {
			return nil, err
		}
		return strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	case "real":
		var s string
		if err := dec.DecodeElement(&s, &start); err != nil {
			return nil, err
		}
		return strconv.ParseFloat(strings.TrimSpace(s), 64)
	case "data":
		var b64 string
		if err := dec.DecodeElement(&b64, &start); err != nil {
			return nil, err
		}
		cleaned := strings.Map(func(r rune) rune {
			if r == ' ' || r == '\n' || r == '\r' || r == '\t' {
				return -1
			}
			return r
		}, b64)
		return base64.StdEncoding.DecodeString(cleaned)
	case "array":
		var out []any
		for {
			tok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			switch t := tok.(type) {
			case xml.StartElement:
				val, err := parsePlistValue(dec, t)
				if err != nil {
					return nil, err
				}
				out = append(out, val)
			case xml.EndElement:
				if t.Name.Local == "array" {
					return out, nil
				}
			}
		}
	case "dict":
		out := map[string]any{}
		var curKey string
		hasKey := false
		for {
			tok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			switch t := tok.(type) {
			case xml.StartElement:
				if t.Name.Local == "key" {
					var k string
					if err := dec.DecodeElement(&k, &t); err != nil {
						return nil, err
					}
					curKey = k
					hasKey = true
					continue
				}
				val, err := parsePlistValue(dec, t)
				if err != nil {
					return nil, err
				}
				if hasKey {
					out[curKey] = val
					hasKey = false
				}
			case xml.EndElement:
				if t.Name.Local == "dict" {
					return out, nil
				}
			}
		}
	default:
		return nil, dec.Skip()
	}
}
