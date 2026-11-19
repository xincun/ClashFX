package transaction

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/clashfx/core-engine/internal/ailock"
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
		DNS struct {
			NameserverPolicy map[string]string `yaml:"nameserver-policy"`
		} `yaml:"dns"`
		Rules         []string `yaml:"rules"`
		RuleProviders map[string]struct {
			Payload []string `yaml:"payload"`
		} `yaml:"rule-providers"`
	}
	if err := yaml.Unmarshal([]byte(applier.appliedData), &appliedDoc); err != nil {
		t.Fatalf("unmarshal applied runtime yaml: %v", err)
	}
	for _, k := range []string{"*", "+.test", "+.local", "+.internal", "captive.apple.com", "+.msftconnecttest.com", "+.msftncsi.com"} {
		if got := appliedDoc.DNS.NameserverPolicy[k]; got != "system" {
			t.Fatalf("dns.nameserver-policy[%q] = %q, want \"system\"", k, got)
		}
	}
	if len(appliedDoc.Rules) < 2 ||
		appliedDoc.Rules[0] != "RULE-SET,"+profile.InternalDirectRuleProviderName+",DIRECT" ||
		appliedDoc.Rules[1] != "RULE-SET,"+profile.PrivateDirectRuleProviderName+",DIRECT" {
		t.Fatalf("expected %s then %s at top of rules, got %v",
			profile.InternalDirectRuleProviderName, profile.PrivateDirectRuleProviderName, appliedDoc.Rules)
	}
	foundCaptiveDirect := false
	for _, r := range appliedDoc.Rules {
		if r == "DOMAIN,captive.apple.com,DIRECT" {
			foundCaptiveDirect = true
			break
		}
	}
	if !foundCaptiveDirect {
		t.Fatalf("expected DOMAIN,captive.apple.com,DIRECT in rules, got %v", appliedDoc.Rules)
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

func TestPrivateIntranetDNSFakeIPFilterAndAILockDirectPrecedence(t *testing.T) {
	store, err := profile.NewStore(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Disable GEOSITE rules in mixin so real Mihomo config parsing requires no external geosite.dat.
	if err := profile.WriteFileAtomic(store.ProfileMixinPath(), []byte(profile.DisabledProfileMixinTemplate), 0600); err != nil {
		t.Fatal(err)
	}

	profileYAML := `mixed-port: 17890
proxies:
  - name: GeneralHK
    type: socks5
    server: 127.0.0.1
    port: 1081
  - name: LockedUS
    type: socks5
    server: 198.51.100.10
    port: 1082
proxy-groups:
  - name: Proxy
    type: select
    proxies: [GeneralHK]
rules:
  - MATCH,Proxy
`
	if err := profile.WriteFileAtomic(store.ProfilePath("config"), []byte(profileYAML), 0600); err != nil {
		t.Fatal(err)
	}

	_, err = store.UpdateSettings(func(s *profile.AppSettings) error {
		s.EnhancedMode = true
		s.AILockEnabled = true
		s.AILockTarget = "LockedUS"
		s.TunRouteExcludes = "30.0.0.0/8, 30.1.2.3, *.alibaba-inc.com, conan.custom.org"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	applier := &mockApplier{}
	ctrl := NewController(store, applier, "", ailock.TransformHookForController(nil))
	ctrl.SetPortProbeForTest(func(int, bool) (bool, bool) { return true, false })

	if _, err := ctrl.Apply(context.Background(), false); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	var doc struct {
		DNS struct {
			EnhancedMode     string            `yaml:"enhanced-mode"`
			FakeIPFilter     []string          `yaml:"fake-ip-filter"`
			NameserverPolicy map[string]string `yaml:"nameserver-policy"`
		} `yaml:"dns"`
		Tun struct {
			RouteExcludeAddress []string `yaml:"route-exclude-address"`
		} `yaml:"tun"`
		Rules         []string `yaml:"rules"`
		RuleProviders map[string]struct {
			Type     string   `yaml:"type"`
			Behavior string   `yaml:"behavior"`
			Payload  []string `yaml:"payload"`
		} `yaml:"rule-providers"`
	}
	if err := yaml.Unmarshal([]byte(applier.appliedData), &doc); err != nil {
		t.Fatalf("unmarshal applied runtime YAML failed: %v", err)
	}

	// 1. Verify Fake-IP filter includes dotless "*", multi-level private TLDs (+.<tld>),
	// and normalized TunRouteExcludes domains.
	hasItem := func(list []string, want string) bool {
		for _, v := range list {
			if v == want {
				return true
			}
		}
		return false
	}
	for _, want := range []string{
		"*",
		"+.test",
		"+.local",
		"+.lan",
		"+.localdomain",
		"+.localhost",
		"+.home.arpa",
		"+.internal",
		"+.corp",
		"+.intranet",
		"+.private",
		"+.alibaba-inc.com",
		"conan.custom.org",
	} {
		if !hasItem(doc.DNS.FakeIPFilter, want) {
			t.Fatalf("expected %q in dns.fake-ip-filter, got %v", want, doc.DNS.FakeIPFilter)
		}
	}

	// 2. Verify nameserver-policy routes dotless, private TLDs, and TunRouteExcludes domains to system DNS.
	for _, want := range []string{
		"*",
		"+.test",
		"+.local",
		"+.internal",
		"+.corp",
		"+.alibaba-inc.com",
		"conan.custom.org",
		"captive.apple.com",
	} {
		if got := doc.DNS.NameserverPolicy[want]; got != "system" {
			t.Fatalf("dns.nameserver-policy[%q] = %q, want \"system\"", want, got)
		}
	}

	// 3. Verify TUN route-exclude-address captures both CIDRs and bare IPs normalized to /32.
	for _, wantCIDR := range []string{"30.0.0.0/8", "30.1.2.3/32"} {
		if !hasItem(doc.Tun.RouteExcludeAddress, wantCIDR) {
			t.Fatalf("expected %q in tun.route-exclude-address, got %v", wantCIDR, doc.Tun.RouteExcludeAddress)
		}
	}

	// 4. Verify rule ordering places clashfx-internal-direct and clashfx-private-direct BEFORE clashfx-ai-lock.
	wantLeading := []string{
		"RULE-SET," + profile.InternalDirectRuleProviderName + ",DIRECT",
		"RULE-SET," + profile.PrivateDirectRuleProviderName + ",DIRECT",
		"RULE-SET," + profile.AILockRuleProviderName + ",LockedUS",
		"RULE-SET," + profile.AILockRuleProviderName + ",REJECT",
	}
	if len(doc.Rules) < len(wantLeading) {
		t.Fatalf("rules too short: %v", doc.Rules)
	}
	for i, want := range wantLeading {
		if doc.Rules[i] != want {
			t.Fatalf("rules[%d] = %q, want %q (all rules: %v)", i, doc.Rules[i], want, doc.Rules)
		}
	}

	// 5. Verify clashfx-private-direct inline provider contains LAN CIDRs, private TLDs, dotless regex, and TunRouteExcludes.
	privProv := doc.RuleProviders[profile.PrivateDirectRuleProviderName]
	if privProv.Type != "inline" || privProv.Behavior != "classical" {
		t.Fatalf("unexpected %s provider definition: %+v", profile.PrivateDirectRuleProviderName, privProv)
	}
	for _, wantRule := range []string{
		"IP-CIDR,127.0.0.0/8,no-resolve",
		"IP-CIDR,10.0.0.0/8,no-resolve",
		"IP-CIDR,192.168.0.0/16,no-resolve",
		"IP-CIDR,30.0.0.0/8,no-resolve",
		"IP-CIDR,30.1.2.3/32,no-resolve",
		"DOMAIN-SUFFIX,test",
		"DOMAIN-SUFFIX,local",
		"DOMAIN-SUFFIX,internal",
		"DOMAIN-SUFFIX,alibaba-inc.com",
		"DOMAIN,conan.custom.org",
		"DOMAIN-REGEX," + profile.DotlessHostnameRegex,
	} {
		if !hasItem(privProv.Payload, wantRule) {
			t.Fatalf("expected %q in %s payload, got %v", wantRule, profile.PrivateDirectRuleProviderName, privProv.Payload)
		}
	}

	// 6. Verify partial subscription fake-ip-filter + direct-nameserver + custom nameserver-policy preservation
	partialSubYAML := `mixed-port: 17890
dns:
  enable: true
  enhanced-mode: fake-ip
  fake-ip-filter:
    - "*.lan"
    - "custom.keep.org"
  direct-nameserver:
    - 223.5.5.5
  nameserver-policy:
    "+.test": "10.0.0.53"
proxies:
  - name: LockedUS
    type: socks5
    server: 198.51.100.10
    port: 1082
proxy-groups:
  - name: Proxy
    type: select
    proxies: [LockedUS]
rules:
  - MATCH,Proxy
`
	if err := profile.WriteFileAtomic(store.ProfilePath("partial"), []byte(partialSubYAML), 0600); err != nil {
		t.Fatal(err)
	}
	_, _ = store.UpdateSettings(func(s *profile.AppSettings) error {
		s.SelectedProfile = "partial"
		s.AILockEnabled = false
		s.TunRouteExcludes = ""
		return nil
	})
	if _, err := ctrl.Apply(context.Background(), false); err != nil {
		t.Fatalf("Apply partial profile failed: %v", err)
	}
	var partialDoc struct {
		DNS struct {
			FakeIPFilter                 []string          `yaml:"fake-ip-filter"`
			NameserverPolicy             map[string]string `yaml:"nameserver-policy"`
			DirectNameserverFollowPolicy bool              `yaml:"direct-nameserver-follow-policy"`
		} `yaml:"dns"`
	}
	if err := yaml.Unmarshal([]byte(applier.appliedData), &partialDoc); err != nil {
		t.Fatal(err)
	}
	filterJoined := strings.Join(partialDoc.DNS.FakeIPFilter, ",")
	for _, want := range []string{"custom.keep.org", "+.test", "+.local", "+.internal", "*"} {
		if !strings.Contains(filterJoined, want) {
			t.Fatalf("expected %q merged into partial fake-ip-filter, got %v", want, partialDoc.DNS.FakeIPFilter)
		}
	}
	if partialDoc.DNS.NameserverPolicy["+.test"] != "10.0.0.53" {
		t.Fatalf("expected user nameserver-policy[+.test] preserved, got %q", partialDoc.DNS.NameserverPolicy["+.test"])
	}
	if partialDoc.DNS.NameserverPolicy["+.internal"] != "system" {
		t.Fatalf("expected missing +.internal defaulted to system, got %q", partialDoc.DNS.NameserverPolicy["+.internal"])
	}
	if !partialDoc.DNS.DirectNameserverFollowPolicy {
		t.Fatal("expected direct-nameserver-follow-policy=true when profile specifies direct-nameserver")
	}

	// 5. Verify whitelist fake-ip-filter-mode is NOT corrupted by blacklist injections
	whitelistYAML := `mixed-port: 17890
dns:
  enable: true
  enhanced-mode: fake-ip
  fake-ip-filter-mode: whitelist
  fake-ip-filter:
    - "+.openai.com"
proxies:
  - name: LockedUS
    type: socks5
    server: 198.51.100.10
    port: 1082
proxy-groups:
  - name: Proxy
    type: select
    proxies: [LockedUS]
rules:
  - MATCH,Proxy
`
	if err := profile.WriteFileAtomic(store.ProfilePath("whitelist"), []byte(whitelistYAML), 0600); err != nil {
		t.Fatal(err)
	}
	_, _ = store.UpdateSettings(func(s *profile.AppSettings) error {
		s.SelectedProfile = "whitelist"
		return nil
	})
	if _, err := ctrl.Apply(context.Background(), false); err != nil {
		t.Fatalf("Apply whitelist profile failed: %v", err)
	}
	var wlDoc struct {
		DNS struct {
			FakeIPFilter []string `yaml:"fake-ip-filter"`
		} `yaml:"dns"`
	}
	if err := yaml.Unmarshal([]byte(applier.appliedData), &wlDoc); err != nil {
		t.Fatal(err)
	}
	if len(wlDoc.DNS.FakeIPFilter) != 1 || wlDoc.DNS.FakeIPFilter[0] != "+.openai.com" {
		t.Fatalf("whitelist fake-ip-filter must stay untouched, got %v", wlDoc.DNS.FakeIPFilter)
	}
}
