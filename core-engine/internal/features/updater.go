package features

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const DefaultGitHubReleasesURL = "https://api.github.com/repos/yichengchen/clashX/releases"

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

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return res, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "ClashFX-Windows/"+currentVersion)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return res, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return res, fmt.Errorf("releases endpoint returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var releases []githubRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&releases); err != nil {
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
		if !strings.HasSuffix(lower, ".exe") && !strings.HasSuffix(lower, ".zip") {
			continue
		}
		if strings.Contains(lower, archTag) || (archTag == "amd64" && strings.Contains(lower, "x64")) {
			res.DownloadURL = asset.BrowserDownloadURL
			break
		}
	}
	return res, nil
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
		pre = s[idx+1:]
		s = s[:idx]
	}
	parts := strings.Split(s, ".")
	var nums [3]int
	for i := 0; i < len(parts) && i < 3; i++ {
		nums[i], _ = strconv.Atoi(parts[i])
	}
	return nums, pre
}
