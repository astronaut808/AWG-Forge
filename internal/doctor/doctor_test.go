package doctor

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/sqldb"
)

func TestParseAWGShow(t *testing.T) {
	got := parseAWGShow(`interface: awg20
  public key: server-key
  private key: (hidden)
  listening port: 7445
  jc: 8
  s3: 36

peer: client-key
  preshared key: (hidden)
  endpoint: 185.2.104.24:8225
  allowed ips: 10.20.0.2/32
  latest handshake: 8 seconds ago
  transfer: 236.24 KiB received, 3.90 MiB sent
`)
	if got.ListenPort != 7445 {
		t.Fatalf("listen port = %d, want 7445", got.ListenPort)
	}
	peer, ok := got.Peers["client-key"]
	if !ok {
		t.Fatal("missing client peer")
	}
	if peer.AllowedIPs != "10.20.0.2/32" {
		t.Fatalf("allowed IPs = %q", peer.AllowedIPs)
	}
	if peer.LatestHandshake != "8 seconds ago" {
		t.Fatalf("handshake = %q", peer.LatestHandshake)
	}
	if peer.Transfer != "236.24 KiB received, 3.90 MiB sent" {
		t.Fatalf("transfer = %q", peer.Transfer)
	}
}

func TestParseAWGShowPeerWithoutHandshake(t *testing.T) {
	got := parseAWGShow(`interface: awg15
  listening port: 7444

peer: client-key
  preshared key: (hidden)
  allowed ips: 10.15.0.2/32
`)
	peer := got.Peers["client-key"]
	if peer.LatestHandshake != "" {
		t.Fatalf("handshake = %q, want empty", peer.LatestHandshake)
	}
}

func TestParseRouteDev(t *testing.T) {
	got := parseRouteDev(`1.1.1.1 via 203.0.113.1 dev ens3 src 203.0.113.10 uid 0
    cache
`)
	if got != "ens3" {
		t.Fatalf("dev = %q, want ens3", got)
	}
}

func TestProtocolNotSupportedDetection(t *testing.T) {
	if !isProtocolNotSupported("awg show awg15 failed: Unable to access interface: Protocol not supported") {
		t.Fatal("expected Protocol not supported to be detected")
	}
	if isProtocolNotSupported("awg show awg15 failed: operation not permitted") {
		t.Fatal("did not expect unrelated error to match")
	}
}

func TestRedactProcessLine(t *testing.T) {
	got := redactProcessLine(`UNCONN 0 0 0.0.0.0:7443 0.0.0.0:* users:(("amneziawg-go",pid=12345,fd=7))`)
	if strings.Contains(got, "pid=12345") {
		t.Fatalf("pid was not redacted: %q", got)
	}
	if !strings.Contains(got, "pid=<pid>") {
		t.Fatalf("redacted pid marker missing: %q", got)
	}
}

func TestGroupResultsUsesStableCategoryOrder(t *testing.T) {
	results := []Result{
		{Level: "ok", Category: categoryClients, Area: "peer awg20/phone", Message: "runtime peer present"},
		{Level: "ok", Category: categorySystem, Area: "state", Message: "initialized"},
		{Level: "warn", Category: categoryNetwork, Area: "rp_filter all", Message: "strict mode"},
		{Level: "ok", Category: "custom", Area: "extra", Message: "kept"},
	}

	groups := GroupResults(results)
	got := make([]string, 0, len(groups))
	for _, group := range groups {
		got = append(got, group.Category)
	}
	want := []string{categorySystem, categoryNetwork, categoryClients, "custom"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("categories = %v, want %v", got, want)
	}
}

func TestGroupResultsPreservesResultOrderWithinCategory(t *testing.T) {
	results := []Result{
		{Level: "ok", Category: categoryNetwork, Area: "IPv4 forwarding", Message: "enabled"},
		{Level: "ok", Category: categoryNetwork, Area: "external interface", Message: "eth0 exists"},
	}

	groups := GroupResults(results)
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(groups))
	}
	if groups[0].Results[0].Area != "IPv4 forwarding" || groups[0].Results[1].Area != "external interface" {
		t.Fatalf("result order changed: %#v", groups[0].Results)
	}
}

func TestCheckTrafficLimitsWarnsForEnabledExceededClient(t *testing.T) {
	cfg, state := trafficLimitDoctorFixture(t, true)

	var c checker
	c.checkTrafficLimits(cfg, state)

	if len(c.results) != 1 {
		t.Fatalf("results = %#v, want one warning", c.results)
	}
	result := c.results[0]
	if result.Level != "warn" || result.Category != categoryClients {
		t.Fatalf("result = %#v, want clients warning", result)
	}
	if !strings.Contains(result.Area, "awg20/phone") {
		t.Fatalf("area = %q, want tunnel/client names", result.Area)
	}
	if !strings.Contains(result.Message, "enabled client is over traffic limit") {
		t.Fatalf("message = %q, want enabled over-limit warning", result.Message)
	}
}

func TestCheckTrafficLimitsWarnsForDisabledExceededClient(t *testing.T) {
	cfg, state := trafficLimitDoctorFixture(t, false)

	var c checker
	c.checkTrafficLimits(cfg, state)

	if len(c.results) != 1 {
		t.Fatalf("results = %#v, want one warning", c.results)
	}
	result := c.results[0]
	if result.Level != "warn" || result.Category != categoryClients {
		t.Fatalf("result = %#v, want clients warning", result)
	}
	if !strings.Contains(result.Message, "increase or clear the limit before enabling") {
		t.Fatalf("message = %q, want enable guidance", result.Message)
	}
}

func trafficLimitDoctorFixture(t *testing.T, enabled bool) (config.Config, config.State) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Config{
		ConfigDir:            dir,
		DatabaseMode:         sqldb.ModeSQLite,
		DatabasePath:         filepath.Join(dir, "awg-forge.db"),
		DatabaseBusyTimeout:  5 * time.Second,
		DatabaseQueryTimeout: 2 * time.Second,
		DatabaseMaxOpenConns: 1,
		DatabaseMaxIdleConns: 1,
	}
	if _, err := sqldb.Migrate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	if err := sqldb.RecordTrafficSamples(context.Background(), cfg, []sqldb.TrafficSample{
		{SampledAt: now.Add(-time.Minute), TunnelID: "tunnel-1", ClientID: "client-1", RxBytes: 0, TxBytes: 0, Present: true},
		{SampledAt: now, TunnelID: "tunnel-1", ClientID: "client-1", RxBytes: 7000, TxBytes: 0, Present: true},
	}); err != nil {
		t.Fatal(err)
	}
	limit := uint64(5000)
	if err := sqldb.SetClientTrafficLimit(context.Background(), cfg, "tunnel-1", "client-1", &limit); err != nil {
		t.Fatal(err)
	}
	return cfg, config.State{Tunnels: []config.Tunnel{{
		ID:   "tunnel-1",
		Name: "awg20",
		Clients: []config.Client{{
			ID:      "client-1",
			Name:    "phone",
			Enabled: enabled,
		}},
	}}}
}
