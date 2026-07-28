package firewall

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/astronaut808/awg-forge/internal/config"
)

func TestExpectedRulesSkipDisabledTunnels(t *testing.T) {
	state := testState()
	state.Tunnels[1].Enabled = false
	rules := ExpectedRules(testConfig(), state)
	if len(rules) != 4 {
		t.Fatalf("rules = %d, want 4", len(rules))
	}
	for _, rule := range rules {
		if rule.Tunnel != "awg0" {
			t.Fatalf("unexpected disabled tunnel rule: %+v", rule)
		}
	}
}

func TestCheckReportsMissingAndDuplicate(t *testing.T) {
	state := config.State{Tunnels: []config.Tunnel{testState().Tunnels[0]}}
	rules := ExpectedRules(testConfig(), state)
	runner := &fakeRunner{counts: map[string]int{
		rules[0].Spec(): 0,
		rules[1].Spec(): 2,
		rules[2].Spec(): 1,
		rules[3].Spec(): 1,
	}}
	report := Check(testConfig(), state, runner)
	got := map[string]string{}
	for _, item := range report.Results {
		got[item.Rule] = item.Status
	}
	if got["masquerade"] != "missing" {
		t.Fatalf("masquerade status = %q, want missing", got["masquerade"])
	}
	if got["input-udp"] != "duplicate" {
		t.Fatalf("input status = %q, want duplicate", got["input-udp"])
	}
	if got["forward-egress"] != "ok" {
		t.Fatalf("forward-egress status = %q, want ok", got["forward-egress"])
	}
}

func TestRepairDeletesDuplicatesAndInsertsOneRule(t *testing.T) {
	state := config.State{Tunnels: []config.Tunnel{testState().Tunnels[0]}}
	rules := ExpectedRules(testConfig(), state)
	runner := &fakeRunner{counts: map[string]int{
		rules[0].Spec(): 3,
		rules[1].Spec(): 0,
		rules[2].Spec(): 1,
		rules[3].Spec(): 1,
	}}
	report, err := Repair(testConfig(), state, runner)
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range rules {
		if got := runner.counts[rule.Spec()]; got != 1 {
			t.Fatalf("%s count = %d, want 1", rule.Spec(), got)
		}
	}
	for _, item := range report.Results {
		if item.Status != "ok" {
			t.Fatalf("%s status = %s, want ok", item.Rule, item.Status)
		}
	}
	if runner.deletes != 2 {
		t.Fatalf("deletes = %d, want 2", runner.deletes)
	}
	if runner.inserts != 1 {
		t.Fatalf("inserts = %d, want 1", runner.inserts)
	}
}

func TestExpectedRulesAreScopedAndOwned(t *testing.T) {
	tunnel := testState().Tunnels[0]
	rules := ExpectedRulesForTunnel("eth0", tunnel)
	if got, want := rules[2].Args, []string{"-i", "awg0", "-s", "10.8.0.0/24", "-o", "eth0", "-m", "conntrack", "--ctstate", "NEW,ESTABLISHED,RELATED", "-m", "comment", "--comment", "awg-forge-tunnel-awg0-forward-egress", "-j", "ACCEPT"}; !sameStrings(got, want) {
		t.Fatalf("egress rule = %#v, want %#v", got, want)
	}
	if got, want := rules[3].Args, []string{"-i", "eth0", "-o", "awg0", "-d", "10.8.0.0/24", "-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "-m", "comment", "--comment", "awg-forge-tunnel-awg0-forward-return", "-j", "ACCEPT"}; !sameStrings(got, want) {
		t.Fatalf("return rule = %#v, want %#v", got, want)
	}
	for _, rule := range rules {
		if rule.Name == "forward-egress" || rule.Name == "forward-return" {
			if containsSequence(rule.Args, []string{"-i", "awg0", "-j", "ACCEPT"}) || containsSequence(rule.Args, []string{"-o", "awg0", "-j", "ACCEPT"}) {
				t.Fatalf("rule must not allow unrestricted forwarding: %s", rule.Spec())
			}
		}
	}
}

func TestRuleCommentUsesIPTablesSafeCharactersAndFitsLimit(t *testing.T) {
	const maxCommentLength = 255
	valid := regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	for _, tunnel := range []config.Tunnel{
		{ID: strings.Repeat("f", 16), InterfaceName: "awg20"},
		{ID: strings.Repeat("x", 256), InterfaceName: "awg20"},
		{ID: "unsafe:id", InterfaceName: "awg20.1"},
	} {
		for _, name := range []string{"masquerade", "input-udp", "forward-egress", "forward-return"} {
			comment := ruleComment(tunnel, name)
			if !valid.MatchString(comment) {
				t.Fatalf("comment %q contains unsafe characters", comment)
			}
			if len(comment) > maxCommentLength {
				t.Fatalf("comment length = %d, want at most %d", len(comment), maxCommentLength)
			}
		}
	}
	first := ruleComment(config.Tunnel{ID: "unsafe:id", InterfaceName: "awg20.1"}, "masquerade")
	second := ruleComment(config.Tunnel{ID: "unsafe:id", InterfaceName: "awg20.2"}, "masquerade")
	if first == second {
		t.Fatal("fallback comment owners must not collide for distinct tunnels")
	}
}

func TestIPTablesRunnerCountsTaggedRuleDespiteCanonicalOutput(t *testing.T) {
	rule := ExpectedRulesForTunnel("eth0", testState().Tunnels[0])[2]
	comment, ok := ruleCommentFromArgs(rule.Args)
	if !ok {
		t.Fatal("managed rule has no comment")
	}
	outputCalls := 0
	runCalls := 0
	runner := IPTablesRunner{
		output: func(args ...string) ([]byte, error) {
			outputCalls++
			if got, want := strings.Join(args, " "), "-S FORWARD"; got != want {
				t.Fatalf("iptables output args = %q, want %q", got, want)
			}
			return []byte("-A FORWARD -s 10.8.0.0/24 -i awg0 -o eth0 -m conntrack --ctstate NEW,RELATED,ESTABLISHED -m comment --comment " + comment + " -j ACCEPT\n"), nil
		},
		run: func(args ...string) error {
			runCalls++
			if got, want := strings.Join(args, " "), "-C "+rule.Chain+" "+strings.Join(rule.Args, " "); got != want {
				t.Fatalf("iptables check args = %q, want %q", got, want)
			}
			return nil
		},
	}
	count, err := runner.Count(rule)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || outputCalls != 1 || runCalls != 1 {
		t.Fatalf("count=%d output=%d run=%d, want 1, 1, 1", count, outputCalls, runCalls)
	}
}

func TestLineHasCommentMatchesWholeCommentToken(t *testing.T) {
	if lineHasComment("-A FORWARD -m comment --comment awg-forge-owner-rule-extra -j ACCEPT", "awg-forge-owner-rule") {
		t.Fatal("prefix comment matched")
	}
	if !lineHasComment("-A FORWARD -m comment --comment awg-forge-owner-rule -j ACCEPT", "awg-forge-owner-rule") {
		t.Fatal("exact comment did not match")
	}
}

func TestExpectedRulesUseWarpAsSelectedEgress(t *testing.T) {
	tunnel := testState().Tunnels[0]
	tunnel.EgressMode = config.EgressWarp
	rules := ExpectedRulesForTunnel("eth0", tunnel)
	for _, rule := range []Rule{rules[0], rules[2], rules[3]} {
		if !containsSequence(rule.Args, []string{"-o", "warp0"}) && !containsSequence(rule.Args, []string{"-i", "warp0"}) {
			t.Fatalf("WARP egress is missing from %s", rule.Spec())
		}
	}
}

func TestMigrateLegacyRulesCreatesTaggedRulesBeforeDeletingLegacyRules(t *testing.T) {
	tunnel := testState().Tunnels[0]
	state := config.State{Tunnels: []config.Tunnel{tunnel}}
	runner := &orderedRunner{fakeRunner: fakeRunner{counts: map[string]int{}}}
	for _, rule := range LegacyRulesForTunnel("eth0", tunnel) {
		runner.counts[rule.Spec()] = 1
	}
	if _, err := MigrateLegacyRules(testConfig(), tunnel, runner); err != nil {
		t.Fatal(err)
	}
	for _, rule := range ExpectedRules(testConfig(), state) {
		if got := runner.counts[rule.Spec()]; got != 1 {
			t.Fatalf("managed rule %s count = %d, want 1", rule.Spec(), got)
		}
	}
	for _, rule := range LegacyRulesForTunnel("eth0", tunnel) {
		if got := runner.counts[rule.Spec()]; got != 0 {
			t.Fatalf("legacy rule %s count = %d, want 0", rule.Spec(), got)
		}
	}
	firstDelete := -1
	for i, action := range runner.actions {
		if action == "delete-legacy" {
			firstDelete = i
			break
		}
	}
	if firstDelete < 4 {
		t.Fatalf("legacy rule deletion happened before all managed rules were installed: %#v", runner.actions)
	}
}

func TestLegacyRulesPresentDetectsPartialLegacyFirewallState(t *testing.T) {
	tunnel := testState().Tunnels[0]
	legacy := LegacyRulesForTunnel("eth0", tunnel)
	runner := &fakeRunner{counts: map[string]int{
		legacy[2].Spec(): 1,
	}}
	present, err := LegacyRulesPresent(testConfig(), tunnel, runner)
	if err != nil {
		t.Fatal(err)
	}
	if !present {
		t.Fatal("LegacyRulesPresent = false, want true for a residual broad FORWARD rule")
	}

	present, err = LegacyRulesPresent(testConfig(), tunnel, &fakeRunner{counts: map[string]int{}})
	if err != nil {
		t.Fatal(err)
	}
	if present {
		t.Fatal("LegacyRulesPresent = true, want false without legacy rules")
	}
}

func TestMigrateLegacyRulesRemovesPartialLegacyFirewallState(t *testing.T) {
	tunnel := testState().Tunnels[0]
	legacy := LegacyRulesForTunnel("eth0", tunnel)
	runner := &orderedRunner{fakeRunner: fakeRunner{counts: map[string]int{
		legacy[2].Spec(): 1,
	}}}
	if _, err := MigrateLegacyRules(testConfig(), tunnel, runner); err != nil {
		t.Fatal(err)
	}
	if got := runner.counts[legacy[2].Spec()]; got != 0 {
		t.Fatalf("residual legacy rule count = %d, want 0", got)
	}
	firstDelete := -1
	for i, action := range runner.actions {
		if action == "delete-legacy" {
			firstDelete = i
			break
		}
	}
	if firstDelete < 4 {
		t.Fatalf("legacy rule deletion happened before all managed rules were installed: %#v", runner.actions)
	}
}

func TestRemoveRulesForTunnelDoesNotRemoveLegacyRules(t *testing.T) {
	tunnel := testState().Tunnels[0]
	runner := &fakeRunner{counts: map[string]int{}}
	for _, rule := range ExpectedRulesForTunnel("eth0", tunnel) {
		runner.counts[rule.Spec()] = 1
	}
	for _, rule := range LegacyRulesForTunnel("eth0", tunnel) {
		runner.counts[rule.Spec()] = 1
	}
	if err := RemoveRulesForTunnel(testConfig(), tunnel, runner); err != nil {
		t.Fatal(err)
	}
	for _, rule := range ExpectedRulesForTunnel("eth0", tunnel) {
		if got := runner.counts[rule.Spec()]; got != 0 {
			t.Fatalf("managed rule %s count = %d, want 0", rule.Spec(), got)
		}
	}
	for _, rule := range LegacyRulesForTunnel("eth0", tunnel) {
		if got := runner.counts[rule.Spec()]; got != 1 {
			t.Fatalf("legacy rule %s count = %d, want 1", rule.Spec(), got)
		}
	}
}

func TestMigrateLegacyRulesKeepsLegacyRulesWhenManagedSetupFails(t *testing.T) {
	tunnel := testState().Tunnels[0]
	runner := &failingInsertRunner{fakeRunner: fakeRunner{counts: map[string]int{}}}
	for _, rule := range LegacyRulesForTunnel("eth0", tunnel) {
		runner.counts[rule.Spec()] = 1
	}
	if _, err := MigrateLegacyRules(testConfig(), tunnel, runner); err == nil {
		t.Fatal("MigrateLegacyRules error = nil, want insert failure")
	}
	for _, rule := range LegacyRulesForTunnel("eth0", tunnel) {
		if got := runner.counts[rule.Spec()]; got != 1 {
			t.Fatalf("legacy rule %s count = %d, want 1", rule.Spec(), got)
		}
	}
}

func TestRepairDoesNothingWhenApplyDisabled(t *testing.T) {
	cfg := testConfig()
	cfg.ApplyConfig = false
	runner := &fakeRunner{counts: map[string]int{}}
	report, err := Repair(cfg, testState(), runner)
	if err != nil {
		t.Fatal(err)
	}
	if report.ApplyEnabled {
		t.Fatal("report ApplyEnabled = true, want false")
	}
	if runner.inserts != 0 || runner.deletes != 0 {
		t.Fatalf("runner changed firewall: inserts=%d deletes=%d", runner.inserts, runner.deletes)
	}
}

type fakeRunner struct {
	counts  map[string]int
	inserts int
	deletes int
}

type orderedRunner struct {
	fakeRunner
	actions []string
}

type failingInsertRunner struct {
	fakeRunner
}

func (r *failingInsertRunner) Insert(Rule) error {
	return errors.New("insert failed")
}

func (r *orderedRunner) Delete(rule Rule) error {
	if strings.HasPrefix(rule.Name, "legacy-") {
		r.actions = append(r.actions, "delete-legacy")
	} else {
		r.actions = append(r.actions, "delete-managed")
	}
	return r.fakeRunner.Delete(rule)
}

func (r *orderedRunner) Insert(rule Rule) error {
	r.actions = append(r.actions, "insert-managed")
	return r.fakeRunner.Insert(rule)
}

func (r fakeRunner) Count(rule Rule) (int, error) {
	return r.counts[rule.Spec()], nil
}

func (r *fakeRunner) Delete(rule Rule) error {
	key := rule.Spec()
	if r.counts[key] > 0 {
		r.counts[key]--
	}
	r.deletes++
	return nil
}

func (r *fakeRunner) Insert(rule Rule) error {
	r.counts[rule.Spec()]++
	r.inserts++
	return nil
}

func testConfig() config.Config {
	return config.Config{
		ExternalInterface: "eth0",
		ApplyConfig:       true,
	}
}

func testState() config.State {
	return config.State{
		Tunnels: []config.Tunnel{
			{
				ID:            "tunnel-awg0",
				Name:          "awg0",
				InterfaceName: "awg0",
				Enabled:       true,
				ListenPort:    51820,
				IPv4Subnet:    "10.8.0.0/24",
			},
			{
				ID:            "tunnel-awg15",
				Name:          "awg15",
				InterfaceName: "awg15",
				Enabled:       true,
				ListenPort:    51821,
				IPv4Subnet:    "10.15.0.0/24",
			},
		},
	}
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func containsSequence(values, sequence []string) bool {
	for i := 0; i+len(sequence) <= len(values); i++ {
		if sameStrings(values[i:i+len(sequence)], sequence) {
			return true
		}
	}
	return false
}
