package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
)

func TestAWG3PublicStateOmitsHeaderProtectionKey(t *testing.T) {
	cfg := awg3TestConfig(t)
	svc := app.New(cfg)
	tunnel, err := svc.CreateTunnel("awg_3_0", "awg30", "10.30.0.0/24", 51840)
	if err != nil {
		t.Fatal(err)
	}
	state, err := svc.Init()
	if err != nil {
		t.Fatal(err)
	}
	secret := tunnel.ProtocolSecrets["HeaderProtectionKey"]
	w := &web{cfg: cfg, service: svc}
	payload, err := json.Marshal(w.publicState(t.Context(), state))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "HeaderProtectionKey") || strings.Contains(string(payload), secret) {
		t.Fatalf("public state leaked AWG3 secret: %s", payload)
	}
}

func TestAWG3RejectsQRExports(t *testing.T) {
	cfg := awg3TestConfig(t)
	svc := app.New(cfg)
	tunnel, err := svc.CreateTunnel("awg_3_0", "awg30", "10.30.0.0/24", 51840)
	if err != nil {
		t.Fatal(err)
	}
	client, err := svc.AddClientToTunnel(tunnel.ID, "AWG3 Phone")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RegenerateTunnelProtocol(tunnel.ID, "awg_3_0"); err != nil {
		t.Fatal(err)
	}
	w := &web{cfg: cfg, service: svc}
	for _, handler := range []func(http.ResponseWriter, *http.Request, string){w.clientQRAPI, w.clientAmneziaVPNQRAPI, w.clientAmneziaVPNQRSeriesAPI} {
		rr := httptest.NewRecorder()
		handler(rr, httptest.NewRequest(http.MethodGet, "/", nil), client.ID)
		if rr.Code != http.StatusConflict {
			t.Fatalf("QR handler status = %d, want %d: %s", rr.Code, http.StatusConflict, rr.Body.String())
		}
	}
	rr := httptest.NewRecorder()
	w.clientImportKeyAPI(rr, httptest.NewRequest(http.MethodPost, "http://127.0.0.1:51821/", nil), client.ID)
	if rr.Code != http.StatusConflict {
		t.Fatalf("import key status = %d, want %d: %s", rr.Code, http.StatusConflict, rr.Body.String())
	}
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	updated, ok := tunnelByID(state, tunnel.ID)
	if !ok {
		t.Fatal("AWG3 tunnel disappeared from state")
	}
	if updated.Clients[0].ConfigRevision == updated.ConfigRevision {
		t.Fatal("rejected AWG3 QR or vpn:// export marked the stale client config as delivered")
	}
}

func TestAWG3ProfileIsOnlyListedWhenEnabled(t *testing.T) {
	state := config.State{}
	if profilesContain(availableProfiles(false, state), "awg_3_0") {
		t.Fatal("AWG3 profile was exposed without the experimental flag")
	}
	if !profilesContain(availableProfiles(true, state), "awg_3_0") {
		t.Fatal("AWG3 profile was not exposed with the experimental flag")
	}
}

func awg3TestConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{
		ConfigDir: t.TempDir(), TunnelName: "awg0", ServerHost: "vpn.example.com", ListenPort: 51820,
		WebUIHost: "127.0.0.1", WebUIPort: 51821, ExternalInterface: "eth0", IPv4Subnet: "10.8.0.0/24",
		DNS: "1.1.1.1", AllowedIPs: "0.0.0.0/0", MTU: 1420, ProtocolProfile: "awg_2_0", AWG3Experimental: true,
		AWG3Runtime: true,
	}
}

func profilesContain(profiles []map[string]any, id string) bool {
	for _, profile := range profiles {
		if profile["id"] == id {
			return true
		}
	}
	return false
}

func tunnelByID(state config.State, id string) (config.Tunnel, bool) {
	for _, tunnel := range state.Tunnels {
		if tunnel.ID == id {
			return tunnel, true
		}
	}
	return config.Tunnel{}, false
}
