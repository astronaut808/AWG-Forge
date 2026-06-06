package render_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/render"
)

func TestLegacyServerGolden(t *testing.T) {
	state := testState(true)
	got, err := render.ServerConfig(state, state.Tunnels[0])
	if err != nil {
		t.Fatal(err)
	}
	want := readGolden(t, "testdata/golden/legacy_server.conf")
	if got != want {
		t.Fatalf("server config mismatch\nwant:\n%s\ngot:\n%s", want, got)
	}
}

func TestLegacyClientGolden(t *testing.T) {
	state := testState(true)
	got, err := render.ClientConfig(state, state.Tunnels[0], state.Tunnels[0].Clients[0])
	if err != nil {
		t.Fatal(err)
	}
	want := readGolden(t, "testdata/golden/legacy_client.conf")
	if got != want {
		t.Fatalf("client config mismatch\nwant:\n%s\ngot:\n%s", want, got)
	}
}

func TestNoDuplicateJc(t *testing.T) {
	state := testState(true)
	got, err := render.ClientConfig(state, state.Tunnels[0], state.Tunnels[0].Clients[0])
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(got, "\nJc = "); n != 1 {
		t.Fatalf("expected one Jc line, got %d\n%s", n, got)
	}
}

func TestDisabledClientNotRenderedAsServerPeer(t *testing.T) {
	state := testState(false)
	got, err := render.ServerConfig(state, state.Tunnels[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "client-public-key") {
		t.Fatal("disabled client was rendered into server config")
	}
}

func TestExpiredClientNotRenderedAsServerPeer(t *testing.T) {
	state := testState(true)
	state.Tunnels[0].Clients[0].ExpiresAt = time.Now().UTC().Add(-time.Hour)
	got, err := render.ServerConfig(state, state.Tunnels[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "client-public-key") {
		t.Fatal("expired client was rendered into server config")
	}
}

func TestAWG15RendersSignaturePacketsInClientOnly(t *testing.T) {
	state := testState(true)
	state.Tunnels[0].ProtocolProfileID = "awg_1_5"
	state.Tunnels[0].MTU = 1280
	state.Tunnels[0].ProtocolParams["I1"] = "<r 2><b 0x8580000100010000000004796162730679616e6465780272750000010001c00c000100010000026d000457fa27d1>"
	state.Tunnels[0].ProtocolParams["I2"] = ""
	state.Tunnels[0].ProtocolParams["I3"] = ""
	state.Tunnels[0].ProtocolParams["I4"] = ""
	state.Tunnels[0].ProtocolParams["I5"] = ""

	clientConfig, err := render.ClientConfig(state, state.Tunnels[0], state.Tunnels[0].Clients[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(clientConfig, "\nI1 = <r 2><b 0x858") {
		t.Fatalf("client config missing I1:\n%s", clientConfig)
	}
	if !strings.Contains(clientConfig, "\nAddress = 10.8.0.2/32\n") {
		t.Fatalf("client config missing /32 address prefix:\n%s", clientConfig)
	}
	if !strings.Contains(clientConfig, "\nMTU = 1280\n") {
		t.Fatalf("client config missing MTU:\n%s", clientConfig)
	}
	serverConfig, err := render.ServerConfig(state, state.Tunnels[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(serverConfig, "\nMTU = 1280\n") {
		t.Fatalf("server config missing MTU:\n%s", serverConfig)
	}
	if !strings.Contains(serverConfig, "iptables -t nat -C POSTROUTING") || !strings.Contains(serverConfig, "iptables -I FORWARD 1 -i awg0 -j ACCEPT") {
		t.Fatalf("server config should use idempotent firewall rules:\n%s", serverConfig)
	}
	if strings.Contains(serverConfig, "\nI1 = ") {
		t.Fatalf("server config should not include 1.5 client-side I1:\n%s", serverConfig)
	}
}

func TestAWG20ServerGolden(t *testing.T) {
	state := testAWG20State(true)
	got, err := render.ServerConfig(state, state.Tunnels[0])
	if err != nil {
		t.Fatal(err)
	}
	want := readGolden(t, "testdata/golden/awg20_server.conf")
	if got != want {
		t.Fatalf("server config mismatch\nwant:\n%s\ngot:\n%s", want, got)
	}
}

func TestAWG20ClientGolden(t *testing.T) {
	state := testAWG20State(true)
	got, err := render.ClientConfig(state, state.Tunnels[0], state.Tunnels[0].Clients[0])
	if err != nil {
		t.Fatal(err)
	}
	want := readGolden(t, "testdata/golden/awg20_client.conf")
	if got != want {
		t.Fatalf("client config mismatch\nwant:\n%s\ngot:\n%s", want, got)
	}
}

func TestAutoMTUIsOmitted(t *testing.T) {
	state := testState(true)
	state.Tunnels[0].MTU = 0
	serverConfig, err := render.ServerConfig(state, state.Tunnels[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(serverConfig, "\nMTU = ") {
		t.Fatalf("server config should omit auto MTU:\n%s", serverConfig)
	}
	clientConfig, err := render.ClientConfig(state, state.Tunnels[0], state.Tunnels[0].Clients[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(clientConfig, "\nMTU = ") {
		t.Fatalf("client config should omit auto MTU:\n%s", clientConfig)
	}
}

func testAWG20State(enabled bool) config.State {
	state := testState(enabled)
	state.Tunnels[0].Name = "awg20"
	state.Tunnels[0].InterfaceName = "awg20"
	state.Tunnels[0].ListenPort = 51830
	state.Tunnels[0].ServerAddress = "10.20.0.1"
	state.Tunnels[0].IPv4Subnet = "10.20.0.0/24"
	state.Tunnels[0].ProtocolProfileID = "awg_2_0"
	state.Tunnels[0].ProtocolParams = config.ProtocolParams{
		"Jc": "7", "Jmin": "128", "Jmax": "900",
		"S1": "22", "S2": "33", "S3": "44", "S4": "16",
		"H1": "1000-1031", "H2": "2000-2031", "H3": "3000-3031", "H4": "4000-4031",
		"I1": "<r 2><b 0x8580000100010000000004796162730679616e6465780272750000010001c00c000100010000026d000457fa27d1>",
		"I2": "<r 8><t><r 16>",
		"I3": "<rd 12><r 12>",
		"I4": "<rc 16><r 10>",
		"I5": "<r 32>",
	}
	state.Tunnels[0].Clients[0].TunnelID = "tunnel1"
	state.Tunnels[0].Clients[0].IPv4Address = "10.20.0.2"
	return state
}

func readGolden(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile("../../" + path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func testState(enabled bool) config.State {
	now := time.Date(2026, 5, 18, 0, 0, 0, 0, time.UTC)
	return config.State{
		SchemaVersion:     2,
		ExternalInterface: "eth0",
		ServerHost:        "vpn.example.com",
		Tunnels: []config.Tunnel{{
			ID:                "tunnel1",
			Name:              "awg0",
			InterfaceName:     "awg0",
			Enabled:           true,
			ServerPrivateKey:  "server-private-key",
			ServerPublicKey:   "server-public-key",
			ListenPort:        51820,
			ServerAddress:     "10.8.0.1",
			IPv4Subnet:        "10.8.0.0/24",
			DNS:               "1.1.1.1",
			AllowedIPs:        "0.0.0.0/0",
			Keepalive:         0,
			MTU:               1420,
			ProtocolProfileID: "awg_legacy_1_0",
			ProtocolParams: config.ProtocolParams{
				"Jc": "4", "Jmin": "64", "Jmax": "1024",
				"S1": "0", "S2": "0",
				"H1": "1111111111", "H2": "2222222222", "H3": "3333333333", "H4": "444444444",
			},
			Clients: []config.Client{{
				ID: "client1", TunnelID: "tunnel1", Name: "phone", Enabled: enabled, IPv4Address: "10.8.0.2",
				PrivateKey: "client-private-key", PublicKey: "client-public-key", PresharedKey: "client-preshared-key",
				CreatedAt: now, UpdatedAt: now,
			}},
			CreatedAt: now, UpdatedAt: now,
		}},
		CreatedAt: now,
		UpdatedAt: now,
	}
}
