package transaction

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/clashfx/core-engine/internal/portprobe"
	"github.com/clashfx/core-engine/internal/profile"
	"github.com/clashfx/core-engine/internal/service"
	"github.com/clashfx/core-engine/internal/subscription"
)

type runnerApplier struct {
	helper subscription.BinaryCoreHelper
	runner *service.Runner
}

func (a *runnerApplier) ValidateConfig(homeDir, configPath string) error {
	return a.helper.ValidateConfig(homeDir, configPath)
}

func (a *runnerApplier) ApplyRuntimeConfig(ctx context.Context, _ profile.AppSettings, homeDir, runtimeConfigPath string, apiPort int, secret string, forceRestart bool) error {
	if !forceRestart && apiPort > 0 && a.runner.Status().Running {
		return ReloadViaREST(ctx, apiPort, secret, runtimeConfigPath)
	}
	_, err := a.runner.Start(homeDir, runtimeConfigPath, "")
	return err
}

func TestRealMihomoCoreEndToEndIntegration(t *testing.T) {
	coreExe := strings.TrimSpace(os.Getenv("CLASHFX_CORE_EXE"))
	if coreExe == "" {
		t.Skip("CLASHFX_CORE_EXE not set; skipping real mihomo_core integration test")
	}

	trustedDir := filepath.Dir(coreExe)
	runner := service.NewRunner(trustedDir)
	runner.SetCoreBinaryForTest(filepath.Base(coreExe), 12*time.Second, 6*time.Second)
	defer func() { _ = runner.Stop() }()

	helper := subscription.BinaryCoreHelper{CoreBinaryPath: coreExe}

	dataDir := t.TempDir()
	customDir := filepath.Join(t.TempDir(), "custom-profiles")
	store, err := profile.NewStore(dataDir, "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if _, err := store.UpdateSettings(func(s *profile.AppSettings) error {
		s.CustomConfigDir = customDir
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// 1. Live share-link subscription fetch + mihomo_core -convert-share-links + -t validation
	shareURI := "ss://" + base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:secret-pass")) + "@127.0.0.1:18388#LiveNode\n"
	subBody := base64.StdEncoding.EncodeToString([]byte(shareURI))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Subscription-Userinfo", "upload=1024; download=2048; total=1048576; expire=1999999999")
		_, _ = w.Write([]byte(subBody))
	}))
	defer srv.Close()

	subMgr, err := subscription.NewManager(store, helper)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := subMgr.AddOrUpdate("live_sub", srv.URL, ""); err != nil {
		t.Fatal(err)
	}
	updatedSub, err := subMgr.Refresh(context.Background(), "live_sub", 0)
	if err != nil {
		t.Fatalf("Refresh live_sub failed: %v", err)
	}
	if updatedSub.UserInfo == nil || *updatedSub.UserInfo.Download != 2048 {
		t.Fatalf("unexpected userinfo: %+v", updatedSub.UserInfo)
	}

	// 2. Verify OS reserved port detection if host provided an actual WinNAT/Hyper-V excluded port
	preferredAPIPort := 19190
	if rawExcluded := strings.TrimSpace(os.Getenv("CLASHFX_TEST_EXCLUDED_PORT")); rawExcluded != "" {
		if excludedPort, convErr := strconv.Atoi(rawExcluded); convErr == nil && excludedPort > 0 {
			avail, reserved := portprobe.ProbeLoopbackPort(excludedPort, false)
			if avail || !reserved {
				t.Fatalf("expected OS excluded port %d to probe as avail=false reserved=true, got avail=%v reserved=%v", excludedPort, avail, reserved)
			}
			preferredAPIPort = excludedPort
			t.Logf("Verified real OS excluded port %d classified as reserved=true", excludedPort)
		}
	}

	_, err = store.UpdateSettings(func(s *profile.AppSettings) error {
		s.SelectedProfile = "live_sub"
		s.PortOverride = 17990
		s.APIPort = preferredAPIPort
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RememberProxySelection("live_sub", "PROXY", "LiveNode"); err != nil {
		t.Fatal(err)
	}

	applier := &runnerApplier{helper: helper, runner: runner}
	ctrl := NewController(store, applier, "")

	// 3. Cold-start real mihomo_core and verify LiveNode + port relocation
	state, err := ctrl.Apply(context.Background(), true)
	if err != nil {
		t.Fatalf("initial Apply failed: %v", err)
	}
	if preferredAPIPort != 19190 && state.APIPort == preferredAPIPort {
		t.Fatalf("expected controller to avoid OS excluded port %d, but got %d", preferredAPIPort, state.APIPort)
	}
	t.Logf("Live mihomo_core started on mixed-port=%d api-port=%d", state.MixedPort, state.APIPort)

	proxiesJSON := getCoreJSON(t, state.APIPort, "/proxies")
	if !strings.Contains(proxiesJSON, "LiveNode") {
		t.Fatalf("expected LiveNode in live /proxies response: %s", proxiesJSON)
	}

	// 4. NTFS fsnotify watch + Profile Mixin hot reload via PUT /configs?force=true
	watchCh := make(chan string, 4)
	if err := store.WatchActiveConfigDir(func(changedFile string) {
		select {
		case watchCh <- changedFile:
		default:
		}
	}); err != nil {
		t.Fatal(err)
	}
	mixinYAML := "profile:\n  prepend-rules:\n    - DOMAIN-SUFFIX,mixin-verified.example,DIRECT\n"
	if err := profile.WriteFileAtomic(store.ProfileMixinPath(), []byte(mixinYAML), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-watchCh:
	case <-time.After(4 * time.Second):
		t.Fatal("timed out waiting for fsnotify callback after editing Profile Mixin")
	}

	state, err = ctrl.Apply(context.Background(), false)
	if err != nil {
		t.Fatalf("hot-reload Apply with Profile Mixin failed: %v", err)
	}
	rulesJSON := getCoreJSON(t, state.APIPort, "/rules")
	if !strings.Contains(rulesJSON, "mixin-verified.example") {
		t.Fatalf("expected mixin-verified.example in live /rules response: %s", rulesJSON)
	}

	// 5. Switch to broken profile -> real mihomo_core -t rejects preflight, active core stays healthy
	brokenYAML := "mixed-port: 17990\nproxies:\n  - name: BrokenNode\n    type: nonexistent_protocol\n"
	if err := profile.WriteFileAtomic(store.ProfilePath("broken"), []byte(brokenYAML), 0600); err != nil {
		t.Fatal(err)
	}
	_, _ = store.UpdateSettings(func(s *profile.AppSettings) error {
		s.SelectedProfile = "broken"
		return nil
	})
	if _, err := ctrl.Apply(context.Background(), false); err == nil {
		t.Fatal("expected preflight validation error for broken profile")
	}
	proxiesAfterBroken := getCoreJSON(t, state.APIPort, "/proxies")
	if !strings.Contains(proxiesAfterBroken, "LiveNode") {
		t.Fatalf("live core lost LiveNode after rejected broken profile: %s", proxiesAfterBroken)
	}
}

func getCoreJSON(t *testing.T, apiPort int, path string) string {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	var lastErr error
	for i := 0; i < 15; i++ {
		resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d%s", apiPort, path))
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				var compact map[string]any
				if json.Unmarshal(body, &compact) == nil {
					return string(body)
				}
			}
			lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
		} else {
			lastErr = err
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("GET %s failed: %v", path, lastErr)
	return ""
}
