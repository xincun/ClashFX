package transaction

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/clashfx/core-engine/internal/profile"
	"gopkg.in/yaml.v3"
)

type mockApplier struct {
	failApply   bool
	appliedData string
	applyCalls  int
}

func (m *mockApplier) ValidateConfig(_, configPath string) error {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	if strings.Contains(string(raw), "BROKEN_YAML_MARKER") {
		return fmt.Errorf("Parse config error: broken marker")
	}
	return nil
}

func (m *mockApplier) ApplyRuntimeConfig(_ context.Context, _ profile.AppSettings, _, runtimeConfigPath string, _ int, _ string) error {
	m.applyCalls++
	raw, err := os.ReadFile(runtimeConfigPath)
	if err != nil {
		return err
	}
	if m.failApply {
		m.failApply = false // fail only the candidate apply so rollback apply succeeds
		return fmt.Errorf("simulated runtime bind failure")
	}
	m.appliedData = string(raw)
	return nil
}

func TestTransactionApplyPortAvoidanceAndAutomaticRollback(t *testing.T) {
	store, err := profile.NewStore(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Write a good profile
	goodYAML := `mixed-port: 7890
proxies:
  - name: GoodNode
    type: socks5
    server: 127.0.0.1
    port: 1080
proxy-groups:
  - name: Proxy
    type: select
    proxies: [GoodNode, DIRECT]
rules:
  - MATCH,Proxy
`
	if err := profile.WriteFileAtomic(store.ProfilePath("config"), []byte(goodYAML), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.RememberProxySelection("config", "Proxy", "GoodNode"); err != nil {
		t.Fatal(err)
	}

	_, _ = store.UpdateSettings(func(s *profile.AppSettings) error {
		s.EnhancedMode = true
		return nil
	})

	applier := &mockApplier{}
	ctrl := NewController(store, applier, "")
	// Simulate 7890 and 19090 being in Windows Hyper-V excluded port range -> must relocate to 7891 and 19091!
	ctrl.SetPortProbeForTest(func(port int, _ bool) (bool, bool) {
		if port == 7890 || port == 19090 {
			return false, true
		}
		return true, false
	})

	st, err := ctrl.Apply(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if st.MixedPort != 7891 {
		t.Fatalf("MixedPort = %d, want 7891 (skipped reserved 7890)", st.MixedPort)
	}
	if st.APIPort != 19091 {
		t.Fatalf("APIPort = %d, want 19091 (skipped reserved 19090)", st.APIPort)
	}
	if strings.Contains(applier.appliedData, "x-clashfx-recovery-selections") {
		t.Fatalf("legacy custom recovery key must not be injected into stock Mihomo config:\n%s", applier.appliedData)
	}
	if !strings.Contains(applier.appliedData, "msftconnecttest.com") {
		t.Fatalf("expected default Windows NCSI fake-ip-filter in managed TUN config:\n%s", applier.appliedData)
	}
	var appliedDoc struct {
		RuleProviders map[string]struct {
			Payload []string `yaml:"payload"`
		} `yaml:"rule-providers"`
	}
	if err := yaml.Unmarshal([]byte(applier.appliedData), &appliedDoc); err != nil {
		t.Fatalf("unmarshal applied runtime yaml: %v", err)
	}
	wantInternal := []string{"PROCESS-NAME,mihomo_core.exe", "PROCESS-NAME,mihomo_core"}
	gotInternal := appliedDoc.RuleProviders[profile.InternalDirectRuleProviderName].Payload
	if len(gotInternal) != len(wantInternal) || gotInternal[0] != wantInternal[0] || gotInternal[1] != wantInternal[1] {
		t.Fatalf("clashfx-internal-direct payload = %v, want %v (daemon/service must not bypass active proxy)", gotInternal, wantInternal)
	}

	// Switch to a broken profile -> preflight validation fails BEFORE touching active runtime!
	if err := profile.WriteFileAtomic(store.ProfilePath("bad"), []byte("mixed-port: 7890\nproxies:\n  - name: BROKEN_YAML_MARKER\n    type: invalid\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, _ = store.UpdateSettings(func(s *profile.AppSettings) error {
		s.SelectedProfile = "bad"
		return nil
	})
	if _, err := ctrl.Apply(context.Background(), false); err == nil {
		t.Fatal("expected preflight validation failure on broken profile")
	}
	if !strings.Contains(applier.appliedData, "GoodNode") {
		t.Fatal("broken preflight must leave previous runtime untouched")
	}

	// Switch to another syntactically valid profile whose live apply fails -> automatic rollback restores GoodNode!
	otherYAML := strings.ReplaceAll(goodYAML, "GoodNode", "CandidateNode")
	if err := profile.WriteFileAtomic(store.ProfilePath("candidate"), []byte(otherYAML), 0600); err != nil {
		t.Fatal(err)
	}
	_, _ = store.UpdateSettings(func(s *profile.AppSettings) error {
		s.SelectedProfile = "candidate"
		return nil
	})
	applier.failApply = true
	if _, err := ctrl.Apply(context.Background(), false); err == nil {
		t.Fatal("expected live apply error to be returned")
	}
	if !strings.Contains(applier.appliedData, "GoodNode") {
		t.Fatalf("expected automatic rollback to restore GoodNode, got:\n%s", applier.appliedData)
	}
}
