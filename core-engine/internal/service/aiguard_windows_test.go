//go:build windows && (amd64 || arm64)

package service

import (
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// TestWFPStructABI verifies every Win64 WFP structure size and field offset against
// the Windows SDK / WireGuard tunnel/firewall Win64 ABI specifications.
func TestWFPStructABI(t *testing.T) {
	if got := unsafe.Sizeof(wfpByteBlob{}); got != 16 {
		t.Fatalf("sizeof(wfpByteBlob) = %d, want 16", got)
	}
	if got := unsafe.Offsetof(wfpByteBlob{}.data); got != 8 {
		t.Fatalf("offsetof(wfpByteBlob.data) = %d, want 8", got)
	}
	if got := unsafe.Sizeof(wfpDisplayData0{}); got != 16 {
		t.Fatalf("sizeof(wfpDisplayData0) = %d, want 16", got)
	}
	if got := unsafe.Sizeof(wfpValue0{}); got != 16 {
		t.Fatalf("sizeof(wfpValue0) = %d, want 16", got)
	}
	if got := unsafe.Offsetof(wfpValue0{}.value); got != 8 {
		t.Fatalf("offsetof(wfpValue0.value) = %d, want 8", got)
	}
	if got := unsafe.Sizeof(wfpAction0{}); got != 20 {
		t.Fatalf("sizeof(wfpAction0) = %d, want 20", got)
	}
	if got := unsafe.Sizeof(wfpFilterCondition0{}); got != 40 {
		t.Fatalf("sizeof(wfpFilterCondition0) = %d, want 40", got)
	}
	if got := unsafe.Offsetof(wfpFilterCondition0{}.conditionValue); got != 24 {
		t.Fatalf("offsetof(wfpFilterCondition0.conditionValue) = %d, want 24", got)
	}
	if got := unsafe.Sizeof(wfpSession0{}); got != 72 {
		t.Fatalf("sizeof(wfpSession0) = %d, want 72", got)
	}
	if got := unsafe.Sizeof(wfpProvider0{}); got != 64 {
		t.Fatalf("sizeof(wfpProvider0) = %d, want 64", got)
	}
	if got := unsafe.Sizeof(wfpSublayer0{}); got != 72 {
		t.Fatalf("sizeof(wfpSublayer0) = %d, want 72", got)
	}
	if got := unsafe.Sizeof(wfpFilter0{}); got != 200 {
		t.Fatalf("sizeof(wfpFilter0) = %d, want 200", got)
	}
	if got := unsafe.Offsetof(wfpFilter0{}.action); got != 128 {
		t.Fatalf("offsetof(wfpFilter0.action) = %d, want 128", got)
	}
	if got := unsafe.Offsetof(wfpFilter0{}.providerContextKey); got != 152 {
		t.Fatalf("offsetof(wfpFilter0.providerContextKey) = %d, want 152", got)
	}
	if got := unsafe.Offsetof(wfpFilter0{}.filterID); got != 176 {
		t.Fatalf("offsetof(wfpFilter0.filterID) = %d, want 176", got)
	}

	v4 := prefixToV4AddrMask(netip.MustParsePrefix("10.0.0.0/8"))
	if v4.addr != 0x0a000000 || v4.mask != 0xff000000 {
		t.Fatalf("unexpected v4 addr/mask: %#x / %#x", v4.addr, v4.mask)
	}
}

func copyFileSimple(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func runDialProbe(exe, target string) (int, string) {
	cmd := exec.Command(exe, target)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return 0, strings.TrimSpace(string(out))
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode(), strings.TrimSpace(string(out))
	}
	return -1, fmt.Sprintf("%v (%s)", err, strings.TrimSpace(string(out)))
}

// TestWindowsLiveWFPAndCoreSupervisor verifies real Windows kernel WFP app-scoped
// blocking, Wintun LUID permitting, real mihomo_core.exe abrupt crash auto-restart,
// and complete persistent log files. Run elevated on Windows with:
//   CLASHFX_LIVE_WINDOWS_WFP_VERIFY=1
//   CLASHFX_TEST_PROBE_EXE=<path-to-dial-probe.exe>
//   CLASHFX_TEST_REAL_CORE_DIR=<dir-containing-mihomo_core.exe>
func TestWindowsLiveWFPAndCoreSupervisor(t *testing.T) {
	if strings.TrimSpace(os.Getenv("CLASHFX_LIVE_WINDOWS_WFP_VERIFY")) != "1" {
		t.Skip("CLASHFX_LIVE_WINDOWS_WFP_VERIFY != 1")
	}
	probeSrc := strings.TrimSpace(os.Getenv("CLASHFX_TEST_PROBE_EXE"))
	coreDir := strings.TrimSpace(os.Getenv("CLASHFX_TEST_REAL_CORE_DIR"))
	if probeSrc == "" || coreDir == "" {
		t.Fatal("CLASHFX_TEST_PROBE_EXE and CLASHFX_TEST_REAL_CORE_DIR are required")
	}

	workDir := t.TempDir()
	aiExe := filepath.Join(workDir, "Programs", "Antigravity", "resources", "app", "extensions", "antigravity", "bin", "language_server_windows_arm64.exe")
	normalExe := filepath.Join(workDir, "NormalApp", "normal_client.exe")
	if err := copyFileSimple(probeSrc, aiExe); err != nil {
		t.Fatalf("copy ai probe: %v", err)
	}
	if err := copyFileSimple(probeSrc, normalExe); err != nil {
		t.Fatalf("copy normal probe: %v", err)
	}

	// Start a local loopback TCP listener so we prove loopback remains permitted under WFP.
	loopLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen loopback: %v", err)
	}
	defer loopLn.Close()
	go func() {
		for {
			c, aErr := loopLn.Accept()
			if aErr != nil {
				return
			}
			_ = c.Close()
		}
	}()
	loopAddr := loopLn.Addr().String()

	// Pick a reachable public TCP endpoint for baseline test (supports CN corporate/VM networks).
	publicTarget := ""
	for _, cand := range []string{"223.5.5.5:443", "114.114.114.114:53", "1.1.1.1:443", "8.8.8.8:443"} {
		if code, _ := runDialProbe(aiExe, cand); code == 0 {
			publicTarget = cand
			break
		}
	}
	if publicTarget == "" {
		t.Fatal("no reachable public TCP endpoint found before WFP activation")
	}
	t.Logf("Baseline reachable public target: %s", publicTarget)

	svcDataDir := filepath.Join(workDir, "svc-data")
	homeDir := filepath.Join(workDir, "home")
	logDir := filepath.Join(workDir, "logs")
	if err := os.MkdirAll(homeDir, 0755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(homeDir, ".runtime-config.yaml")
	cfgYAML := `mixed-port: 17988
allow-lan: false
mode: rule
log-level: info
ipv6: false
external-controller: 127.0.0.1:19188
dns:
  enable: true
  listen: 127.0.0.1:10588
  enhanced-mode: fake-ip
  fake-ip-range: 198.18.0.1/16
  default-nameserver:
    - 223.5.5.5
    - 114.114.114.114
  nameserver:
    - 223.5.5.5
tun:
  enable: true
  stack: mixed
  device: ClashFX
  auto-route: true
  auto-detect-interface: true
  strict-route: true
  dns-hijack:
    - any:53
rules:
  - MATCH,DIRECT
`
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0600); err != nil {
		t.Fatal(err)
	}

	runner := NewRunner(coreDir)
	runner.InitPrivilegedService(svcDataDir)
	defer func() {
		_ = runner.DisarmAIGuard()
		_ = runner.ShutdownService()
	}()

	// 1. Arm WFP AI Guard while core is DOWN -> blocking_leaks mode.
	syncResp := runner.Handle(Request{
		Action: "sync_ai_guard",
		AIGuard: &AIGuardConfig{
			Enabled:           true,
			ProtectedExePaths: []string{aiExe},
			TunDeviceName:     "ClashFX",
			AllowLAN:          true,
		},
	})
	if !syncResp.OK || syncResp.Status == nil || syncResp.Status.AIGuard == nil {
		t.Fatalf("sync_ai_guard failed: %+v", syncResp)
	}
	if !syncResp.Status.AIGuard.Armed || syncResp.Status.AIGuard.Mode != AIGuardModeBlockingLeaks {
		t.Fatalf("expected WFP armed in blocking_leaks mode, got %+v", syncResp.Status.AIGuard)
	}
	t.Logf("[PASS] WFP armed in blocking_leaks: %+v", syncResp.Status.AIGuard)

	// 1a. Protected AI executable MUST be blocked by Windows kernel (WSAEACCES / exit 13) on public IP!
	if code, out := runDialProbe(aiExe, publicTarget); code != 13 {
		t.Fatalf("SECURITY FAILURE: expected protected AI exe blocked with WSAEACCES (exit 13) while TUN down, got exit=%d out=%q", code, out)
	}
	t.Logf("[PASS] Protected AI exe (%s) BLOCKED in kernel (WSAEACCES) on %s while TUN is down", filepath.Base(aiExe), publicTarget)

	// 1b. Protected AI executable MUST still reach loopback 127.0.0.1!
	if code, out := runDialProbe(aiExe, loopAddr); code != 0 {
		t.Fatalf("expected protected AI exe to reach loopback %s, got exit=%d out=%q", loopAddr, code, out)
	}
	t.Logf("[PASS] Protected AI exe permitted on loopback %s", loopAddr)

	// 1c. Normal non-AI executable MUST NOT be blocked by WFP!
	if code, out := runDialProbe(normalExe, publicTarget); code != 0 {
		t.Fatalf("expected non-AI exe to remain unblocked on %s, got exit=%d out=%q", publicTarget, code, out)
	}
	t.Logf("[PASS] Non-AI exe (%s) unblocked on %s", filepath.Base(normalExe), publicTarget)

	// 2. Start real mihomo_core.exe with Wintun "ClashFX" -> verify WFP transitions to armed_normal + TUN LUID!
	startResp := runner.Handle(Request{
		Action:     "start_core",
		HomeDir:    homeDir,
		ConfigFile: cfgPath,
		LogDir:     logDir,
	})
	if !startResp.OK || startResp.Status == nil || !startResp.Status.Running {
		t.Fatalf("start_core with real mihomo_core.exe failed: %+v", startResp)
	}
	pid1 := startResp.Status.PID

	var st Status
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		st = runner.Status()
		if st.Running && st.TunHealthy && st.TunLUID != 0 && st.AIGuard != nil && st.AIGuard.Mode == AIGuardModeArmedNormal {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !st.TunHealthy || st.TunLUID == 0 || st.AIGuard == nil || st.AIGuard.Mode != AIGuardModeArmedNormal {
		t.Fatalf("expected Wintun ClashFX UP and WFP armed_normal, got %+v", st)
	}
	t.Logf("[PASS] Real mihomo_core.exe pid=%d + Wintun ClashFX luid=%d + WFP armed_normal", pid1, st.TunLUID)

	// 2a. With TUN UP, AI exe MUST be permitted via Wintun ClashFX (no WSAEACCES)!
	if code, out := runDialProbe(aiExe, publicTarget); code == 13 {
		t.Fatalf("expected AI exe permitted over Wintun ClashFX (luid=%d), but got WSAEACCES: %s", st.TunLUID, out)
	} else {
		t.Logf("[PASS] AI exe NOT blocked by WFP while Wintun ClashFX is UP (exit=%d out=%q)", code, out)
	}

	// 3. Simulate abrupt crash (TerminateProcess) -> verify automatic supervision recovery + full logs!
	hProc, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid1))
	if err != nil {
		t.Fatalf("OpenProcess(%d): %v", pid1, err)
	}
	if err := windows.TerminateProcess(hProc, 77); err != nil {
		_ = windows.CloseHandle(hProc)
		t.Fatalf("TerminateProcess(%d): %v", pid1, err)
	}
	_ = windows.CloseHandle(hProc)
	t.Logf("Force-killed real mihomo_core.exe pid=%d (exit=77); waiting for automatic supervisor recovery...", pid1)

	var recovered Status
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		cur := runner.Status()
		if cur.Running && cur.PID != pid1 && cur.RestartCount >= 1 && cur.Health == CoreHealthHealthy && cur.TunHealthy && cur.AIGuard != nil && cur.AIGuard.Mode == AIGuardModeArmedNormal {
			recovered = cur
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !recovered.Running || recovered.PID == pid1 || recovered.RestartCount < 1 {
		t.Fatalf("expected automatic mihomo_core.exe recovery after hard kill, got %+v", runner.Status())
	}
	t.Logf("[PASS] Auto-recovered real mihomo_core.exe pid=%d -> pid=%d (restarts=%d, tun_luid=%d, ai_guard=%s)",
		pid1, recovered.PID, recovered.RestartCount, recovered.TunLUID, recovered.AIGuard.Mode)

	// 4. Verify complete persistent log files on disk!
	for label, p := range map[string]string{
		"mihomo-core.log":            filepath.Join(logDir, "mihomo-core.log"),
		"clashfx-service-events.log": filepath.Join(logDir, "clashfx-service-events.log"),
		"clashfx-service.log":        filepath.Join(svcDataDir, "logs", "clashfx-service.log"),
	} {
		data, rErr := os.ReadFile(p)
		if rErr != nil || len(data) == 0 {
			t.Fatalf("expected non-empty persisted %s at %s (err=%v)", label, p, rErr)
		}
		text := string(data)
		if !strings.Contains(text, "exited unexpectedly") {
			t.Fatalf("expected crash record in %s, got:\n%s", label, text)
		}
		t.Logf("[PASS] Persisted %s verified (%d bytes)", label, len(data))
	}

	// 5. Disarm & stop -> verify zero lingering WFP rules!
	_ = runner.Handle(Request{Action: "disarm_ai_guard"})
	_ = runner.Handle(Request{Action: "stop_core"})
	if code, out := runDialProbe(aiExe, publicTarget); code != 0 {
		t.Fatalf("expected AI exe unblocked after disarm_ai_guard, got exit=%d out=%q", code, out)
	}
	t.Logf("[PASS] Disarm restored clean network access for %s (zero lingering WFP rules)", filepath.Base(aiExe))
}
