package service

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

const fakeMihomoSource = `package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
)

var (
	coreVersion = "v1.19.24"
	failStart   = "false"
)

func main() {
	var configFile string
	testOnly := false
	for i := 1; i < len(os.Args); i++ {
		arg := os.Args[i]
		switch arg {
		case "-v":
			fmt.Printf("Mihomo Meta %s %s %s with go1.26.3\n", coreVersion, runtime.GOOS, runtime.GOARCH)
			return
		case "-t":
			testOnly = true
		case "-f":
			if i+1 < len(os.Args) {
				configFile = os.Args[i+1]
				i++
			}
		case "-d", "-ext-ui":
			if i+1 < len(os.Args) {
				i++
			}
		}
	}
	if testOnly {
		return
	}
	if failStart == "true" {
		fmt.Fprintln(os.Stderr, "simulated broken core startup error")
		os.Exit(1)
	}
	ctrlAddr := ""
	if configFile != "" {
		if raw, err := os.ReadFile(configFile); err == nil {
			for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "external-controller:") {
					ctrlAddr = strings.TrimSpace(strings.TrimPrefix(trimmed, "external-controller:"))
					ctrlAddr = strings.Trim(ctrlAddr, "'\"")
					break
				}
			}
		}
	}
	if ctrlAddr != "" {
		mux := http.NewServeMux()
		mux.HandleFunc("/version", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, "{\"meta\":true,\"version\":%q}", coreVersion)
		})
		mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("{}"))
		})
		ln, err := net.Listen("tcp", ctrlAddr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "listen %s: %v\n", ctrlAddr, err)
			os.Exit(1)
		}
		defer ln.Close()
		go func() { _ = (&http.Server{Handler: mux}).Serve(ln) }()
	}
	fmt.Printf("RESTful API listening at: %s\n", ctrlAddr)
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh
}
`

func buildFakeMihomoBinary(t *testing.T, outPath, version string, fail bool) []byte {
	t.Helper()
	srcDir := t.TempDir()
	goMod := "module fake_mihomo\n\ngo 1.21\n"
	if err := os.WriteFile(filepath.Join(srcDir, "go.mod"), []byte(goMod), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "main.go"), []byte(fakeMihomoSource), 0600); err != nil {
		t.Fatal(err)
	}
	failFlag := "false"
	if fail {
		failFlag = "true"
	}
	ldflags := fmt.Sprintf("-s -w -X main.coreVersion=%s -X main.failStart=%s", version, failFlag)
	cmd := exec.Command("go", "build", "-ldflags="+ldflags, "-o", outPath, ".")
	cmd.Dir = srcDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile fake core (%s): %v\n%s", version, err, string(out))
	}
	b, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func makeTestArchive(t *testing.T, assetName, innerName string, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	if strings.HasSuffix(assetName, ".zip") {
		zw := zip.NewWriter(&buf)
		fw, err := zw.Create(innerName)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(payload); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func probeLiveHTTPVersion(t *testing.T, ctrlAddr string) string {
	t.Helper()
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Get("http://" + ctrlAddr + "/version")
	if err != nil {
		t.Fatalf("GET /version failed: %v", err)
	}
	defer resp.Body.Close()
	var p struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		t.Fatal(err)
	}
	return p.Version
}

func TestCompareCoreVersionsAndOutputParser(t *testing.T) {
	if CompareCoreVersions("v1.19.34", "v1.19.24") <= 0 {
		t.Fatal("expected v1.19.34 > v1.19.24")
	}
	if CompareCoreVersions("1.19.24", "v1.19.24") != 0 {
		t.Fatal("expected 1.19.24 == v1.19.24")
	}
	if CompareCoreVersions("v1.19.24", "v1.19.24-beta1") <= 0 {
		t.Fatal("expected stable v1.19.24 > prerelease")
	}
	if CompareCoreVersions("v1.19.24", "alpha-abc123") <= 0 {
		t.Fatal("expected semver > alpha build")
	}
	got := parseCoreVersionOutput("Mihomo Meta v1.19.24 darwin arm64 with go1.26.3\n")
	if got != "v1.19.24" {
		t.Fatalf("unexpected parsed version: %q", got)
	}
	gotAlpha := parseCoreVersionOutput("Mihomo Meta alpha-1a2b3c4 linux amd64")
	if gotAlpha != "alpha-1a2b3c4" {
		t.Fatalf("unexpected parsed alpha version: %q", gotAlpha)
	}
}

func TestExtractArchiveBinaryZipAndGzip(t *testing.T) {
	want := []byte("binary-payload-12345")

	gzBytes := makeTestArchive(t, "mihomo.gz", "", want)
	gzOut, err := os.CreateTemp(t.TempDir(), "out-gz-*")
	if err != nil {
		t.Fatal(err)
	}
	if err := extractArchiveBinary(gzBytes, "mihomo-darwin-arm64-v1.19.34.gz", gzOut); err != nil {
		t.Fatalf("gzip extract failed: %v", err)
	}
	_ = gzOut.Close()
	gotGZ, _ := os.ReadFile(gzOut.Name())
	if !bytes.Equal(gotGZ, want) {
		t.Fatalf("gzip mismatch: got %q", gotGZ)
	}

	zipEntry := "mihomo-darwin-arm64"
	if runtime.GOOS == "windows" {
		zipEntry = "mihomo-windows-amd64.exe"
	}
	zipBytes := makeTestArchive(t, "mihomo.zip", zipEntry, want)
	zipOut, err := os.CreateTemp(t.TempDir(), "out-zip-*")
	if err != nil {
		t.Fatal(err)
	}
	if err := extractArchiveBinary(zipBytes, "mihomo-test.zip", zipOut); err != nil {
		t.Fatalf("zip extract failed: %v", err)
	}
	_ = zipOut.Close()
	gotZIP, _ := os.ReadFile(zipOut.Name())
	if !bytes.Equal(gotZIP, want) {
		t.Fatalf("zip mismatch: got %q", gotZIP)
	}
}

func TestRunnerUpgradeCoreSuccessAlreadyLatestDigestAndRollback(t *testing.T) {
	trustedDir := t.TempDir()
	homeDir := t.TempDir()
	exeName := "mihomo_core"
	if runtime.GOOS == "windows" {
		exeName = "mihomo_core.exe"
	}
	corePath := filepath.Join(trustedDir, exeName)

	buildFakeMihomoBinary(t, corePath, "v1.19.24", false)
	v2Bin := buildFakeMihomoBinary(t, filepath.Join(t.TempDir(), "v2"), "v1.19.34", false)
	badStartBin := buildFakeMihomoBinary(t, filepath.Join(t.TempDir(), "v3-bad"), "v1.19.35", true)

	assetV2Name := coreAssetCandidates(runtime.GOOS, runtime.GOARCH, "v1.19.34")[0]
	innerV2 := "mihomo_core.exe"
	assetV2Bytes := makeTestArchive(t, assetV2Name, innerV2, v2Bin)
	assetV2SHA := sha256Hex(assetV2Bytes)

	assetV3Name := coreAssetCandidates(runtime.GOOS, runtime.GOARCH, "v1.19.35")[0]
	assetV3Bytes := makeTestArchive(t, assetV3Name, innerV2, badStartBin)
	assetV3SHA := sha256Hex(assetV3Bytes)

	mode := "v2_bad_digest"
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/release":
			w.Header().Set("Content-Type", "application/json")
			switch mode {
			case "v2_bad_digest":
				_ = json.NewEncoder(w).Encode(upstreamReleasePayload{
					TagName: "v1.19.34",
					Assets: []upstreamReleaseAsset{{
						Name:               assetV2Name,
						BrowserDownloadURL: srv.URL + "/dl/" + assetV2Name,
						Digest:             "sha256:0000000000000000000000000000000000000000000000000000000000000000",
					}},
				})
			case "v2_ok":
				_ = json.NewEncoder(w).Encode(upstreamReleasePayload{
					TagName: "v1.19.34",
					Assets: []upstreamReleaseAsset{{
						Name:               assetV2Name,
						BrowserDownloadURL: srv.URL + "/dl/" + assetV2Name,
						Digest:             "sha256:" + assetV2SHA,
					}},
				})
			case "v3_broken_start":
				_ = json.NewEncoder(w).Encode(upstreamReleasePayload{
					TagName: "v1.19.35",
					Assets: []upstreamReleaseAsset{{
						Name:               assetV3Name,
						BrowserDownloadURL: srv.URL + "/dl/" + assetV3Name,
						Digest:             "sha256:" + assetV3SHA,
					}},
				})
			}
		case "/dl/" + assetV2Name:
			_, _ = w.Write(assetV2Bytes)
		case "/dl/" + assetV3Name:
			_, _ = w.Write(assetV3Bytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctrlAddr := ln.Addr().String()
	_ = ln.Close()

	cfgPath := filepath.Join(homeDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("external-controller: "+ctrlAddr+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	runner := NewRunner(trustedDir)
	runner.SetCoreBinaryForTest(exeName, 10*time.Second, 5*time.Second)
	runner.SetUpgradeEndpointsForTest(srv.URL+"/release", srv.URL+"/dl/")

	startResp := runner.Handle(Request{Action: "start_core", HomeDir: homeDir, ConfigFile: cfgPath})
	if !startResp.OK || startResp.Status == nil || !startResp.Status.Running {
		t.Fatalf("failed to start initial fake core: %+v", startResp)
	}
	defer func() { _ = runner.Stop() }()
	initialPID := startResp.Status.PID
	if v := probeLiveHTTPVersion(t, ctrlAddr); v != "v1.19.24" {
		t.Fatalf("expected initial live /version v1.19.24, got %q", v)
	}

	// 1. Checksum mismatch must fail BEFORE stopping the running v1 core.
	respBadDigest := runner.Handle(Request{Action: "upgrade_core", Channel: "stable"})
	if respBadDigest.OK || !strings.Contains(respBadDigest.Message, "checksum mismatch") {
		t.Fatalf("expected checksum mismatch error, got %+v", respBadDigest)
	}
	if respBadDigest.Status == nil || !respBadDigest.Status.Running || respBadDigest.Status.PID != initialPID {
		t.Fatalf("running core should not have stopped on checksum error (want PID %d), got %+v", initialPID, respBadDigest.Status)
	}

	// 2. Valid upgrade while running swaps binary, restarts with new PID, and reports v1.19.34.
	mode = "v2_ok"
	respOK := runner.Handle(Request{Action: "upgrade_core", Channel: "stable"})
	if !respOK.OK || respOK.Upgrade == nil || !respOK.Upgrade.Updated || respOK.Upgrade.PreviousVersion != "v1.19.24" || respOK.Upgrade.CurrentVersion != "v1.19.34" {
		t.Fatalf("expected successful upgrade to v1.19.34, got %+v", respOK)
	}
	if respOK.Status == nil || !respOK.Status.Running || respOK.Status.PID == initialPID {
		t.Fatalf("expected restarted running core with new PID (old=%d), got %+v", initialPID, respOK.Status)
	}
	upgradedPID := respOK.Status.PID
	if v := probeLiveHTTPVersion(t, ctrlAddr); v != "v1.19.34" {
		t.Fatalf("expected upgraded live /version v1.19.34, got %q", v)
	}

	// 3. Repeat upgrade on same version returns AlreadyLatest without restarting process.
	respRepeat := runner.Handle(Request{Action: "upgrade_core", Channel: "stable"})
	if !respRepeat.OK || respRepeat.Upgrade == nil || respRepeat.Upgrade.Updated || !respRepeat.Upgrade.AlreadyLatest {
		t.Fatalf("expected AlreadyLatest=true on repeat upgrade, got %+v", respRepeat)
	}
	if respRepeat.Status == nil || !respRepeat.Status.Running || respRepeat.Status.PID != upgradedPID {
		t.Fatalf("expected no restart when already latest (want PID %d), got %+v", upgradedPID, respRepeat.Status)
	}

	// 4. Upgraded binary that passes -v but fails startup triggers automatic .bak rollback to v1.19.34!
	mode = "v3_broken_start"
	respRollback := runner.Handle(Request{Action: "upgrade_core", Channel: "stable"})
	if respRollback.OK || !strings.Contains(respRollback.Message, "rolled back to previous core") {
		t.Fatalf("expected automatic rollback error message, got %+v", respRollback)
	}
	if respRollback.Status == nil || !respRollback.Status.Running {
		t.Fatalf("expected rolled-back core running, got %+v", respRollback.Status)
	}
	if gotDiskVer := ProbeCoreVersion(corePath); gotDiskVer != "v1.19.34" {
		t.Fatalf("expected on-disk binary rolled back to v1.19.34, got %q", gotDiskVer)
	}
	if v := probeLiveHTTPVersion(t, ctrlAddr); v != "v1.19.34" {
		t.Fatalf("expected rolled-back live /version v1.19.34, got %q", v)
	}
}

func TestShouldPreserveInstalledCore(t *testing.T) {
	dir := t.TempDir()
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	bundledV24 := filepath.Join(dir, "bundled_v24"+ext)
	installedV24 := filepath.Join(dir, "installed_v24"+ext)
	installedV34 := filepath.Join(dir, "installed_v34"+ext)
	installedV20 := filepath.Join(dir, "installed_v20"+ext)
	missingPath := filepath.Join(dir, "missing_core"+ext)

	buildFakeMihomoBinary(t, bundledV24, "v1.19.24", false)
	buildFakeMihomoBinary(t, installedV24, "v1.19.24", false)
	buildFakeMihomoBinary(t, installedV34, "v1.19.34", false)
	buildFakeMihomoBinary(t, installedV20, "v1.19.20", false)

	if ShouldPreserveInstalledCore(bundledV24, missingPath) {
		t.Fatal("expected missing installed core not to be preserved")
	}
	if ShouldPreserveInstalledCore(bundledV24, installedV24) {
		t.Fatal("expected equal-version installed core (v1.19.24) to be refreshed cleanly")
	}
	if ShouldPreserveInstalledCore(bundledV24, installedV20) {
		t.Fatal("expected older installed core (v1.19.20) to be upgraded by bundled v1.19.24")
	}
	if !ShouldPreserveInstalledCore(bundledV24, installedV34) {
		t.Fatal("expected newer online-upgraded core (v1.19.34) to be preserved over bundled v1.19.24")
	}
	if !ShouldPreserveInstalledCore(missingPath, installedV34) {
		t.Fatal("expected valid installed core to be preserved if bundled path is missing")
	}
}

func TestDoHTTPGetWithProxyFallbackOnProxyNon2xx(t *testing.T) {
	// Target server returns 200 OK directly.
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("direct-ok"))
	}))
	defer origin.Close()

	// Proxy returns 403 rate-limited error.
	badProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("proxy-rate-limited"))
	}))
	defer badProxy.Close()

	u, err := url.Parse(badProxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxyPort, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	body, code, err := doHTTPGetWithProxyFallback(ctx, origin.URL+"/test", "", proxyPort, 3*time.Second, 1024, origin.URL)
	if err != nil || code != http.StatusOK || string(body) != "direct-ok" {
		t.Fatalf("expected direct fallback on proxy 403, got code=%d body=%q err=%v", code, string(body), err)
	}
}

func TestLiveUpgradeCoreIntegration(t *testing.T) {
	seedPath := strings.TrimSpace(os.Getenv("CLASHFX_LIVE_UPGRADE_VERIFY_SEED"))
	if seedPath == "" {
		t.Skip("CLASHFX_LIVE_UPGRADE_VERIFY_SEED not set; skipping live online mihomo_core upgrade test")
	}

	tmpDir := t.TempDir()
	seedDir := t.TempDir()
	exeName := "mihomo_core"
	if runtime.GOOS == "windows" {
		exeName = "mihomo_core.exe"
	}
	copyBin := func(src, dst string) {
		in, err := os.Open(src)
		if err != nil {
			t.Fatal(err)
		}
		defer in.Close()
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(out, in); err != nil {
			_ = out.Close()
			t.Fatal(err)
		}
		if err := out.Close(); err != nil {
			t.Fatal(err)
		}
	}

	corePath := filepath.Join(tmpDir, exeName)
	bundledPath := filepath.Join(seedDir, exeName)
	copyBin(seedPath, corePath)
	copyBin(seedPath, bundledPath)

	prevVer := ProbeCoreVersion(corePath)
	if prevVer == "" {
		t.Fatalf("failed to probe seed core version at %s", corePath)
	}

	proxyPort := 7890
	runner := NewRunner(tmpDir)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	info, _, err := runner.UpgradeCore(ctx, "stable", proxyPort)
	if err != nil {
		t.Fatalf("live UpgradeCore failed: %v (info=%+v)", err, info)
	}
	if !info.Updated {
		t.Fatalf("expected live UpgradeCore Updated=true, got %+v", info)
	}
	if CompareCoreVersions(info.CurrentVersion, prevVer) <= 0 {
		t.Fatalf("expected upgraded version > %q, got %q", prevVer, info.CurrentVersion)
	}
	if got := ProbeCoreVersion(corePath); got != info.CurrentVersion {
		t.Fatalf("upgraded binary on disk reported %q, want %q", got, info.CurrentVersion)
	}
	if !ShouldPreserveInstalledCore(bundledPath, corePath) {
		t.Fatalf("expected upgraded core %q to be preserved over bundled %q", info.CurrentVersion, prevVer)
	}
	t.Logf("Live UpgradeCore succeeded: %s -> %s", info.PreviousVersion, info.CurrentVersion)
}
