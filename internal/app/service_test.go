package app_test

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/protocol"
	"github.com/astronaut808/awg-forge/internal/storage"
)

func TestClientIPAllocationAndReuse(t *testing.T) {
	svc := app.New(testConfig(t))
	a, err := svc.AddClient("phone")
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.AddClient("laptop")
	if err != nil {
		t.Fatal(err)
	}
	if a.IPv4Address != "10.8.0.2" || b.IPv4Address != "10.8.0.3" {
		t.Fatalf("unexpected IPs: %s %s", a.IPv4Address, b.IPv4Address)
	}
	if err := svc.RemoveClient(a.ID); err != nil {
		t.Fatal(err)
	}
	c, err := svc.AddClient("tablet")
	if err != nil {
		t.Fatal(err)
	}
	if c.IPv4Address != "10.8.0.2" {
		t.Fatalf("expected freed IP 10.8.0.2, got %s", c.IPv4Address)
	}
}

func TestInvalidClientNameRejected(t *testing.T) {
	svc := app.New(testConfig(t))
	if _, err := svc.AddClient("../bad"); err == nil {
		t.Fatal("expected invalid name error")
	}
}

func TestConfigFilesWrittenWithCorrectPermissions(t *testing.T) {
	cfg := testConfig(t)
	svc := app.New(cfg)
	client, err := svc.AddClient("phone")
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{
		cfg.ConfigDir,
		filepath.Join(cfg.ConfigDir, "state.json"),
		filepath.Join(cfg.ConfigDir, "tunnels", "awg0", "server.conf"),
		filepath.Join(cfg.ConfigDir, "tunnels", "awg0", "clients", client.ID+".conf"),
	}
	want := []os.FileMode{0700, 0600, 0600, 0600}
	for i, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want[i] {
			t.Fatalf("%s permission = %o, want %o", path, got, want[i])
		}
	}
}

func TestCreateParallelTunnelAndClient(t *testing.T) {
	cfg := testConfig(t)
	svc := app.New(cfg)
	if _, err := svc.Init(); err != nil {
		t.Fatal(err)
	}
	tunnel, err := svc.CreateTunnel("awg_1_5", "awg15", "10.15.0.0/24", 51825)
	if err != nil {
		t.Fatal(err)
	}
	client, err := svc.AddClientToTunnel(tunnel.ID, "phone15")
	if err != nil {
		t.Fatal(err)
	}
	if client.TunnelID != tunnel.ID {
		t.Fatalf("client tunnel = %q, want %q", client.TunnelID, tunnel.ID)
	}
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Tunnels) != 2 {
		t.Fatalf("tunnels = %d, want 2", len(state.Tunnels))
	}
	if state.Tunnels[0].ListenPort == state.Tunnels[1].ListenPort {
		t.Fatal("parallel tunnels share listen port")
	}
	if _, err := os.Stat(filepath.Join(cfg.ConfigDir, "tunnels", "awg15", "server.conf")); err != nil {
		t.Fatal(err)
	}
}

func TestCreateTunnelRejectsPortAndSubnetCollisions(t *testing.T) {
	svc := app.New(testConfig(t))
	if _, err := svc.Init(); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateTunnel("awg_1_5", "dupe-port", "10.15.0.0/24", 51820); err == nil {
		t.Fatal("expected port collision")
	}
	if _, err := svc.CreateTunnel("awg_1_5", "dupe-subnet", "10.8.0.0/25", 51825); err == nil {
		t.Fatal("expected subnet collision")
	}
}

func TestDeleteTunnelCreatesStateBackup(t *testing.T) {
	cfg := testConfig(t)
	svc := app.New(cfg)
	if _, err := svc.CreateTunnel("awg_1_5", "awg15", "10.15.0.0/24", 51825); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteTunnel("awg15"); err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(filepath.Join(cfg.ConfigDir, "backups", "state-*-delete-awg15.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("backups = %d, want 1", len(matches))
	}
	info, err := os.Stat(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("backup permissions = %o, want 0600", info.Mode().Perm())
	}
}

func TestTunnelSettingsChangeMarksClientConfigStaleUntilDownloaded(t *testing.T) {
	svc := app.New(testConfig(t))
	client, err := svc.AddClient("phone")
	if err != nil {
		t.Fatal(err)
	}
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	tunnel := state.Tunnels[0]
	if client.ConfigRevision != tunnel.ConfigRevision {
		t.Fatalf("client revision = %d, tunnel revision = %d", client.ConfigRevision, tunnel.ConfigRevision)
	}
	if _, err := svc.UpdateTunnelSettings(tunnel.ID, tunnel.Name, tunnel.IPv4Subnet, tunnel.DNS, tunnel.AllowedIPs, tunnel.Keepalive, 1280, tunnel.ListenPort, tunnel.Enabled); err != nil {
		t.Fatal(err)
	}
	state, err = svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if state.Tunnels[0].ConfigRevision <= tunnel.ConfigRevision {
		t.Fatal("expected tunnel config revision to increase")
	}
	if state.Tunnels[0].Clients[0].ConfigRevision >= state.Tunnels[0].ConfigRevision {
		t.Fatal("expected client config to be stale")
	}
	if _, _, err := svc.ClientConfigForDownload(client.ID); err != nil {
		t.Fatal(err)
	}
	state, err = svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if state.Tunnels[0].Clients[0].ConfigRevision != state.Tunnels[0].ConfigRevision {
		t.Fatal("expected config download to mark client fresh")
	}
}

func TestSessionSecretIsGeneratedAndPersisted(t *testing.T) {
	cfg := testConfig(t)
	svc := app.New(cfg)
	state, err := svc.Init()
	if err != nil {
		t.Fatal(err)
	}
	if state.SessionSecret == "" {
		t.Fatal("expected generated session secret")
	}
	if state.SchemaVersion != 2 {
		t.Fatalf("schema version = %d, want 2", state.SchemaVersion)
	}
	if len(state.Tunnels) != 1 {
		t.Fatalf("tunnels = %d, want 1", len(state.Tunnels))
	}
	secret, err := svc.SessionSecret()
	if err != nil {
		t.Fatal(err)
	}
	if secret != state.SessionSecret {
		t.Fatal("session secret was not persisted")
	}
}

func TestConfiguredSessionSecretWins(t *testing.T) {
	cfg := testConfig(t)
	cfg.SessionSecret = "configured"
	svc := app.New(cfg)
	secret, err := svc.SessionSecret()
	if err != nil {
		t.Fatal(err)
	}
	if secret != "configured" {
		t.Fatalf("secret = %q, want configured", secret)
	}
}

func TestProtocolParamsValidation(t *testing.T) {
	p := protocol.Legacy10{}
	params, err := p.GenerateDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Validate(params); err != nil {
		t.Fatal(err)
	}
	assertRange(t, params["Jc"], 4, 10)
	assertRange(t, params["Jmin"], 64, 256)
	assertRange(t, params["Jmax"], 768, 1024)
	assertRange(t, params["S1"], 15, 64)
	assertRange(t, params["S2"], 15, 64)

	params["Jc"] = "11"
	if err := p.Validate(params); err == nil {
		t.Fatal("expected invalid Jc")
	}
}

func TestInitRepairsOutOfRangePersistedProtocolParams(t *testing.T) {
	cfg := testConfig(t)
	svc := app.New(cfg)
	state, err := svc.Init()
	if err != nil {
		t.Fatal(err)
	}
	state.Tunnels[0].ProtocolParams["Jc"] = "11"
	state.Tunnels[0].ProtocolParams["S1"] = "142"
	oldRevision := state.Tunnels[0].ConfigRevision
	if err := storage.New(cfg.ConfigDir).Save(state); err != nil {
		t.Fatal(err)
	}

	repaired, err := svc.Init()
	if err != nil {
		t.Fatal(err)
	}
	if got := repaired.Tunnels[0].ProtocolParams["Jc"]; got == "11" {
		t.Fatalf("Jc was not repaired: %s", got)
	}
	if got := repaired.Tunnels[0].ProtocolParams["S1"]; got == "142" {
		t.Fatalf("S1 was not repaired: %s", got)
	}
	if repaired.Tunnels[0].ConfigRevision <= oldRevision {
		t.Fatal("expected config revision to increase after protocol repair")
	}
	matches, err := filepath.Glob(filepath.Join(cfg.ConfigDir, "backups", "state-*-repair-protocol-params.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("repair backups = %d, want 1", len(matches))
	}
}

func TestProtocolParamsRejectWeakOrInvalidLegacyCombinations(t *testing.T) {
	p := protocol.Legacy10{}
	params, err := p.GenerateDefaults()
	if err != nil {
		t.Fatal(err)
	}
	params["S1"] = "0"
	params["S2"] = "56"
	if err := p.Validate(params); err == nil {
		t.Fatal("expected S1+56 collision error")
	}

	params, err = p.GenerateDefaults()
	if err != nil {
		t.Fatal(err)
	}
	params["H4"] = params["H1"]
	if err := p.Validate(params); err == nil {
		t.Fatal("expected duplicate H value error")
	}
}

func TestAWG15DefaultsIncludeDNSLikeI1(t *testing.T) {
	p, ok := protocol.ByID("awg_1_5")
	if !ok {
		t.Fatal("missing awg_1_5 profile")
	}
	params, err := p.GenerateDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if params["I1"] == "" {
		t.Fatal("expected I1 default")
	}
	if err := p.Validate(params); err != nil {
		t.Fatal(err)
	}
	params["I1"] = "<b 0xabc>"
	if err := p.Validate(params); err == nil {
		t.Fatal("expected odd hex signature validation error")
	}
}

func assertRange(t *testing.T, raw string, min, max int) {
	t.Helper()
	n, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatal(err)
	}
	if n < min || n > max {
		t.Fatalf("%s outside %d..%d", raw, min, max)
	}
}

func TestInvalidSubnetRejected(t *testing.T) {
	t.Setenv("IPV4_SUBNET", "bad")
	if _, err := config.FromEnv(); err == nil {
		t.Fatal("expected invalid subnet")
	}
}

func testConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{
		ConfigDir:           t.TempDir(),
		TunnelName:          "awg0",
		ServerHost:          "vpn.example.com",
		ListenPort:          51820,
		WebUIHost:           "127.0.0.1",
		WebUIPort:           51821,
		ExternalInterface:   "eth0",
		IPv4Subnet:          "10.8.0.0/24",
		DNS:                 "1.1.1.1",
		AllowedIPs:          "0.0.0.0/0",
		PersistentKeepalive: 0,
		MTU:                 1420,
		ProtocolProfile:     "awg_legacy_1_0",
	}
}
