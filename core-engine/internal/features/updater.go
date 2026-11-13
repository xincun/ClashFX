package features

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const DefaultGitHubReleasesURL = "https://api.github.com/repos/Clash-FX/ClashFX/releases"

type UpdateCheckResult struct {
	CurrentVersion  string `json:"current_version"`
	Channel         string `json:"channel"` // "stable" or "lab"
	UpdateAvailable bool   `json:"update_available"`
	LatestVersion   string `json:"latest_version,omitempty"`
	ReleaseURL      string `json:"release_url,omitempty"`
	DownloadURL     string `json:"download_url,omitempty"`
	ReleaseNotes    string `json:"release_notes,omitempty"`
	PublishedAt     string `json:"published_at,omitempty"`
}

type githubRelease struct {
	TagName     string `json:"tag_name"`
	HTMLURL     string `json:"html_url"`
	Body        string `json:"body"`
	Draft       bool   `json:"draft"`
	Prerelease  bool   `json:"prerelease"`
	PublishedAt string `json:"published_at"`
	Assets      []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

func CheckForUpdates(ctx context.Context, currentVersion, channel, apiURL string) (UpdateCheckResult, error) {
	return CheckForUpdatesWithProxy(ctx, currentVersion, channel, apiURL, 0)
}

func CheckForUpdatesWithProxy(ctx context.Context, currentVersion, channel, apiURL string, localProxyPort int) (UpdateCheckResult, error) {
	channel = strings.ToLower(strings.TrimSpace(channel))
	if channel != "lab" {
		channel = "stable"
	}
	if strings.TrimSpace(apiURL) == "" {
		apiURL = DefaultGitHubReleasesURL
	}

	res := UpdateCheckResult{
		CurrentVersion: currentVersion,
		Channel:        channel,
	}

	bodyBytes, statusCode, err := fetchReleasesFeed(ctx, apiURL, currentVersion, localProxyPort)
	if err != nil {
		return res, err
	}
	if statusCode != http.StatusOK {
		snippet := string(bodyBytes)
		if len(snippet) > 2048 {
			snippet = snippet[:2048]
		}
		return res, fmt.Errorf("releases endpoint returned HTTP %d: %s", statusCode, strings.TrimSpace(snippet))
	}

	var releases []githubRelease
	if err := json.Unmarshal(bodyBytes, &releases); err != nil {
		return res, fmt.Errorf("parse releases feed: %w", err)
	}

	var chosen *githubRelease
	for i := range releases {
		rel := &releases[i]
		if rel.Draft {
			continue
		}
		if channel == "stable" && rel.Prerelease {
			continue
		}
		if !isWindowsCompatibleRelease(rel) {
			continue
		}
		if chosen == nil || compareSemver(rel.TagName, chosen.TagName) > 0 {
			chosen = rel
		}
	}
	if chosen == nil {
		return res, nil
	}

	res.LatestVersion = chosen.TagName
	res.ReleaseURL = chosen.HTMLURL
	res.ReleaseNotes = chosen.Body
	res.PublishedAt = chosen.PublishedAt
	res.UpdateAvailable = compareSemver(chosen.TagName, currentVersion) > 0

	archTag := runtime.GOARCH // "arm64" or "amd64"
	for _, asset := range chosen.Assets {
		lower := strings.ToLower(asset.Name)
		if strings.HasSuffix(lower, ".dsym.zip") || strings.HasSuffix(lower, ".dmg") {
			continue
		}
		if !strings.HasSuffix(lower, ".exe") && !strings.HasSuffix(lower, ".msi") && !strings.HasSuffix(lower, ".zip") {
			continue
		}
		if strings.Contains(lower, archTag) || (archTag == "amd64" && strings.Contains(lower, "x64")) {
			res.DownloadURL = asset.BrowserDownloadURL
			break
		}
	}
	return res, nil
}

func fetchReleasesFeed(ctx context.Context, apiURL, currentVersion string, localProxyPort int) ([]byte, int, error) {
	doOnce := func(proxyPort int, timeout time.Duration) ([]byte, int, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", "ClashFX-Windows/"+currentVersion)

		transport := http.DefaultTransport.(*http.Transport).Clone()
		if proxyPort > 0 && proxyPort <= 65535 {
			pURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", proxyPort))
			transport.Proxy = http.ProxyURL(pURL)
		}
		client := &http.Client{
			Transport: transport,
			Timeout:   timeout,
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, 0, err
		}
		defer resp.Body.Close()
		data, rErr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		return data, resp.StatusCode, rErr
	}

	if localProxyPort > 0 {
		if data, code, err := doOnce(localProxyPort, 10*time.Second); err == nil {
			return data, code, nil
		}
	}
	return doOnce(0, 10*time.Second)
}

func isWindowsCompatibleRelease(rel *githubRelease) bool {
	if strings.Contains(strings.ToLower(rel.TagName), "win") {
		return true
	}
	for _, asset := range rel.Assets {
		lower := strings.ToLower(strings.TrimSpace(asset.Name))
		if strings.HasSuffix(lower, ".dsym.zip") || strings.HasSuffix(lower, ".dmg") || strings.HasSuffix(lower, ".pkg") {
			continue
		}
		if strings.HasSuffix(lower, ".exe") || strings.HasSuffix(lower, ".msi") ||
			(strings.HasSuffix(lower, ".zip") && (strings.Contains(lower, "win") || strings.Contains(lower, "portable") || strings.Contains(lower, "amd64") || strings.Contains(lower, "arm64") || strings.Contains(lower, "x64"))) {
			return true
		}
	}
	return false
}

func compareSemver(a, b string) int {
	pa, preA := splitSemver(a)
	pb, preB := splitSemver(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			if pa[i] > pb[i] {
				return 1
			}
			return -1
		}
	}
	if preA == preB {
		return 0
	}
	// Release > prerelease for identical major.minor.patch
	if preA == "" && preB != "" {
		return 1
	}
	if preA != "" && preB == "" {
		return -1
	}
	return strings.Compare(preA, preB)
}

func splitSemver(raw string) ([3]int, string) {
	s := strings.TrimPrefix(strings.TrimSpace(raw), "v")
	s = strings.TrimPrefix(s, "V")
	var pre string
	if idx := strings.IndexByte(s, '-'); idx >= 0 {
		pre = s[:idx]
		pre, s = s[idx+1:], pre
	}
	parts := strings.Split(s, ".")
	var nums [3]int
	for i := 0; i < len(parts) && i < 3; i++ {
		nums[i], _ = strconv.Atoi(parts[i])
	}
	return nums, pre
}
