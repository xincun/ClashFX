package service

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	upstreamReleaseLatestAPIURL = "https://api.github.com/repos/MetaCubeX/mihomo/releases/latest"
	upstreamReleaseAlphaAPIURL  = "https://api.github.com/repos/MetaCubeX/mihomo/releases/tags/Prerelease-Alpha"
	upstreamVersionLatestURL    = "https://github.com/MetaCubeX/mihomo/releases/latest/download/version.txt"
	upstreamVersionAlphaURL     = "https://github.com/MetaCubeX/mihomo/releases/download/Prerelease-Alpha/version.txt"
	upstreamDownloadLatestBase  = "https://github.com/MetaCubeX/mihomo/releases/latest/download/"
	upstreamDownloadAlphaBase   = "https://github.com/MetaCubeX/mihomo/releases/download/Prerelease-Alpha/"

	maxUpgradeArchiveBytes = 64 * 1024 * 1024
	maxUpgradeBinaryBytes  = 192 * 1024 * 1024
)

type upstreamReleasePayload struct {
	TagName string                `json:"tag_name"`
	Assets  []upstreamReleaseAsset `json:"assets"`
}

type upstreamReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Digest             string `json:"digest"`
}

type resolvedCoreRelease struct {
	Version        string
	AssetName      string
	DownloadURL    string
	ExpectedSHA256 string
}

// ProbeCoreVersion executes "<exe> -v" with a short timeout and extracts the reported
// Mihomo core version token (e.g. "v1.19.24" or "alpha-..."). Returns "" on failure.
func ProbeCoreVersion(exePath string) string {
	cleaned := strings.TrimSpace(exePath)
	if cleaned == "" {
		return ""
	}
	info, err := os.Stat(cleaned)
	if err != nil || info.IsDir() {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, cleaned, "-v")
	configureChildProcess(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil && len(out) == 0 {
		return ""
	}
	return parseCoreVersionOutput(string(out))
}

func parseCoreVersionOutput(raw string) string {
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		for _, tok := range strings.Fields(trimmed) {
			if strings.HasPrefix(tok, "alpha-") || strings.HasPrefix(tok, "Prerelease-") {
				return tok
			}
			if _, _, ok := parseNumericSemver(tok); ok {
				if strings.HasPrefix(tok, "v") || strings.HasPrefix(tok, "V") {
					return "v" + tok[1:]
				}
				return "v" + tok
			}
		}
	}
	return ""
}

func parseNumericSemver(raw string) ([3]int, string, bool) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "v")
	s = strings.TrimPrefix(s, "V")
	if s == "" {
		return [3]int{}, "", false
	}
	var pre string
	if idx := strings.IndexByte(s, '-'); idx >= 0 {
		pre = s[idx+1:]
		s = s[:idx]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return [3]int{}, "", false
	}
	var nums [3]int
	for i := 0; i < 3; i++ {
		if parts[i] == "" {
			return [3]int{}, "", false
		}
		v, err := strconv.Atoi(parts[i])
		if err != nil || v < 0 {
			return [3]int{}, "", false
		}
		nums[i] = v
	}
	return nums, pre, true
}

// CompareCoreVersions compares two Mihomo version strings. Semver takes precedence
// over non-semver tags such as alpha builds; equal strings return 0.
func CompareCoreVersions(a, b string) int {
	ta := strings.TrimSpace(a)
	tb := strings.TrimSpace(b)
	if ta == tb {
		return 0
	}
	if ta == "" {
		return -1
	}
	if tb == "" {
		return 1
	}
	pa, preA, okA := parseNumericSemver(ta)
	pb, preB, okB := parseNumericSemver(tb)
	if okA && okB {
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
		if preA == "" && preB != "" {
			return 1
		}
		if preA != "" && preB == "" {
			return -1
		}
		return strings.Compare(preA, preB)
	}
	if okA != okB {
		if okA {
			return 1
		}
		return -1
	}
	return strings.Compare(ta, tb)
}

func normalizeUpgradeChannel(ch string) string {
	switch strings.ToLower(strings.TrimSpace(ch)) {
	case "lab", "alpha", "prerelease":
		return "lab"
	default:
		return "stable"
	}
}

func coreAssetCandidates(goos, goarch, ver string) []string {
	ext := ".gz"
	if goos == "windows" {
		ext = ".zip"
	}
	switch {
	case goos == "darwin" && goarch == "arm64":
		return []string{fmt.Sprintf("mihomo-darwin-arm64-%s%s", ver, ext)}
	case goos == "darwin" && goarch == "amd64":
		return []string{
			fmt.Sprintf("mihomo-darwin-amd64-v1-%s%s", ver, ext),
			fmt.Sprintf("mihomo-darwin-amd64-%s%s", ver, ext),
			fmt.Sprintf("mihomo-darwin-amd64-compatible-%s%s", ver, ext),
		}
	case goos == "windows" && goarch == "arm64":
		return []string{fmt.Sprintf("mihomo-windows-arm64-%s%s", ver, ext)}
	case goos == "windows" && goarch == "amd64":
		return []string{
			fmt.Sprintf("mihomo-windows-amd64-v1-%s%s", ver, ext),
			fmt.Sprintf("mihomo-windows-amd64-%s%s", ver, ext),
			fmt.Sprintf("mihomo-windows-amd64-compatible-%s%s", ver, ext),
		}
	default:
		return []string{fmt.Sprintf("mihomo-%s-%s-%s%s", goos, goarch, ver, ext)}
	}
}

func selectMatchingCoreAsset(assets []upstreamReleaseAsset, goos, goarch, ver string) (upstreamReleaseAsset, bool) {
	byName := make(map[string]upstreamReleaseAsset, len(assets))
	for _, a := range assets {
		byName[strings.TrimSpace(a.Name)] = a
	}
	for _, cand := range coreAssetCandidates(goos, goarch, ver) {
		if a, ok := byName[cand]; ok {
			return a, true
		}
	}
	ext := ".gz"
	if goos == "windows" {
		ext = ".zip"
	}
	prefixes := []string{fmt.Sprintf("mihomo-%s-%s-", goos, goarch)}
	if goarch == "amd64" {
		prefixes = []string{
			fmt.Sprintf("mihomo-%s-amd64-v1-", goos),
			fmt.Sprintf("mihomo-%s-amd64-", goos),
			fmt.Sprintf("mihomo-%s-amd64-compatible-", goos),
		}
	}
	for _, pref := range prefixes {
		for _, a := range assets {
			name := strings.TrimSpace(a.Name)
			if !strings.HasPrefix(name, pref) || !strings.HasSuffix(name, ext) {
				continue
			}
			// Skip compiler-pinned or higher ISA variants (go120, v2, v3) during fuzzy fallback.
			tail := strings.TrimPrefix(name, pref)
			if strings.Contains(tail, "-go1") || strings.HasPrefix(tail, "v2-") || strings.HasPrefix(tail, "v3-") {
				continue
			}
			return a, true
		}
	}
	return upstreamReleaseAsset{}, false
}

func isTrustedAssetURL(rawURL, testBaseURL string) bool {
	if testBaseURL != "" {
		return strings.HasPrefix(rawURL, testBaseURL)
	}
	u, err := url.Parse(rawURL)
	if err != nil || !strings.EqualFold(u.Scheme, "https") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	switch host {
	case "github.com":
		return strings.HasPrefix(u.Path, "/MetaCubeX/mihomo/")
	case "objects.githubusercontent.com", "release-assets.githubusercontent.com":
		return true
	default:
		return false
	}
}

func (r *Runner) upgradeEndpointsLocked() (string, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.testReleaseAPIURL, r.testDownloadBaseURL
}

func doHTTPGetWithProxyFallback(ctx context.Context, rawURL string, accept string, proxyPort int, timeout time.Duration, maxBytes int64, testBaseURL string) ([]byte, int, error) {
	fetchOnce := func(port int) ([]byte, int, error) {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		if port > 0 && port <= 65535 {
			pURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
			transport.Proxy = http.ProxyURL(pURL)
		}
		client := &http.Client{
			Transport: transport,
			Timeout:   timeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 8 {
					return errors.New("too many redirects")
				}
				if !isTrustedAssetURL(req.URL.String(), testBaseURL) {
					return fmt.Errorf("blocked untrusted redirect target %q", req.URL.Hostname())
				}
				return nil
			},
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, 0, err
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		req.Header.Set("User-Agent", "ClashFX-CoreUpdater/1.0")
		resp, err := client.Do(req)
		if err != nil {
			return nil, 0, err
		}
		defer resp.Body.Close()
		data, rErr := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
		if rErr != nil {
			return nil, resp.StatusCode, rErr
		}
		if int64(len(data)) > maxBytes {
			return nil, resp.StatusCode, fmt.Errorf("response exceeded %d byte limit", maxBytes)
		}
		return data, resp.StatusCode, nil
	}

	if proxyPort > 0 && proxyPort <= 65535 {
		if data, code, err := fetchOnce(proxyPort); err == nil {
			return data, code, nil
		}
	}
	return fetchOnce(0)
}

func extractAlphaVersionFromAssetName(assetName string) string {
	idx := strings.Index(assetName, "alpha-")
	if idx < 0 {
		return ""
	}
	rest := assetName[idx:]
	rest = strings.TrimSuffix(rest, ".gz")
	rest = strings.TrimSuffix(rest, ".zip")
	return rest
}

func (r *Runner) resolveUpstreamCoreRelease(ctx context.Context, channel string, proxyPort int) (resolvedCoreRelease, error) {
	ch := normalizeUpgradeChannel(channel)
	testAPI, testDLBase := r.upgradeEndpointsLocked()

	apiURL := upstreamReleaseLatestAPIURL
	versionURL := upstreamVersionLatestURL
	downloadBase := upstreamDownloadLatestBase
	if ch == "lab" {
		apiURL = upstreamReleaseAlphaAPIURL
		versionURL = upstreamVersionAlphaURL
		downloadBase = upstreamDownloadAlphaBase
	}
	if testAPI != "" {
		apiURL = testAPI
	}

	body, code, err := doHTTPGetWithProxyFallback(ctx, apiURL, "application/vnd.github+json", proxyPort, 15*time.Second, 4<<20, testDLBase)
	if err == nil && code == http.StatusOK {
		var rel upstreamReleasePayload
		if jsonErr := json.Unmarshal(body, &rel); jsonErr != nil {
			return resolvedCoreRelease{}, fmt.Errorf("parse release metadata: %w", jsonErr)
		}
		ver := strings.TrimSpace(rel.TagName)
		if ch == "lab" || strings.EqualFold(ver, "Prerelease-Alpha") {
			for _, a := range rel.Assets {
				if strings.EqualFold(strings.TrimSpace(a.Name), "version.txt") && isTrustedAssetURL(a.BrowserDownloadURL, testDLBase) {
					if vBytes, vCode, vErr := doHTTPGetWithProxyFallback(ctx, a.BrowserDownloadURL, "", proxyPort, 10*time.Second, 1024, testDLBase); vErr == nil && vCode == http.StatusOK {
						if txt := strings.TrimSpace(string(vBytes)); txt != "" {
							ver = txt
						}
					}
					break
				}
			}
		}
		asset, ok := selectMatchingCoreAsset(rel.Assets, runtime.GOOS, runtime.GOARCH, ver)
		if !ok {
			return resolvedCoreRelease{}, fmt.Errorf("no %s/%s asset found in release %q", runtime.GOOS, runtime.GOARCH, rel.TagName)
		}
		if (ch == "lab" || strings.EqualFold(ver, "Prerelease-Alpha")) && strings.Contains(asset.Name, "alpha-") {
			if extracted := extractAlphaVersionFromAssetName(asset.Name); extracted != "" {
				ver = extracted
			}
		}
		if !isTrustedAssetURL(asset.BrowserDownloadURL, testDLBase) {
			return resolvedCoreRelease{}, fmt.Errorf("untrusted download URL %q", asset.BrowserDownloadURL)
		}
		var shaHex string
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(asset.Digest)), "sha256:") {
			shaHex = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(asset.Digest), "sha256:"))
		}
		return resolvedCoreRelease{
			Version:        ver,
			AssetName:      asset.Name,
			DownloadURL:    asset.BrowserDownloadURL,
			ExpectedSHA256: shaHex,
		}, nil
	}

	// If tests supplied a custom release API, don't mask HTTP errors via hardcoded GitHub fallback.
	if testAPI != "" {
		if err != nil {
			return resolvedCoreRelease{}, fmt.Errorf("query release endpoint: %w", err)
		}
		return resolvedCoreRelease{}, fmt.Errorf("release endpoint returned HTTP %d", code)
	}

	// Rate-limit / CDN fallback using upstream version.txt + releases/download URL.
	vBytes, vCode, vErr := doHTTPGetWithProxyFallback(ctx, versionURL, "text/plain", proxyPort, 10*time.Second, 1024, "")
	if vErr != nil {
		if err != nil {
			return resolvedCoreRelease{}, fmt.Errorf("fetch release info: %w", err)
		}
		return resolvedCoreRelease{}, fmt.Errorf("github API HTTP %d; fallback version.txt error: %w", code, vErr)
	}
	if vCode != http.StatusOK {
		return resolvedCoreRelease{}, fmt.Errorf("upstream version check returned HTTP %d", vCode)
	}
	ver := strings.TrimSpace(string(vBytes))
	if ver == "" {
		return resolvedCoreRelease{}, errors.New("upstream version.txt was empty")
	}
	cands := coreAssetCandidates(runtime.GOOS, runtime.GOARCH, ver)
	assetName := cands[0]
	dlURL := downloadBase + assetName
	return resolvedCoreRelease{
		Version:     ver,
		AssetName:   assetName,
		DownloadURL: dlURL,
	}, nil
}

func extractArchiveBinary(archiveBytes []byte, assetName string, out *os.File) error {
	lower := strings.ToLower(assetName)
	if strings.HasSuffix(lower, ".zip") {
		zr, err := zip.NewReader(bytes.NewReader(archiveBytes), int64(len(archiveBytes)))
		if err != nil {
			return fmt.Errorf("open zip archive: %w", err)
		}
		for _, f := range zr.File {
			if f.FileInfo().IsDir() {
				continue
			}
			base := strings.ToLower(filepath.Base(f.Name))
			if runtime.GOOS == "windows" {
				if !strings.HasSuffix(base, ".exe") {
					continue
				}
			} else if strings.Contains(base, ".") && !strings.HasSuffix(base, ".bin") {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return fmt.Errorf("open zip entry %q: %w", f.Name, err)
			}
			n, copyErr := io.Copy(out, io.LimitReader(rc, maxUpgradeBinaryBytes+1))
			_ = rc.Close()
			if copyErr != nil {
				return fmt.Errorf("extract %q: %w", f.Name, copyErr)
			}
			if n == 0 || n > maxUpgradeBinaryBytes {
				return fmt.Errorf("invalid extracted binary size (%d bytes)", n)
			}
			return nil
		}
		return errors.New("no executable binary found inside zip archive")
	}

	if strings.HasSuffix(lower, ".gz") {
		gz, err := gzip.NewReader(bytes.NewReader(archiveBytes))
		if err != nil {
			return fmt.Errorf("open gzip stream: %w", err)
		}
		defer gz.Close()
		n, copyErr := io.Copy(out, io.LimitReader(gz, maxUpgradeBinaryBytes+1))
		if copyErr != nil {
			return fmt.Errorf("decompress core binary: %w", copyErr)
		}
		if n == 0 || n > maxUpgradeBinaryBytes {
			return fmt.Errorf("invalid decompressed binary size (%d bytes)", n)
		}
		return nil
	}

	return fmt.Errorf("unsupported archive format %q", assetName)
}

func renameWithRetry(src, dst string) error {
	var err error
	attempts := 1
	if runtime.GOOS == "windows" {
		attempts = 6
	}
	for i := 0; i < attempts; i++ {
		err = os.Rename(src, dst)
		if err == nil {
			return nil
		}
		if i+1 < attempts {
			time.Sleep(100 * time.Millisecond)
		}
	}
	return err
}

func moveAsideBackup(corePath string) (string, error) {
	if _, err := os.Stat(corePath); os.IsNotExist(err) {
		return "", nil
	}
	// Clean any old backup files first best-effort.
	if matches, err := filepath.Glob(corePath + ".bak*"); err == nil {
		for _, m := range matches {
			_ = os.Remove(m)
		}
	}
	backupPath := corePath + ".bak"
	if err := os.Remove(backupPath); err != nil && !os.IsNotExist(err) {
		backupPath = fmt.Sprintf("%s.bak.%d", corePath, time.Now().UnixNano())
	}
	if err := renameWithRetry(corePath, backupPath); err != nil {
		return "", err
	}
	return backupPath, nil
}

// UpgradeCore atomically downloads, verifies, and swaps mihomo_core inside trustedDir.
// Network I/O executes while any currently running core continues serving traffic (so
// downloads can use proxyPort), then r.mu is acquired only for the stop-swap-start cutover,
// rolling back automatically if the replacement binary fails startup readiness.
func (r *Runner) UpgradeCore(ctx context.Context, channel string, proxyPort int) (CoreUpgradeInfo, Status, error) {
	r.upgradeMu.Lock()
	defer r.upgradeMu.Unlock()

	trustedDir := r.TrustedDir()
	r.mu.Lock()
	exeName := r.coreExeName
	_, testDLBase := r.testReleaseAPIURL, r.testDownloadBaseURL
	r.mu.Unlock()

	corePath := filepath.Join(trustedDir, exeName)
	prevVer := ProbeCoreVersion(corePath)

	rel, err := r.resolveUpstreamCoreRelease(ctx, channel, proxyPort)
	if err != nil {
		return CoreUpgradeInfo{PreviousVersion: prevVer, CurrentVersion: prevVer}, r.Status(), err
	}

	isLab := normalizeUpgradeChannel(channel) == "lab"
	upToDate := false
	if prevVer != "" {
		if isLab {
			upToDate = (prevVer == rel.Version)
		} else {
			upToDate = CompareCoreVersions(rel.Version, prevVer) <= 0
		}
	}
	if upToDate {
		return CoreUpgradeInfo{
			Updated:         false,
			AlreadyLatest:   true,
			PreviousVersion: prevVer,
			CurrentVersion:  prevVer,
			LatestVersion:   rel.Version,
		}, r.Status(), nil
	}

	archiveBytes, dlCode, dlErr := doHTTPGetWithProxyFallback(ctx, rel.DownloadURL, "", proxyPort, 90*time.Second, maxUpgradeArchiveBytes, testDLBase)
	if dlErr != nil {
		return CoreUpgradeInfo{PreviousVersion: prevVer, CurrentVersion: prevVer, LatestVersion: rel.Version}, r.Status(), fmt.Errorf("download core archive: %w", dlErr)
	}
	if dlCode != http.StatusOK {
		return CoreUpgradeInfo{PreviousVersion: prevVer, CurrentVersion: prevVer, LatestVersion: rel.Version}, r.Status(), fmt.Errorf("download core archive returned HTTP %d", dlCode)
	}

	if rel.ExpectedSHA256 != "" {
		sum := sha256.Sum256(archiveBytes)
		actual := hex.EncodeToString(sum[:])
		if !strings.EqualFold(actual, rel.ExpectedSHA256) {
			return CoreUpgradeInfo{PreviousVersion: prevVer, CurrentVersion: prevVer, LatestVersion: rel.Version}, r.Status(),
				fmt.Errorf("downloaded archive checksum mismatch (expected %s, got %s)", rel.ExpectedSHA256, actual)
		}
	}

	if err := os.MkdirAll(trustedDir, 0755); err != nil {
		return CoreUpgradeInfo{PreviousVersion: prevVer, CurrentVersion: prevVer, LatestVersion: rel.Version}, r.Status(), fmt.Errorf("prepare trusted core dir: %w", err)
	}

	tmpPattern := ".mihomo-upgrade-*"
	if runtime.GOOS == "windows" {
		tmpPattern = ".mihomo-upgrade-*.exe"
	}
	tmpFile, err := os.CreateTemp(trustedDir, tmpPattern)
	if err != nil {
		return CoreUpgradeInfo{PreviousVersion: prevVer, CurrentVersion: prevVer, LatestVersion: rel.Version}, r.Status(), fmt.Errorf("create staged core binary: %w", err)
	}
	tmpPath := tmpFile.Name()
	keepTmp := false
	defer func() {
		if !keepTmp {
			_ = os.Remove(tmpPath)
		}
	}()

	if err := extractArchiveBinary(archiveBytes, rel.AssetName, tmpFile); err != nil {
		_ = tmpFile.Close()
		return CoreUpgradeInfo{PreviousVersion: prevVer, CurrentVersion: prevVer, LatestVersion: rel.Version}, r.Status(), err
	}
	if err := tmpFile.Chmod(0755); err != nil {
		_ = tmpFile.Close()
		return CoreUpgradeInfo{PreviousVersion: prevVer, CurrentVersion: prevVer, LatestVersion: rel.Version}, r.Status(), err
	}
	if err := tmpFile.Close(); err != nil {
		return CoreUpgradeInfo{PreviousVersion: prevVer, CurrentVersion: prevVer, LatestVersion: rel.Version}, r.Status(), err
	}

	prepareUpgradedCoreFile(tmpPath)

	newVer := ProbeCoreVersion(tmpPath)
	if newVer == "" {
		return CoreUpgradeInfo{PreviousVersion: prevVer, CurrentVersion: prevVer, LatestVersion: rel.Version}, r.Status(),
			errors.New("staged core binary failed '-v' verification")
	}

	// Swap under r.mu so core lifecycle transitions remain strictly atomic.
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.trustedDir != trustedDir {
		return CoreUpgradeInfo{PreviousVersion: prevVer, CurrentVersion: prevVer, LatestVersion: rel.Version}, r.statusLocked(),
			errors.New("trusted directory changed concurrently during upgrade")
	}
	wasRunning := r.statusLocked().Running
	homeDir := r.homeDir
	configFile := r.configFile
	extUI := r.extUI

	if err := r.stopLocked(); err != nil {
		return CoreUpgradeInfo{PreviousVersion: prevVer, CurrentVersion: prevVer, LatestVersion: rel.Version}, r.statusLocked(),
			fmt.Errorf("stop core before upgrade: %w", err)
	}

	backupPath, bakErr := moveAsideBackup(corePath)
	if bakErr != nil {
		if wasRunning && homeDir != "" && configFile != "" {
			_, _ = r.startLocked(homeDir, configFile, extUI)
		}
		return CoreUpgradeInfo{PreviousVersion: prevVer, CurrentVersion: prevVer, LatestVersion: rel.Version}, r.statusLocked(),
			fmt.Errorf("backup previous core binary: %w", bakErr)
	}

	if err := renameWithRetry(tmpPath, corePath); err != nil {
		if backupPath != "" {
			_ = renameWithRetry(backupPath, corePath)
		}
		if wasRunning && homeDir != "" && configFile != "" {
			_, _ = r.startLocked(homeDir, configFile, extUI)
		}
		return CoreUpgradeInfo{PreviousVersion: prevVer, CurrentVersion: prevVer, LatestVersion: rel.Version}, r.statusLocked(),
			fmt.Errorf("install upgraded core binary: %w", err)
	}
	keepTmp = true
	refreshCorePlatformPostInstall(corePath)

	if wasRunning && homeDir != "" && configFile != "" {
		_, startErr := r.startLocked(homeDir, configFile, extUI)
		if startErr != nil {
			_ = r.stopLocked()
			_ = os.Remove(corePath)
			rollbackNote := ""
			if backupPath != "" {
				if rbRenameErr := renameWithRetry(backupPath, corePath); rbRenameErr == nil {
					if _, rbStartErr := r.startLocked(homeDir, configFile, extUI); rbStartErr != nil {
						rollbackNote = fmt.Sprintf("; rollback restart failed: %v", rbStartErr)
					} else {
						rollbackNote = "; rolled back to previous core"
					}
				} else {
					rollbackNote = fmt.Sprintf("; rollback rename failed: %v", rbRenameErr)
				}
			}
			return CoreUpgradeInfo{PreviousVersion: prevVer, CurrentVersion: prevVer, LatestVersion: rel.Version}, r.statusLocked(),
				fmt.Errorf("upgraded core failed to start (%w)%s", startErr, rollbackNote)
		}
	}

	if backupPath != "" {
		_ = os.Remove(backupPath)
	}
	return CoreUpgradeInfo{
		Updated:         true,
		PreviousVersion: prevVer,
		CurrentVersion:  newVer,
		LatestVersion:   rel.Version,
	}, r.statusLocked(), nil
}
