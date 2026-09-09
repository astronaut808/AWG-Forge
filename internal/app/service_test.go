package app_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/buildinfo"
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

func TestConcurrentClientCreationDoesNotLoseState(t *testing.T) {
	svc := app.New(testConfig(t))
	state, err := svc.Init()
	if err != nil {
		t.Fatal(err)
	}
	const clients = 20
	var wg sync.WaitGroup
	errs := make(chan error, clients)
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := svc.AddClientToTunnel(state.Tunnels[0].ID, "client-"+strconv.Itoa(i))
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	state, err = svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if got := len(state.Tunnels[0].Clients); got != clients {
		t.Fatalf("clients = %d, want %d", got, clients)
	}
}

func TestFreshInitDefaultsToAWG20(t *testing.T) {
	cfg := config.Config{
		ConfigDir:         t.TempDir(),
		ServerHost:        "vpn.example.com",
		ExternalInterface: "eth0",
	}
	state, err := app.New(cfg).Init()
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Tunnels[0].ProtocolProfileID; got != "awg_2_0" {
		t.Fatalf("profile = %s, want awg_2_0", got)
	}
	if got := state.Tunnels[0].InterfaceName; got != "awg20" {
		t.Fatalf("interface = %s, want awg20", got)
	}
	if got := state.Tunnels[0].IPv4Subnet; got != "10.20.0.0/24" {
		t.Fatalf("subnet = %s, want 10.20.0.0/24", got)
	}
}

func TestInitRejectsUnsafeLoadedInterfaceNames(t *testing.T) {
	for _, name := range []string{"../escape", "awg/escape", `awg\\escape`, "awg..escape"} {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig(t)
			state := config.State{
				SchemaVersion: config.CurrentStateSchemaVersion,
				Warp:          config.Warp{InterfaceName: "warp0"},
				Tunnels:       []config.Tunnel{{InterfaceName: name}},
			}
			if err := storage.New(cfg.ConfigDir).Save(state); err != nil {
				t.Fatal(err)
			}
			if _, err := app.New(cfg).Init(); err == nil || !strings.Contains(err.Error(), "invalid tunnel interface name") {
				t.Fatalf("Init error = %v, want unsafe interface name rejection", err)
			}
		})
	}
}

func TestInitRejectsUnsafeLoadedWARPInterfaceName(t *testing.T) {
	cfg := testConfig(t)
	state := config.State{
		SchemaVersion: config.CurrentStateSchemaVersion,
		Warp:          config.Warp{InterfaceName: "../warp"},
	}
	if err := storage.New(cfg.ConfigDir).Save(state); err != nil {
		t.Fatal(err)
	}
	if _, err := app.New(cfg).Init(); err == nil || !strings.Contains(err.Error(), "invalid WARP interface name") {
		t.Fatalf("Init error = %v, want unsafe WARP interface name rejection", err)
	}
}

func TestInitWithOptionsCreatesFirstTunnel(t *testing.T) {
	cfg := config.Config{
		ConfigDir:         t.TempDir(),
		ServerHost:        "fallback.example.com",
		ExternalInterface: "eth0",
	}
	state, err := app.New(cfg).InitWithOptions(app.InitOptions{
		ServerHost:          "edge.example.com",
		ExternalInterface:   "ens3",
		ProfileID:           "awg_1_5",
		Name:                "awg15",
		ListenPort:          51825,
		IPv4Subnet:          "10.15.0.0/24",
		DNS:                 "9.9.9.9",
		AllowedIPs:          "0.0.0.0/0",
		PersistentKeepalive: 25,
		MTU:                 1280,
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.ServerHost != "edge.example.com" || state.ExternalInterface != "ens3" {
		t.Fatalf("init host/interface not applied: %#v", state)
	}
	tunnel := state.Tunnels[0]
	if tunnel.ProtocolProfileID != "awg_1_5" || tunnel.InterfaceName != "awg15" || tunnel.ListenPort != 51825 {
		t.Fatalf("init tunnel not applied: %#v", tunnel)
	}
	if tunnel.DNS != "9.9.9.9" || tunnel.Keepalive != 25 || tunnel.MTU != 1280 {
		t.Fatalf("init tunnel settings not applied: %#v", tunnel)
	}
}

func TestInitWithOptionsUsesConservativeAWG3MTU(t *testing.T) {
	enableAWG3RuntimeForTest(t)
	cfg := config.Config{
		ConfigDir:         t.TempDir(),
		ServerHost:        "vpn.example.com",
		ExternalInterface: "eth0",
	}
	state, err := app.New(cfg).InitWithOptions(app.InitOptions{
		ServerHost:        "vpn.example.com",
		ExternalInterface: "eth0",
		ProfileID:         "awg_3",
		Name:              "awg3",
		ListenPort:        51840,
		IPv4Subnet:        "10.30.0.0/24",
		DNS:               "1.1.1.1",
		AllowedIPs:        "0.0.0.0/0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Tunnels[0].MTU; got != 1280 {
		t.Fatalf("AWG3 initial MTU = %d, want 1280", got)
	}
}

func TestInitWithOptionsRejectsInvalidTunnelName(t *testing.T) {
	cfg := config.Config{
		ConfigDir:         t.TempDir(),
		ServerHost:        "vpn.example.com",
		ExternalInterface: "eth0",
	}
	_, err := app.New(cfg).InitWithOptions(app.InitOptions{
		ServerHost:        "vpn.example.com",
		ExternalInterface: "eth0",
		ProfileID:         "awg_2_0",
		Name:              "2bad",
		ListenPort:        51830,
		IPv4Subnet:        "10.20.0.0/24",
		DNS:               "1.1.1.1",
		AllowedIPs:        "0.0.0.0/0",
	})
	if err == nil || !strings.Contains(err.Error(), "tunnel name must start") {
		t.Fatalf("expected invalid tunnel name error, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.ConfigDir, "state.json")); !os.IsNotExist(err) {
		t.Fatalf("state.json was created after invalid init: %v", err)
	}
}

func TestExistingStateWinsOverChangedEnvServerHost(t *testing.T) {
	cfg := testConfig(t)
	svc := app.New(cfg)
	state, err := svc.Init()
	if err != nil {
		t.Fatal(err)
	}
	originalRevision := state.Tunnels[0].ConfigRevision
	cfg.ServerHost = "changed.example.com"
	cfg.LegacyTunnelEnvVars = []string{"SERVER_HOST"}
	state, err = app.New(cfg).Init()
	if err != nil {
		t.Fatal(err)
	}
	if state.ServerHost != "vpn.example.com" {
		t.Fatalf("state server host = %s, want existing state value", state.ServerHost)
	}
	if state.Tunnels[0].ConfigRevision != originalRevision {
		t.Fatalf("revision changed from %d to %d", originalRevision, state.Tunnels[0].ConfigRevision)
	}
}

func TestClientIPAllocationSupportsNon24Subnet(t *testing.T) {
	cfg := testConfig(t)
	svc := app.New(cfg)
	tunnel, err := svc.CreateTunnel("awg_legacy_1_0", "awg25", "10.30.0.128/25", 51825)
	if err != nil {
		t.Fatal(err)
	}
	client, err := svc.AddClientToTunnel(tunnel.ID, "phone")
	if err != nil {
		t.Fatal(err)
	}
	if tunnel.ServerAddress != "10.30.0.129" {
		t.Fatalf("server address = %s, want 10.30.0.129", tunnel.ServerAddress)
	}
	if client.IPv4Address != "10.30.0.130" {
		t.Fatalf("client IP = %s, want 10.30.0.130", client.IPv4Address)
	}
	serverConf, err := os.ReadFile(filepath.Join(cfg.ConfigDir, "tunnels", "awg25", "server.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(serverConf), "Address = 10.30.0.129/25") {
		t.Fatalf("server config did not render /25 address:\n%s", serverConf)
	}
}

func TestNon24SubnetRendersForModernProfiles(t *testing.T) {
	cases := []struct {
		profile string
		name    string
		subnet  string
		port    int
		address string
	}{
		{"awg_1_5", "awg15x", "10.50.0.128/25", 51835, "Address = 10.50.0.129/25"},
		{"awg_2_0", "awg20x", "10.60.0.128/25", 51836, "Address = 10.60.0.129/25"},
	}
	for _, tc := range cases {
		t.Run(tc.profile, func(t *testing.T) {
			cfg := testConfig(t)
			svc := app.New(cfg)
			tunnel, err := svc.CreateTunnel(tc.profile, tc.name, tc.subnet, tc.port)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.AddClientToTunnel(tunnel.ID, "phone"); err != nil {
				t.Fatal(err)
			}
			serverConf, err := os.ReadFile(filepath.Join(cfg.ConfigDir, "tunnels", tc.name, "server.conf"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(serverConf), tc.address) {
				t.Fatalf("server config did not render expected address %q:\n%s", tc.address, serverConf)
			}
		})
	}
}

func TestSmallSubnetAllowsOnlyAvailableClientIPs(t *testing.T) {
	svc := app.New(testConfig(t))
	tunnel, err := svc.CreateTunnel("awg_legacy_1_0", "awg30", "10.40.0.0/30", 51830)
	if err != nil {
		t.Fatal(err)
	}
	client, err := svc.AddClientToTunnel(tunnel.ID, "only-client")
	if err != nil {
		t.Fatal(err)
	}
	if client.IPv4Address != "10.40.0.2" {
		t.Fatalf("client IP = %s, want 10.40.0.2", client.IPv4Address)
	}
	if _, err := svc.AddClientToTunnel(tunnel.ID, "second-client"); err == nil {
		t.Fatal("expected no free client IPs")
	}
}

func TestCreateTunnelCanonicalizesHostCIDR(t *testing.T) {
	svc := app.New(testConfig(t))
	tunnel, err := svc.CreateTunnel("awg_legacy_1_0", "awghost", "10.31.0.42/24", 51831)
	if err != nil {
		t.Fatal(err)
	}
	if tunnel.IPv4Subnet != "10.31.0.0/24" {
		t.Fatalf("subnet = %s, want 10.31.0.0/24", tunnel.IPv4Subnet)
	}
	if tunnel.ServerAddress != "10.31.0.1" {
		t.Fatalf("server address = %s, want 10.31.0.1", tunnel.ServerAddress)
	}
}

func TestCreateTunnelRejectsUnsafeSubnetSizes(t *testing.T) {
	svc := app.New(testConfig(t))
	if _, err := svc.CreateTunnel("awg_legacy_1_0", "too-large", "10.0.0.0/8", 51832); err == nil {
		t.Fatal("expected subnet too large error")
	}
	if _, err := svc.CreateTunnel("awg_legacy_1_0", "too-small", "10.32.0.0/31", 51833); err == nil {
		t.Fatal("expected subnet too small error")
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

func TestCreateClientApplyFailureRollsBackState(t *testing.T) {
	cfg := testConfig(t)
	cfg.ApplyConfig = true
	svc := app.New(cfg)
	client, err := svc.AddClient("phone")
	if err == nil {
		t.Fatal("expected apply error")
	}
	if !strings.Contains(err.Error(), "apply failed") {
		t.Fatalf("error = %q, want apply failed", err.Error())
	}
	if client.ID != "" {
		t.Fatal("expected no created client to be returned with apply error")
	}
	state, stateErr := svc.State()
	if stateErr != nil {
		t.Fatal(stateErr)
	}
	if len(state.Tunnels[0].Clients) != 0 {
		t.Fatalf("clients = %d, want 0", len(state.Tunnels[0].Clients))
	}
	if _, err := os.Stat(filepath.Join(cfg.ConfigDir, "tunnels", "awg0", "server.conf")); err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(filepath.Join(cfg.ConfigDir, "tunnels", "awg0", "clients", "*.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("rendered client configs = %d, want 0", len(matches))
	}
}

func TestCreateTunnelApplyFailureRollsBackState(t *testing.T) {
	cfg := testConfig(t)
	cfg.ApplyConfig = true
	svc := app.New(cfg)
	if _, err := svc.CreateTunnel("awg_1_5", "awg15", "10.15.0.0/24", 51825); err == nil {
		t.Fatal("expected apply error")
	}
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Tunnels) != 1 {
		t.Fatalf("tunnels = %d, want 1", len(state.Tunnels))
	}
	if _, err := os.Stat(filepath.Join(cfg.ConfigDir, "tunnels", "awg15")); !os.IsNotExist(err) {
		t.Fatalf("expected rolled back rendered tunnel dir, stat err = %v", err)
	}
}

func TestTunnelSettingsApplyFailureRollsBackState(t *testing.T) {
	cfg := testConfig(t)
	cfg.ApplyConfig = true
	svc := app.New(cfg)
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	original := state.Tunnels[0]
	if _, err := svc.UpdateTunnelSettings(original.ID, app.TunnelSettingsUpdate{Name: original.Name, ServerHost: original.ServerHost, Subnet: original.IPv4Subnet, DNS: original.DNS, AllowedIPs: original.AllowedIPs, Keepalive: original.Keepalive, MTU: 1280, Port: original.ListenPort, Enabled: original.Enabled}); err == nil {
		t.Fatal("expected apply error")
	}
	state, err = svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Tunnels[0].MTU; got != original.MTU {
		t.Fatalf("MTU = %d, want rolled back %d", got, original.MTU)
	}
}

func TestProtocolApplyFailureRollsBackState(t *testing.T) {
	cfg := testConfig(t)
	cfg.ApplyConfig = true
	svc := app.New(cfg)
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	originalProfile := state.Tunnels[0].ProtocolProfileID
	if err := svc.RegenerateTunnelProtocol(state.Tunnels[0].ID, "awg_2_0"); err == nil {
		t.Fatal("expected apply error")
	}
	state, err = svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Tunnels[0].ProtocolProfileID; got != originalProfile {
		t.Fatalf("profile = %s, want rolled back %s", got, originalProfile)
	}
}

func TestClientMutationsApplyFailureRollBackState(t *testing.T) {
	cfg := testConfig(t)
	svc := app.New(cfg)
	client, err := svc.AddClient("phone")
	if err != nil {
		t.Fatal(err)
	}
	cfg.ApplyConfig = true
	svc = app.New(cfg)

	if err := svc.SetClientEnabled(client.ID, false); err == nil {
		t.Fatal("expected disable apply error")
	}
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if !state.Tunnels[0].Clients[0].Enabled {
		t.Fatal("expected disable to roll back")
	}

	if err := svc.RemoveClient(client.ID); err == nil {
		t.Fatal("expected remove apply error")
	}
	state, err = svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Tunnels[0].Clients) != 1 {
		t.Fatalf("clients = %d, want rolled back 1", len(state.Tunnels[0].Clients))
	}
}

func TestDeleteTunnelApplyFailureRollsBackState(t *testing.T) {
	cfg := testConfig(t)
	svc := app.New(cfg)
	if _, err := svc.CreateTunnel("awg_1_5", "awg15", "10.15.0.0/24", 51825); err != nil {
		t.Fatal(err)
	}
	cfg.ApplyConfig = true
	svc = app.New(cfg)
	if err := svc.DeleteTunnel("awg15"); err == nil {
		t.Fatal("expected apply error")
	}
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Tunnels) != 2 {
		t.Fatalf("tunnels = %d, want rolled back 2", len(state.Tunnels))
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

func TestCreateAWG20TunnelAndClient(t *testing.T) {
	cfg := testConfig(t)
	svc := app.New(cfg)
	tunnel, err := svc.CreateTunnel("awg_2_0", "awg20", "10.20.0.0/24", 51830)
	if err != nil {
		t.Fatal(err)
	}
	client, err := svc.AddClientToTunnel(tunnel.ID, "phone20")
	if err != nil {
		t.Fatal(err)
	}
	if client.IPv4Address != "10.20.0.2" {
		t.Fatalf("client IP = %s, want 10.20.0.2", client.IPv4Address)
	}
	conf, err := svc.ClientConfig(client.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"S3 = ", "S4 = ", "H1 = ", "I1 = "} {
		if !strings.Contains(conf, want) {
			t.Fatalf("AWG 2.0 client config missing %q:\n%s", want, conf)
		}
	}
}

func TestAWG3RequiresLaboratoryRuntime(t *testing.T) {
	cfg := testConfig(t)
	svc := app.New(cfg)
	if _, err := svc.CreateTunnel("awg_3", "awg3", "10.30.0.0/24", 51840); err == nil {
		t.Fatal("expected AWG3 creation without the laboratory runtime to fail")
	}

	enableAWG3RuntimeForTest(t)
	svc = app.New(cfg)
	tunnel, err := svc.CreateTunnel("awg_3", "awg3", "10.30.0.0/24", 51840)
	if err != nil {
		t.Fatal(err)
	}
	if tunnel.ProtocolSecrets.HeaderProtectionKey == "" {
		t.Fatal("AWG3 tunnel is missing HeaderProtectionKey")
	}
	if tunnel.ProtocolParams["RandomTrailers"] != "off" || tunnel.ProtocolParams["DisableCookies"] != "off" {
		t.Fatalf("unsafe AWG 3.x defaults: %#v", tunnel.ProtocolParams)
	}
	if tunnel.MTU != cfg.MTU {
		t.Fatalf("explicit AWG3 MTU = %d, want %d", tunnel.MTU, cfg.MTU)
	}
}

func TestNewTunnelMTUDefaultIsScopedToAWG3(t *testing.T) {
	enableAWG3RuntimeForTest(t)
	cfg := testConfig(t)
	cfg.MTU = 0
	svc := app.New(cfg)

	awg20, err := svc.CreateTunnel("awg_2_0", "awg20", "10.20.0.0/24", 51830)
	if err != nil {
		t.Fatal(err)
	}
	if awg20.MTU != 0 {
		t.Fatalf("AWG2 MTU = %d, want auto", awg20.MTU)
	}

	awg3, err := svc.CreateTunnel("awg_3", "awg3", "10.30.0.0/24", 51840)
	if err != nil {
		t.Fatal(err)
	}
	if awg3.MTU != 1280 {
		t.Fatalf("AWG3 MTU = %d, want 1280", awg3.MTU)
	}

	if _, err := svc.UpdateTunnelSettings(awg3.ID, app.TunnelSettingsUpdate{
		Name:       awg3.Name,
		ServerHost: awg3.ServerHost,
		EgressMode: awg3.EgressMode,
		Subnet:     awg3.IPv4Subnet,
		DNS:        awg3.DNS,
		AllowedIPs: awg3.AllowedIPs,
		Keepalive:  awg3.Keepalive,
		MTU:        0,
		Port:       awg3.ListenPort,
		Enabled:    awg3.Enabled,
	}); err != nil {
		t.Fatal(err)
	}
	state, err := app.New(cfg).Init()
	if err != nil {
		t.Fatal(err)
	}
	for _, tunnel := range state.Tunnels {
		if tunnel.ID != awg3.ID {
			continue
		}
		if tunnel.MTU != 0 {
			t.Fatalf("existing AWG3 auto MTU was replaced with %d", tunnel.MTU)
		}
		return
	}
	t.Fatal("existing AWG3 tunnel was not restored")
}

func TestAWG3ClientExportSupportsConfBasedFormats(t *testing.T) {
	enableAWG3RuntimeForTest(t)
	cfg := testConfig(t)
	cfg.MTU = 1360
	svc := app.New(cfg)
	tunnel, err := svc.CreateTunnel("awg_3", "awg3", "10.30.0.0/24", 51840)
	if err != nil {
		t.Fatal(err)
	}
	client, err := svc.AddClientToTunnel(tunnel.ID, "phone30")
	if err != nil {
		t.Fatal(err)
	}

	conf, _, err := svc.ClientConfigForDownload(client.ID)
	if err != nil {
		t.Fatalf(".conf download failed: %v", err)
	}
	if !strings.Contains(conf, "HeaderProtectionKey =") {
		t.Fatalf("AWG3 .conf is missing HeaderProtectionKey:\n%s", conf)
	}
	if !strings.Contains(conf, "\nMTU = 1360\n") {
		t.Fatalf("AWG3 .conf does not preserve custom MTU:\n%s", conf)
	}
	ctx, err := svc.ClientExportContext(client.ID)
	if err != nil {
		t.Fatalf("ClientExportContext failed: %v", err)
	}
	if ctx.RenderedConf != conf {
		t.Fatal("raw QR export context differs from the downloaded .conf")
	}
	key, returnedClient, err := svc.ClientImportKey(client.ID)
	if err != nil {
		t.Fatalf("ClientImportKey failed: %v", err)
	}
	if returnedClient.ID != client.ID {
		t.Fatalf("client id = %q, want %q", returnedClient.ID, client.ID)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(key, "vpn://"))
	if err != nil {
		t.Fatalf("decode vpn import key: %v", err)
	}
	if string(decoded) != conf {
		t.Fatal("AWG3 vpn import key does not contain the rendered .conf")
	}
}

func TestAWG3ProtocolUpdatePreservesAndRegeneratesHeaderProtectionKey(t *testing.T) {
	enableAWG3RuntimeForTest(t)
	cfg := testConfig(t)
	svc := app.New(cfg)
	tunnel, err := svc.CreateTunnel("awg_3", "awg3", "10.30.0.0/24", 51840)
	if err != nil {
		t.Fatal(err)
	}
	client, err := svc.AddClientToTunnel(tunnel.ID, "phone30")
	if err != nil {
		t.Fatal(err)
	}
	initialKey := tunnel.ProtocolSecrets.HeaderProtectionKey
	params := maps.Clone(tunnel.ProtocolParams)
	params["RekeyTimeout"] = "4-7"
	if err := svc.UpdateTunnelProtocol(tunnel.ID, "awg_3", params); err != nil {
		t.Fatal(err)
	}
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	updated := state.Tunnels[1]
	if got := updated.ProtocolSecrets.HeaderProtectionKey; got != initialKey {
		t.Fatal("ordinary AWG3 protocol update changed HeaderProtectionKey")
	}
	if updated.Clients[0].ID != client.ID || updated.Clients[0].ConfigRevision >= updated.ConfigRevision {
		t.Fatal("AWG3 protocol update did not mark client config stale")
	}
	if err := svc.RegenerateTunnelProtocol(tunnel.ID, "awg_3"); err != nil {
		t.Fatal(err)
	}
	state, err = svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Tunnels[1].ProtocolSecrets.HeaderProtectionKey; got == initialKey || got == "" {
		t.Fatal("AWG3 regeneration did not rotate HeaderProtectionKey")
	}
	if err := svc.UpdateTunnelProtocol(tunnel.ID, "awg_2_0", config.ProtocolParams{}); err != nil {
		t.Fatal(err)
	}
	state, err = svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Tunnels[1].ProtocolSecrets.HeaderProtectionKey; got != "" {
		t.Fatalf("AWG2 tunnel retained AWG3 HeaderProtectionKey: %q", got)
	}
}

func TestAWG3ProtocolToggleDisablePreservesRuntimeResetAndCanonicalClientConfig(t *testing.T) {
	enableAWG3RuntimeForTest(t)
	cfg := testConfig(t)
	svc := app.New(cfg)
	tunnel, err := svc.CreateTunnel("awg_3", "awg3", "10.30.0.0/24", 51840)
	if err != nil {
		t.Fatal(err)
	}
	client, err := svc.AddClientToTunnel(tunnel.ID, "phone30")
	if err != nil {
		t.Fatal(err)
	}

	params := maps.Clone(tunnel.ProtocolParams)
	params["RandomTrailers"] = "on"
	params["DisableCookies"] = "on"
	if err := svc.UpdateTunnelProtocol(tunnel.ID, "awg_3", params); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.ClientConfigForDownload(client.ID); err != nil {
		t.Fatal(err)
	}
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	var enabledRevision int
	for _, candidate := range state.Tunnels {
		if candidate.ID == tunnel.ID {
			enabledRevision = candidate.ConfigRevision
			params = maps.Clone(candidate.ProtocolParams)
			break
		}
	}
	if enabledRevision == 0 {
		t.Fatal("updated AWG3 tunnel not found")
	}

	params["RandomTrailers"] = "off"
	params["DisableCookies"] = "off"
	if err := svc.UpdateTunnelProtocol(tunnel.ID, "awg_3", params); err != nil {
		t.Fatal(err)
	}
	state, err = svc.State()
	if err != nil {
		t.Fatal(err)
	}
	var updated config.Tunnel
	for _, candidate := range state.Tunnels {
		if candidate.ID == tunnel.ID {
			updated = candidate
			break
		}
	}
	if updated.ID == "" {
		t.Fatal("disabled AWG3 tunnel not found")
	}
	if updated.ProtocolParams["RandomTrailers"] != "off" || updated.ProtocolParams["DisableCookies"] != "off" {
		t.Fatalf("disabled AWG3 toggles were not persisted: %#v", updated.ProtocolParams)
	}
	if updated.ConfigRevision <= enabledRevision {
		t.Fatalf("config revision = %d, want greater than %d", updated.ConfigRevision, enabledRevision)
	}
	if updated.Clients[0].ConfigRevision >= updated.ConfigRevision {
		t.Fatal("disabled AWG3 toggles did not mark the client config stale")
	}

	serverConfig, err := os.ReadFile(filepath.Join(cfg.ConfigDir, "tunnels", updated.InterfaceName, "server.conf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"RandomTrailers = off", "DisableCookies = off"} {
		if !strings.Contains(string(serverConfig), want) {
			t.Fatalf("server config is missing runtime reset %q:\n%s", want, serverConfig)
		}
	}
	clientConfig, err := svc.ClientConfig(client.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, unwanted := range []string{"RandomTrailers =", "DisableCookies ="} {
		if strings.Contains(clientConfig, unwanted) {
			t.Fatalf("client config contains disabled AWG3 toggle %q:\n%s", unwanted, clientConfig)
		}
	}
}

func TestRenderAllSkipsAWG3WhenRuntimeSupportIsUnavailable(t *testing.T) {
	enableAWG3RuntimeForTest(t)
	cfg := testConfig(t)
	svc := app.New(cfg)
	tunnel, err := svc.CreateTunnel("awg_3", "awg3", "10.30.0.0/24", 51840)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(cfg.ConfigDir, "tunnels", tunnel.InterfaceName)); err != nil {
		t.Fatal(err)
	}

	buildinfo.AWG3Runtime = "false"
	if err := svc.RenderAll(); err != nil {
		t.Fatalf("RenderAll returned an error instead of preserving operator recovery: %v", err)
	}
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Tunnels[1].LastApplyError; !strings.Contains(got, "runtime support is unavailable in this build") {
		t.Fatalf("AWG3 unavailable state = %q", got)
	}
	if _, err := os.Stat(filepath.Join(cfg.ConfigDir, "tunnels", tunnel.InterfaceName, "server.conf")); !os.IsNotExist(err) {
		t.Fatalf("AWG3 config was rendered without runtime support: %v", err)
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

func TestSuggestedNextTunnelSpecSkipsUsedValuesAcrossProfiles(t *testing.T) {
	state := config.State{Tunnels: []config.Tunnel{
		{Name: "awg0", InterfaceName: "awg0", ListenPort: 7443, IPv4Subnet: "10.8.0.0/24"},
		{Name: "other", InterfaceName: "other", ListenPort: 51820, IPv4Subnet: "10.8.1.0/24"},
	}}

	suggestion := app.SuggestedNextTunnelSpec("awg_legacy_1_0", state)
	if suggestion.Name != "awg0-2" {
		t.Fatalf("suggested name = %s, want awg0-2", suggestion.Name)
	}
	if suggestion.ListenPort != 51821 {
		t.Fatalf("suggested port = %d, want 51821", suggestion.ListenPort)
	}
	if suggestion.IPv4Subnet != "10.8.2.0/24" {
		t.Fatalf("suggested subnet = %s, want 10.8.2.0/24", suggestion.IPv4Subnet)
	}
}

func TestCreateTunnelUsesFreeDefaults(t *testing.T) {
	cfg := testConfig(t)
	cfg.TunnelUDPPortRange = "30000"
	svc := app.New(cfg)
	if _, err := svc.Init(); err != nil {
		t.Fatal(err)
	}

	tunnel, err := svc.CreateTunnel("awg_legacy_1_0", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if tunnel.Name != "awg0-2" || tunnel.ListenPort != 30000 || tunnel.IPv4Subnet != "10.8.1.0/24" {
		t.Fatalf("unexpected tunnel defaults: name=%s port=%d subnet=%s", tunnel.Name, tunnel.ListenPort, tunnel.IPv4Subnet)
	}
}

func TestCreateTunnelDistinguishesAutomaticAndManualPorts(t *testing.T) {
	cfg := testConfig(t)
	cfg.TunnelUDPPortRange = "30000-30010"
	svc := app.New(cfg)
	if _, err := svc.Init(); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateTunnelWithOptions(context.Background(), app.TunnelCreateOptions{
		ProfileID:     "awg_1_5",
		Name:          "auto-outside-range",
		Subnet:        "10.15.0.0/24",
		Port:          51821,
		AutomaticPort: true,
	}); err == nil {
		t.Fatal("expected automatic port outside the configured range to fail")
	}
	if _, err := svc.CreateTunnelWithOptions(context.Background(), app.TunnelCreateOptions{
		ProfileID: "awg_1_5",
		Name:      "manual-port",
		Subnet:    "10.15.0.0/24",
		Port:      51821,
	}); err != nil {
		t.Fatalf("manual port should remain supported: %v", err)
	}
}

func TestInitialModernTunnelUsesConfiguredProfileDefaults(t *testing.T) {
	cfg := testConfig(t)
	cfg.ProtocolProfile = "awg_2_0"
	cfg.TunnelName = "awg20"
	cfg.ListenPort = 51830
	cfg.IPv4Subnet = "10.20.0.0/24"

	svc := app.New(cfg)
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Tunnels) != 1 {
		t.Fatalf("expected one tunnel, got %d", len(state.Tunnels))
	}
	tunnel := state.Tunnels[0]
	if tunnel.Name != "awg20" || tunnel.ProtocolProfileID != "awg_2_0" || tunnel.ListenPort != 51830 || tunnel.IPv4Subnet != "10.20.0.0/24" {
		t.Fatalf("unexpected initial tunnel: name=%s profile=%s port=%d subnet=%s", tunnel.Name, tunnel.ProtocolProfileID, tunnel.ListenPort, tunnel.IPv4Subnet)
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

func TestDeleteTunnelRequiresConfirmationAfterClientConnects(t *testing.T) {
	cfg := testConfig(t)
	svc := app.New(cfg)
	if _, err := svc.CreateTunnel("awg_1_5", "awg15", "10.15.0.0/24", 51825); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddClientToTunnel("awg15", "phone"); err != nil {
		t.Fatal(err)
	}
	store := storage.New(cfg.ConfigDir)
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	tunnelID := ""
	for ti := range state.Tunnels {
		if state.Tunnels[ti].Name == "awg15" {
			state.Tunnels[ti].Clients[0].EverConnected = true
			tunnelID = state.Tunnels[ti].ID
		}
	}
	if tunnelID == "" {
		t.Fatal("missing awg15 tunnel")
	}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	svc = app.New(cfg)
	if err := svc.DeleteTunnel(tunnelID); err == nil || !strings.Contains(err.Error(), `type tunnel name "awg15"`) {
		t.Fatalf("delete without confirmation error = %v", err)
	}
	if err := svc.DeleteTunnelWithConfirmation(tunnelID, "wrong-name"); err == nil || !strings.Contains(err.Error(), `type tunnel name "awg15"`) {
		t.Fatalf("delete with wrong confirmation error = %v", err)
	}
	state, err = svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Tunnels) != 2 {
		t.Fatalf("tunnels after rejected deletion = %d, want 2", len(state.Tunnels))
	}
	if err := svc.DeleteTunnelWithConfirmation(tunnelID, "awg15"); err != nil {
		t.Fatal(err)
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
	if _, err := svc.UpdateTunnelSettings(tunnel.ID, app.TunnelSettingsUpdate{Name: tunnel.Name, ServerHost: tunnel.ServerHost, Subnet: tunnel.IPv4Subnet, DNS: tunnel.DNS, AllowedIPs: tunnel.AllowedIPs, Keepalive: tunnel.Keepalive, MTU: 1280, Port: tunnel.ListenPort, Enabled: tunnel.Enabled}); err != nil {
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

func TestClientImportKeyEncodesRenderedConfig(t *testing.T) {
	svc := app.New(testConfig(t))
	client, err := svc.AddClient("phone")
	if err != nil {
		t.Fatal(err)
	}
	key, returnedClient, err := svc.ClientImportKey(client.ID)
	if err != nil {
		t.Fatal(err)
	}
	if returnedClient.ID != client.ID {
		t.Fatalf("client id = %q, want %q", returnedClient.ID, client.ID)
	}
	if !strings.HasPrefix(key, "vpn://") {
		t.Fatalf("import key prefix mismatch: %q", key)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(key, "vpn://"))
	if err != nil {
		t.Fatal(err)
	}
	conf := string(decoded)
	for _, want := range []string{"[Interface]", "[Peer]", "PrivateKey =", "Endpoint ="} {
		if !strings.Contains(conf, want) {
			t.Fatalf("decoded import key missing %q:\n%s", want, conf)
		}
	}
}

func TestUpdateClientSettingsDoesNotMarkConfigStale(t *testing.T) {
	svc := app.New(testConfig(t))
	client, err := svc.AddClient("phone")
	if err != nil {
		t.Fatal(err)
	}
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	revision := state.Tunnels[0].ConfigRevision

	updated, err := svc.UpdateClientSettings(client.ID, "MacBook", "admin-only note")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "MacBook" {
		t.Fatalf("client name = %q, want MacBook", updated.Name)
	}
	if updated.Notes != "admin-only note" {
		t.Fatalf("client notes = %q", updated.Notes)
	}
	state, err = svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if state.Tunnels[0].ConfigRevision != revision {
		t.Fatalf("tunnel revision changed from %d to %d", revision, state.Tunnels[0].ConfigRevision)
	}
	if state.Tunnels[0].Clients[0].ConfigRevision != revision {
		t.Fatal("client config should remain fresh after metadata-only update")
	}
	_, renamed, err := svc.ClientConfigForDownload(client.ID)
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Name != "MacBook" {
		t.Fatalf("download client name = %q, want MacBook", renamed.Name)
	}
}

func TestDisableClientForTrafficLimitDoesNotMarkConfigStale(t *testing.T) {
	svc := app.New(testConfig(t))
	client, err := svc.AddClient("phone")
	if err != nil {
		t.Fatal(err)
	}
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	revision := state.Tunnels[0].ConfigRevision

	disabled, err := svc.DisableClientForTrafficLimit(client.ID, 6000, 5000, "lifetime")
	if err != nil {
		t.Fatal(err)
	}
	if !disabled {
		t.Fatal("traffic limit should disable an enabled client")
	}
	state, err = svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if state.Tunnels[0].Clients[0].Enabled {
		t.Fatal("client should be disabled after traffic limit enforcement")
	}
	if state.Tunnels[0].ConfigRevision != revision {
		t.Fatalf("tunnel revision changed from %d to %d", revision, state.Tunnels[0].ConfigRevision)
	}
	if state.Tunnels[0].Clients[0].ConfigRevision != revision {
		t.Fatal("traffic limit enforcement should not mark client config stale")
	}
}

func TestClientCreatePersistenceFailureLeavesStateUnchanged(t *testing.T) {
	svc := app.New(testConfig(t))
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	tunnelID := state.Tunnels[0].ID
	persistCalls := 0
	_, err = svc.AddClientToTunnelWithOptions(tunnelID, "phone", app.ClientCreateOptions{
		Persist: func(client config.Client) error {
			persistCalls++
			if client.ID == "" || client.TunnelID != tunnelID {
				t.Fatalf("unexpected client passed to persistence: %#v", client)
			}
			return errors.New("database unavailable")
		},
		RollbackPersist: func(config.Client) error { return nil },
	})
	if err == nil || !strings.Contains(err.Error(), "database unavailable") {
		t.Fatalf("error = %v, want persistence failure", err)
	}
	if persistCalls != 1 {
		t.Fatalf("persistence calls = %d, want 1", persistCalls)
	}
	state, err = svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Tunnels[0].Clients) != 0 {
		t.Fatalf("clients = %#v, want no client after persistence failure", state.Tunnels[0].Clients)
	}
}

func TestTrafficLimitReleaseDoesNotEnableExpiredClient(t *testing.T) {
	svc := app.New(testConfig(t))
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	client, err := svc.AddClientToTunnelWithOptions(state.Tunnels[0].ID, "phone", app.ClientCreateOptions{ExpiresAt: time.Now().UTC().Add(-time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SetClientEnabled(client.ID, false); err != nil {
		t.Fatal(err)
	}
	released, err := svc.EnableClientForTrafficLimitRelease(client.ID, "rolling_30d")
	if err != nil {
		t.Fatal(err)
	}
	if released {
		t.Fatal("expired client must not be re-enabled by a traffic limit release")
	}
	state, err = svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if state.Tunnels[0].Clients[0].Enabled {
		t.Fatal("expired client must remain disabled")
	}
}

func TestUpdateClientSettingsRejectsInvalidName(t *testing.T) {
	svc := app.New(testConfig(t))
	client, err := svc.AddClient("phone")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateClientSettings(client.ID, "../bad", ""); err == nil {
		t.Fatal("expected invalid client name to be rejected")
	}
}

func TestServerHostEnvChangeIsIgnoredAfterStateExists(t *testing.T) {
	cfg := testConfig(t)
	svc := app.New(cfg)
	client, err := svc.AddClient("phone")
	if err != nil {
		t.Fatal(err)
	}
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	oldRevision := state.Tunnels[0].ConfigRevision
	cfg.ServerHost = "new.example.com"
	svc = app.New(cfg)
	state, err = svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if state.ServerHost != "vpn.example.com" {
		t.Fatalf("server host = %q, want existing state value", state.ServerHost)
	}
	if state.Tunnels[0].ConfigRevision != oldRevision {
		t.Fatalf("tunnel config revision changed from %d to %d", oldRevision, state.Tunnels[0].ConfigRevision)
	}
	if state.Tunnels[0].Clients[0].ConfigRevision != oldRevision {
		t.Fatal("client config should remain fresh when env server host changes")
	}
	conf, _, err := svc.ClientConfigForDownload(client.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(conf, "Endpoint = vpn.example.com:51820") {
		t.Fatalf("client config did not keep state endpoint:\n%s", conf)
	}
}

func TestTunnelServerHostOverrideRendersClientEndpoint(t *testing.T) {
	cfg := testConfig(t)
	svc := app.New(cfg)
	client, err := svc.AddClient("phone")
	if err != nil {
		t.Fatal(err)
	}
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	tunnel := state.Tunnels[0]
	if _, err := svc.UpdateTunnelSettings(tunnel.ID, app.TunnelSettingsUpdate{Name: tunnel.Name, ServerHost: "edge.example.com", Subnet: tunnel.IPv4Subnet, DNS: tunnel.DNS, AllowedIPs: tunnel.AllowedIPs, Keepalive: tunnel.Keepalive, MTU: tunnel.MTU, Port: tunnel.ListenPort, Enabled: tunnel.Enabled}); err != nil {
		t.Fatal(err)
	}
	conf, _, err := svc.ClientConfigForDownload(client.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(conf, "Endpoint = edge.example.com:51820") {
		t.Fatalf("client config did not use tunnel endpoint override:\n%s", conf)
	}
}

func TestTunnelServerHostOverrideRejectsUnsafeValues(t *testing.T) {
	cfg := testConfig(t)
	svc := app.New(cfg)
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	tunnel := state.Tunnels[0]
	badHosts := []string{
		"https://edge.example.com",
		"edge.example.com:51820",
		"edge example.com",
		"2001:db8::1",
		"../edge.example.com",
	}
	for _, host := range badHosts {
		t.Run(host, func(t *testing.T) {
			if _, err := svc.UpdateTunnelSettings(tunnel.ID, app.TunnelSettingsUpdate{Name: tunnel.Name, ServerHost: host, Subnet: tunnel.IPv4Subnet, DNS: tunnel.DNS, AllowedIPs: tunnel.AllowedIPs, Keepalive: tunnel.Keepalive, MTU: tunnel.MTU, Port: tunnel.ListenPort, Enabled: tunnel.Enabled}); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestServerHostEnvChangeDoesNotMarkOverriddenTunnelStale(t *testing.T) {
	cfg := testConfig(t)
	svc := app.New(cfg)
	client, err := svc.AddClient("phone")
	if err != nil {
		t.Fatal(err)
	}
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	tunnel := state.Tunnels[0]
	if _, err := svc.UpdateTunnelSettings(tunnel.ID, app.TunnelSettingsUpdate{Name: tunnel.Name, ServerHost: "edge.example.com", Subnet: tunnel.IPv4Subnet, DNS: tunnel.DNS, AllowedIPs: tunnel.AllowedIPs, Keepalive: tunnel.Keepalive, MTU: tunnel.MTU, Port: tunnel.ListenPort, Enabled: tunnel.Enabled}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.ClientConfigForDownload(client.ID); err != nil {
		t.Fatal(err)
	}
	state, err = svc.State()
	if err != nil {
		t.Fatal(err)
	}
	revision := state.Tunnels[0].ConfigRevision
	cfg.ServerHost = "new.example.com"
	svc = app.New(cfg)
	state, err = svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if state.Tunnels[0].ConfigRevision != revision {
		t.Fatalf("overridden tunnel revision changed from %d to %d", revision, state.Tunnels[0].ConfigRevision)
	}
	if state.Tunnels[0].Clients[0].ConfigRevision != revision {
		t.Fatal("expected overridden tunnel client config to stay fresh")
	}
	conf, _, err := svc.ClientConfigForDownload(client.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(conf, "Endpoint = edge.example.com:51820") {
		t.Fatalf("client config did not keep tunnel endpoint override:\n%s", conf)
	}
}

func TestTunnelEgressModeChangeDoesNotMarkClientConfigStale(t *testing.T) {
	cfg := testConfig(t)
	svc := app.New(cfg)
	client, err := svc.AddClient("phone")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportWarpConfig(`[Interface]
PrivateKey = warp-private-key
Address = 172.16.0.2/32

[Peer]
PublicKey = warp-peer-public-key
Endpoint = engage.cloudflareclient.com:2408
PersistentKeepalive = 25
`); err != nil {
		t.Fatal(err)
	}
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	tunnel := state.Tunnels[0]
	revision := tunnel.ConfigRevision
	if _, err := svc.UpdateTunnelSettings(tunnel.ID, app.TunnelSettingsUpdate{
		Name:       tunnel.Name,
		ServerHost: tunnel.ServerHost,
		EgressMode: config.EgressWarp,
		Subnet:     tunnel.IPv4Subnet,
		DNS:        tunnel.DNS,
		AllowedIPs: tunnel.AllowedIPs,
		Keepalive:  tunnel.Keepalive,
		MTU:        tunnel.MTU,
		Port:       tunnel.ListenPort,
		Enabled:    tunnel.Enabled,
	}); err != nil {
		t.Fatal(err)
	}
	state, err = svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if state.Tunnels[0].ConfigRevision != revision {
		t.Fatalf("tunnel revision changed from %d to %d", revision, state.Tunnels[0].ConfigRevision)
	}
	if state.Tunnels[0].Clients[0].ConfigRevision != revision {
		t.Fatal("client config should remain fresh after server-side egress change")
	}
	conf, _, err := svc.ClientConfigForDownload(client.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(conf, "Endpoint = vpn.example.com:51820") {
		t.Fatalf("client config changed unexpectedly:\n%s", conf)
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
	if state.SchemaVersion != config.CurrentStateSchemaVersion {
		t.Fatalf("schema version = %d, want %d", state.SchemaVersion, config.CurrentStateSchemaVersion)
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
	backupBytes, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	var backupState config.State
	if err := json.Unmarshal(backupBytes, &backupState); err != nil {
		t.Fatal(err)
	}
	if got := backupState.Tunnels[0].ProtocolParams["Jc"]; got != "11" {
		t.Fatalf("backup Jc = %q, want pre-repair value 11", got)
	}
	if got := backupState.Tunnels[0].ProtocolParams["S1"]; got != "142" {
		t.Fatalf("backup S1 = %q, want pre-repair value 142", got)
	}
}

func TestInitRepairsAWG3ProtocolParamsWithoutRotatingSecret(t *testing.T) {
	enableAWG3RuntimeForTest(t)
	cfg := testConfig(t)
	svc := app.New(cfg)
	tunnel, err := svc.CreateTunnel("awg_3", "awg3", "10.30.0.0/24", 51840)
	if err != nil {
		t.Fatal(err)
	}
	client, err := svc.AddClientToTunnel(tunnel.ID, "phone")
	if err != nil {
		t.Fatal(err)
	}
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	idx, ok := testTunnelIndexByID(state, tunnel.ID)
	if !ok {
		t.Fatal("AWG3 tunnel not found")
	}
	originalKey := state.Tunnels[idx].ProtocolSecrets.HeaderProtectionKey
	originalRevision := state.Tunnels[idx].ConfigRevision
	delete(state.Tunnels[idx].ProtocolParams, "RandomTrailers")
	delete(state.Tunnels[idx].ProtocolParams, "DisableCookies")
	if err := storage.New(cfg.ConfigDir).Save(state); err != nil {
		t.Fatal(err)
	}

	repaired, err := app.New(cfg).Init()
	if err != nil {
		t.Fatal(err)
	}
	if err := app.New(cfg).RenderAll(); err != nil {
		t.Fatalf("RenderAll after AWG3 protocol repair: %v", err)
	}
	idx, ok = testTunnelIndexByID(repaired, tunnel.ID)
	if !ok {
		t.Fatal("repaired AWG3 tunnel not found")
	}
	repairedTunnel := repaired.Tunnels[idx]
	if got := repairedTunnel.ProtocolSecrets.HeaderProtectionKey; got != originalKey {
		t.Fatal("AWG3 protocol repair rotated HeaderProtectionKey")
	}
	if repairedTunnel.ProtocolParams["RandomTrailers"] != "off" || repairedTunnel.ProtocolParams["DisableCookies"] != "off" {
		t.Fatalf("AWG3 protocol toggles were not repaired to safe defaults: %#v", repairedTunnel.ProtocolParams)
	}
	if repairedTunnel.ConfigRevision != originalRevision+1 {
		t.Fatalf("config revision = %d, want %d", repairedTunnel.ConfigRevision, originalRevision+1)
	}
	if len(repairedTunnel.Clients) != 1 || repairedTunnel.Clients[0].ID != client.ID {
		t.Fatal("AWG3 protocol repair changed the tunnel clients")
	}
	if repairedTunnel.Clients[0].ConfigRevision >= repairedTunnel.ConfigRevision {
		t.Fatal("AWG3 protocol repair did not mark the existing client config stale")
	}
	if profile, ok := protocol.ByID("awg_3"); !ok {
		t.Fatal("AWG3 profile not found")
	} else if err := profile.Validate(repairedTunnel.ProtocolParams); err != nil {
		t.Fatalf("repaired AWG3 protocol params are invalid: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(cfg.ConfigDir, "backups", "state-*-repair-protocol-params.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("repair backups = %d, want 1", len(matches))
	}
}

func TestInitRejectsInvalidAWG3ProtocolSecretWithoutMutation(t *testing.T) {
	for _, invalidateParams := range []bool{false, true} {
		name := "valid params"
		if invalidateParams {
			name = "invalid params"
		}
		t.Run(name, func(t *testing.T) {
			const invalidSecret = "secret-value-must-not-appear-in-errors"
			enableAWG3RuntimeForTest(t)
			cfg := testConfig(t)
			svc := app.New(cfg)
			tunnel, err := svc.CreateTunnel("awg_3", "awg3", "10.30.0.0/24", 51840)
			if err != nil {
				t.Fatal(err)
			}
			state, err := svc.State()
			if err != nil {
				t.Fatal(err)
			}
			idx, ok := testTunnelIndexByID(state, tunnel.ID)
			if !ok {
				t.Fatal("AWG3 tunnel not found")
			}
			state.Tunnels[idx].ProtocolSecrets.HeaderProtectionKey = invalidSecret
			if invalidateParams {
				delete(state.Tunnels[idx].ProtocolParams, "RandomTrailers")
			}
			persistedParams := maps.Clone(state.Tunnels[idx].ProtocolParams)
			persistedRevision := state.Tunnels[idx].ConfigRevision
			if err := storage.New(cfg.ConfigDir).Save(state); err != nil {
				t.Fatal(err)
			}

			_, err = app.New(cfg).Init()
			if err == nil || !strings.Contains(err.Error(), "invalid protocol secrets for \"awg_3\"") {
				t.Fatalf("Init error = %v, want invalid AWG3 protocol secret rejection", err)
			}
			if strings.Contains(err.Error(), invalidSecret) {
				t.Fatal("Init error exposed the invalid protocol secret")
			}
			persisted, err := storage.New(cfg.ConfigDir).Load()
			if err != nil {
				t.Fatal(err)
			}
			idx, ok = testTunnelIndexByID(persisted, tunnel.ID)
			if !ok {
				t.Fatal("persisted AWG3 tunnel not found")
			}
			persistedTunnel := persisted.Tunnels[idx]
			if persistedTunnel.ProtocolSecrets.HeaderProtectionKey != invalidSecret {
				t.Fatal("invalid HeaderProtectionKey was silently replaced")
			}
			if !maps.Equal(persistedTunnel.ProtocolParams, persistedParams) {
				t.Fatal("protocol params changed after secret validation failure")
			}
			if persistedTunnel.ConfigRevision != persistedRevision {
				t.Fatalf("config revision = %d, want unchanged %d", persistedTunnel.ConfigRevision, persistedRevision)
			}
			matches, err := filepath.Glob(filepath.Join(cfg.ConfigDir, "backups", "state-*-repair-protocol-params.json"))
			if err != nil {
				t.Fatal(err)
			}
			if len(matches) != 0 {
				t.Fatalf("repair backups = %d, want 0 after rejected secret", len(matches))
			}
		})
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

func enableAWG3RuntimeForTest(t *testing.T) {
	t.Helper()
	previous := buildinfo.AWG3Runtime
	buildinfo.AWG3Runtime = "true"
	t.Cleanup(func() { buildinfo.AWG3Runtime = previous })
}

func testTunnelIndexByID(state config.State, tunnelID string) (int, bool) {
	for idx := range state.Tunnels {
		if state.Tunnels[idx].ID == tunnelID {
			return idx, true
		}
	}
	return 0, false
}
