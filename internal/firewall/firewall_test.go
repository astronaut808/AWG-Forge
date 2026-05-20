package firewall

import (
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
	if got["forward-in"] != "ok" {
		t.Fatalf("forward-in status = %q, want ok", got["forward-in"])
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
	if runner.deletes != 5 {
		t.Fatalf("deletes = %d, want 5", runner.deletes)
	}
	if runner.inserts != 4 {
		t.Fatalf("inserts = %d, want 4", runner.inserts)
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
				Name:          "awg0",
				InterfaceName: "awg0",
				Enabled:       true,
				ListenPort:    51820,
				IPv4Subnet:    "10.8.0.0/24",
			},
			{
				Name:          "awg15",
				InterfaceName: "awg15",
				Enabled:       true,
				ListenPort:    51821,
				IPv4Subnet:    "10.15.0.0/24",
			},
		},
	}
}
