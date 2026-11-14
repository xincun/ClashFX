package sysproxy

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSystemProxyLifecycleCrashRecoveryAILockAndWakeHeal(t *testing.T) {
	// Helper mode for external VM crash-kill verification:
	// CLASHFX_SYSPROXY_HELPER_DIR=<dir> CLASHFX_SYSPROXY_HELPER_ACTION=enable|recover
	if helperDir := strings.TrimSpace(os.Getenv("CLASHFX_SYSPROXY_HELPER_DIR")); helperDir != "" {
		mgr := NewManager(helperDir, NewOSBackend())
		switch os.Getenv("CLASHFX_SYSPROXY_HELPER_ACTION") {
		case "enable":
			if err := mgr.Apply(17899, ""); err != nil {
				t.Fatalf("helper Apply failed: %v", err)
			}
			return
		case "recover":
			recovered, err := mgr.RecoverAfterUncleanShutdown(false)
			if err != nil || !recovered {
				t.Fatalf("helper RecoverAfterUncleanShutdown = (%v, %v), want (true, nil)", recovered, err)
			}
			return
		case "restore":
			if err := mgr.Restore(); err != nil {
				t.Fatalf("helper Restore failed: %v", err)
			}
			return
		}
	}

	appDir := t.TempDir()
	backend := NewOSBackend()

	origState, err := backend.ReadState()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = backend.WriteState(origState) }()

	// Seed an existing corporate PAC + custom proxy state prior to ClashFX
	initial := OSProxyState{
		ProxyEnable:      0,
		HasProxyServer:   true,
		ProxyServer:      "corp-proxy.internal:8080",
		HasProxyOverride: true,
		ProxyOverride:    "*.corp.internal;<local>",
		HasAutoConfigURL: true,
		AutoConfigURL:    "http://wpad.corp.internal/wpad.dat",
	}
	if err := backend.WriteState(initial); err != nil {
		t.Fatal(err)
	}

	mgr := NewManager(appDir, backend)

	// 1. Enable system proxy on port 7890 with CIDR + custom bypass normalization
	if err := mgr.Apply(7890, "127.0.0.0/8, 192.168.1.0/24; *.mycompany.local, <local>"); err != nil {
		t.Fatal(err)
	}
	st, err := backend.ReadState()
	if err != nil {
		t.Fatal(err)
	}
	if st.ProxyEnable != 1 || st.ProxyServer != "127.0.0.1:7890" {
		t.Fatalf("unexpected enabled state: %+v", st)
	}
	if st.HasAutoConfigURL {
		t.Fatal("AutoConfigURL must be temporarily cleared while ClashFX owns system proxy")
	}
	if runtime.GOOS == "darwin" {
		if !strings.Contains(st.ProxyOverride, "127.0.0.0/8") || !strings.Contains(st.ProxyOverride, "192.168.1.0/24") || strings.Contains(st.ProxyOverride, "<local>") {
			t.Fatalf("unexpected Darwin ProxyOverride: %q", st.ProxyOverride)
		}
	} else {
		if !strings.Contains(st.ProxyOverride, "127.*") || !strings.Contains(st.ProxyOverride, "192.168.1.*") || !strings.HasSuffix(st.ProxyOverride, ";<local>") {
			t.Fatalf("unexpected WinINet ProxyOverride: %q", st.ProxyOverride)
		}
	}
	if winFmt := FormatWindowsBypass("127.0.0.0/8, 192.168.1.0/24; <local>"); !strings.Contains(winFmt, "127.*") || !strings.Contains(winFmt, "192.168.1.*") || !strings.HasSuffix(winFmt, ";<local>") {
		t.Fatalf("unexpected FormatWindowsBypass: %q", winFmt)
	}
	if macFmt := FormatDarwinBypass("127.0.0.0/8, 192.168.1.0/24; <local>"); len(macFmt) != 2 || macFmt[0] != "127.0.0.0/8" || macFmt[1] != "192.168.1.0/24" {
		t.Fatalf("unexpected FormatDarwinBypass: %v", macFmt)
	}
	if _, err := os.Stat(filepath.Join(appDir, SnapshotFileName)); err != nil {
		t.Fatalf("expected crash-recovery snapshot on disk: %v", err)
	}

	// 2. Simulate sleep/wake or third-party app clearing ProxyEnable -> ReconcileIfExpected repairs it
	_ = backend.WriteState(OSProxyState{ProxyEnable: 0})
	repaired, err := mgr.ReconcileIfExpected(true, 7890, "")
	if err != nil || !repaired {
		t.Fatalf("ReconcileIfExpected = (%v, %v), want (true, nil)", repaired, err)
	}
	st, _ = backend.ReadState()
	if st.ProxyEnable != 1 || st.ProxyServer != "127.0.0.1:7890" {
		t.Fatalf("expected wake repair to restore 127.0.0.1:7890, got %+v", st)
	}

	// 2b. Updating custom bypass list must reconcile OS override even when server & enable match
	repaired, err = mgr.ReconcileIfExpected(true, 7890, "example.com,corp.local")
	if err != nil || !repaired {
		t.Fatalf("ReconcileIfExpected bypass change = (%v, %v), want (true, nil)", repaired, err)
	}
	st, _ = backend.ReadState()
	if st.ProxyOverride != FormatBypassForOS("example.com,corp.local") {
		t.Fatalf("expected updated ProxyOverride %q, got %q", FormatBypassForOS("example.com,corp.local"), st.ProxyOverride)
	}
	repaired, err = mgr.ReconcileIfExpected(true, 7890, "example.com,corp.local")
	if err != nil || repaired {
		t.Fatalf("ReconcileIfExpected idempotent check = (%v, %v), want (false, nil)", repaired, err)
	}

	// 3. Simulate AI Proxy Lock fail-closed exit: Shutdown(true) leaves proxy pointing at 127.0.0.1:7890!
	if err := mgr.Shutdown(true); err != nil {
		t.Fatal(err)
	}
	st, _ = backend.ReadState()
	if st.ProxyEnable != 1 || st.ProxyServer != "127.0.0.1:7890" {
		t.Fatalf("AI Lock fail-closed Shutdown(true) should keep 127.0.0.1:7890, got %+v", st)
	}

	// 4. Simulate hard-kill crash recovery on next launch when wantsSystemProxy=false:
	// must restore the original corporate PAC + corp-proxy settings!
	mgrAfterCrash := NewManager(appDir, backend)
	recovered, err := mgrAfterCrash.RecoverAfterUncleanShutdown(false)
	if err != nil || !recovered {
		t.Fatalf("RecoverAfterUncleanShutdown = (%v, %v), want (true, nil)", recovered, err)
	}
	restored, _ := backend.ReadState()
	if restored != initial {
		t.Fatalf("crash recovery restored %+v, want original %+v", restored, initial)
	}

	// 5. Re-enable and test "Turn Off All Proxy Modes" (ForceClearAll)
	if err := mgrAfterCrash.Apply(7892, ""); err != nil {
		t.Fatal(err)
	}
	if err := mgrAfterCrash.ForceClearAll(); err != nil {
		t.Fatal(err)
	}
	cleared, _ := backend.ReadState()
	if cleared.ProxyEnable != 0 || cleared.HasProxyServer || cleared.HasAutoConfigURL {
		t.Fatalf("ForceClearAll left residual proxy settings: %+v", cleared)
	}

	// 6. Simulate uninstall after AI Lock Shutdown(true): CleanupForUninstall must restore unconditionally!
	_ = backend.WriteState(initial)
	if err := mgrAfterCrash.Apply(7890, ""); err != nil {
		t.Fatal(err)
	}
	_ = mgrAfterCrash.Shutdown(true) // leaves 127.0.0.1:7890 enabled
	if err := mgrAfterCrash.CleanupForUninstall(); err != nil {
		t.Fatal(err)
	}
	afterUninstall, _ := backend.ReadState()
	if afterUninstall != initial {
		t.Fatalf("CleanupForUninstall restored %+v, want %+v", afterUninstall, initial)
	}

	// 7. Even if snapshot was already deleted, CleanupForUninstall disables lingering loopback proxy
	// while preserving non-loopback corporate proxies!
	_ = backend.WriteState(OSProxyState{ProxyEnable: 1, HasProxyServer: true, ProxyServer: "http=127.0.0.1:7890;https=127.0.0.1:7890"})
	if err := mgrAfterCrash.CleanupForUninstall(); err != nil {
		t.Fatal(err)
	}
	if st, _ := backend.ReadState(); st.ProxyEnable != 0 {
		t.Fatalf("expected lingering loopback proxy disabled, got %+v", st)
	}
}
