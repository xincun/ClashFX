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

const DefaultGitHubReleasesURL = "https://api.github.com/repos/xincun/ClashFX/releases"

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
	return CheckForUpdatesForPlatform(ctx, currentVersion, channel, apiURL, localProxyPort, runtime.GOOS, runtime.GOARCH)
}

func CheckForUpdatesForPlatform(ctx context.Context, currentVersion, channel, apiURL string, localProxyPort int, targetOS, targetArch string) (UpdateCheckResult, error) {
	channel = strings.ToLower(strings.TrimSpace(channel))
	if channel != "lab" {
		channel = "stable"
	}
	if strings.TrimSpace(apiURL) == "" {
		apiURL = DefaultGitHubReleasesURL
	}
	if strings.TrimSpace(targetOS) == "" {
		targetOS = runtime.GOOS
	}
	if strings.TrimSpace(targetArch) == "" {
		targetArch = runtime.GOARCH
	}

	res := UpdateCheckResult{
		CurrentVersion: currentVersion,
		Channel:        channel,
	}

	bodyBytes, statusCode, err := fetchReleasesFeed(ctx, apiURL, currentVersion, localProxyPort, targetOS)
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
		if !isCompatibleRelease(rel, targetOS) {
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
	res.DownloadURL = selectReleaseDownloadURL(chosen.Assets, targetOS, targetArch)
	return res, nil
}

func fetchReleasesFeed(ctx context.Context, apiURL, currentVersion string, localProxyPort int, targetOS string) ([]byte, int, error) {
	uaPrefix := "ClashFX-Windows/"
	if targetOS == "darwin" {
		uaPrefix = "ClashFX-macOS/"
	}
	doOnce := func(proxyPort int, timeout time.Duration) ([]byte, int, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", uaPrefix+currentVersion)

		transport := http.DefaultTransport.(*http.Transport).Clone()
		defer transport.CloseIdleConnections()
		if proxyPort > 0 && proxyPort <= 65535 {
			pURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", proxyPort))
			transport.Proxy = http.ProxyURL(pURL)
		} else {
			transport.Proxy = nil
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
		pData, pCode, pErr := doOnce(localProxyPort, 10*time.Second)
		if pErr == nil && pCode >= 200 && pCode < 300 {
			return pData, pCode, nil
		}
		dData, dCode, dErr := doOnce(0, 10*time.Second)
		if dErr == nil && dCode >= 200 && dCode < 300 {
			return dData, dCode, nil
		}
		if dErr != nil && pErr == nil {
			return pData, pCode, nil
		}
		return dData, dCode, dErr
	}
	return doOnce(0, 10*time.Second)
}

func isCompatibleRelease(rel *githubRelease, targetOS string) bool {
	if targetOS == "darwin" {
		if strings.Contains(strings.ToLower(rel.TagName), "mac") || strings.Contains(strings.ToLower(rel.TagName), "darwin") {
			return true
		}
		for _, asset := range rel.Assets {
			lower := strings.ToLower(strings.TrimSpace(asset.Name))
			if strings.HasSuffix(lower, ".dsym.zip") {
				continue
			}
			if strings.HasSuffix(lower, ".dmg") || strings.HasSuffix(lower, ".pkg") ||
				(strings.HasSuffix(lower, ".zip") && (strings.Contains(lower, "mac") || strings.Contains(lower, "darwin") || strings.Contains(lower, "universal"))) {
				return true
			}
		}
		return false
	}

	if strings.Contains(strings.ToLower(rel.TagName), "win") {
		return true
	}
	for _, asset := range rel.Assets {
		lower := strings.ToLower(strings.TrimSpace(asset.Name))
		if strings.HasSuffix(lower, ".dsym.zip") || strings.HasSuffix(lower, ".dmg") || strings.HasSuffix(lower, ".pkg") {
			continue
		}
		if strings.HasSuffix(lower, ".zip") &&
			(strings.Contains(lower, "mac") || strings.Contains(lower, "darwin") || strings.Contains(lower, "universal")) {
			continue
		}
		if strings.HasSuffix(lower, ".exe") || strings.HasSuffix(lower, ".msi") ||
			(strings.HasSuffix(lower, ".zip") && (strings.Contains(lower, "win") || strings.Contains(lower, "portable") || strings.Contains(lower, "amd64") || strings.Contains(lower, "arm64") || strings.Contains(lower, "x64"))) {
			return true
		}
	}
	return false
}

func selectReleaseDownloadURL(assets []struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}, targetOS, targetArch string) string {
	archMatches := func(lower string) bool {
		return strings.Contains(lower, targetArch) ||
			(targetArch == "amd64" && (strings.Contains(lower, "x64") || strings.Contains(lower, "x86_64")))
	}
	if targetOS == "darwin" {
		var universalFallback, genericFallback string
		for _, asset := range assets {
			lower := strings.ToLower(strings.TrimSpace(asset.Name))
			if strings.HasSuffix(lower, ".dsym.zip") {
				continue
			}
			isDarwinPkg := strings.HasSuffix(lower, ".dmg") || strings.HasSuffix(lower, ".pkg")
			isDarwinZip := strings.HasSuffix(lower, ".zip") && (strings.Contains(lower, "mac") || strings.Contains(lower, "darwin") || strings.Contains(lower, "universal"))
			if !isDarwinPkg && !isDarwinZip {
				continue
			}
			if archMatches(lower) {
				return asset.BrowserDownloadURL
			}
			if strings.Contains(lower, "universal") && universalFallback == "" {
				universalFallback = asset.BrowserDownloadURL
			}
			if isDarwinPkg && !strings.Contains(lower, "arm64") && !strings.Contains(lower, "amd64") && !strings.Contains(lower, "x64") && !strings.Contains(lower, "x86_64") && genericFallback == "" {
				genericFallback = asset.BrowserDownloadURL
			}
		}
		if universalFallback != "" {
			return universalFallback
		}
		return genericFallback
	}

	var zipFallback string
	for _, asset := range assets {
		lower := strings.ToLower(strings.TrimSpace(asset.Name))
		if strings.HasSuffix(lower, ".dsym.zip") || strings.HasSuffix(lower, ".dmg") || strings.HasSuffix(lower, ".pkg") {
			continue
		}
		if strings.HasSuffix(lower, ".zip") &&
			(strings.Contains(lower, "mac") || strings.Contains(lower, "darwin") || strings.Contains(lower, "universal")) {
			continue
		}
		isInstaller := strings.HasSuffix(lower, ".exe") || strings.HasSuffix(lower, ".msi")
		isZip := strings.HasSuffix(lower, ".zip")
		if !isInstaller && !isZip {
			continue
		}
		if archMatches(lower) {
			if isInstaller {
				return asset.BrowserDownloadURL
			}
			if zipFallback == "" {
				zipFallback = asset.BrowserDownloadURL
			}
		}
	}
	return zipFallback
}

func compareSemver(a, b string) int {
	pa, preA := splitSemver(a)
	pb, preB := splitSemver(b)
	for i := 0; i < 4; i++ {
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
	// Release > prerelease for identical numeric version segments
	if preA == "" && preB != "" {
		return 1
	}
	if preA != "" && preB == "" {
		return -1
	}
	return strings.Compare(preA, preB)
}

func splitSemver(raw string) ([4]int, string) {
	s := strings.TrimLeft(strings.TrimSpace(raw), "vV")
	var pre string
	if idx := strings.IndexByte(s, '-'); idx >= 0 {
		pre, s = s[idx+1:], s[:idx]
	}
	parts := strings.Split(s, ".")
	var nums [4]int
	for i := 0; i < len(parts) && i < 4; i++ {
		nums[i], _ = strconv.Atoi(parts[i])
	}
	return nums, pre
}
