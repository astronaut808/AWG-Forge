package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
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

func TestAWG3AmneziaVPNQRPreservesNativeFields(t *testing.T) {
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
	ctx, err := svc.ClientExportContext(client.ID)
	if err != nil {
		t.Fatal(err)
	}
	defaultJSON, err := buildAmneziaVPNClientConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var defaultOuter amneziaVPNConfig
	if err := json.Unmarshal(defaultJSON, &defaultOuter); err != nil {
		t.Fatal(err)
	}
	var defaultLast map[string]any
	if err := json.Unmarshal([]byte(defaultOuter.Containers[0].AWG.LastConfig), &defaultLast); err != nil {
		t.Fatal(err)
	}
	if defaultLast["config"] != ctx.RenderedConf {
		t.Fatal("AWG3 last_config config does not exactly match the rendered client config")
	}
	outerJSON, err := json.Marshal(defaultOuter.Containers[0].AWG)
	if err != nil {
		t.Fatal(err)
	}
	var outerFields map[string]any
	if err := json.Unmarshal(outerJSON, &outerFields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"Jc", "Jmin", "Jmax", "S1", "S2", "S3", "S4", "H1", "H2", "H3", "H4", "I1", "I2", "I3", "I4", "I5",
		"HeaderProtectionKey", "ContentPaddingAddition", "RekeyAfterTime", "RekeyTimeout", "RejectAfterTime", "KeepaliveTimeout", "MaxHandshakeAttempts",
	} {
		if lastValue := defaultLast[key]; lastValue == nil || lastValue == "" {
			t.Fatalf("AWG3 last_config is missing required field %s", key)
		} else if outerFields[key] != lastValue {
			t.Fatalf("AWG3 field %s differs between outer metadata and last_config: %q != %q", key, outerFields[key], lastValue)
		}
	}
	keepalive := strconv.Itoa(ctx.Tunnel.Keepalive)
	if defaultLast["persistent_keep_alive"] != keepalive || !strings.Contains(ctx.RenderedConf, "PersistentKeepalive = "+keepalive+"\n") {
		t.Fatal("AWG3 persistent keepalive does not match the rendered client config")
	}
	for key, want := range map[string]string{
		"ContentPaddingAddition": "0",
		"RekeyAfterTime":         "120",
		"RekeyTimeout":           "5",
		"RejectAfterTime":        "180",
		"KeepaliveTimeout":       "10",
		"MaxHandshakeAttempts":   "18",
	} {
		if got := defaultLast[key]; got != want {
			t.Fatalf("default AWG3 client field %s = %q, want %q", key, got, want)
		}
		if got := awg3OuterField(defaultOuter.Containers[0].AWG, key); got != want {
			t.Fatalf("default AWG3 server field %s = %q, want %q", key, got, want)
		}
		if !strings.Contains(ctx.RenderedConf, key+" = "+want+"\n") {
			t.Fatalf("rendered AWG3 client config does not contain %s", key)
		}
	}
	ctx.Tunnel.ProtocolParams["ContentPaddingAddition"] = "10-20"
	ctx.Tunnel.ProtocolParams["RekeyAfterTime"] = "120"
	ctx.Tunnel.ProtocolParams["RekeyTimeout"] = "5-10"
	ctx.Tunnel.ProtocolParams["RejectAfterTime"] = "300"
	ctx.Tunnel.ProtocolParams["KeepaliveTimeout"] = "15"
	ctx.Tunnel.ProtocolParams["MaxHandshakeAttempts"] = "3-5"
	ctx.Tunnel.ProtocolParams["PersistentKeepalive"] = "20-30"
	jsonBytes, err := buildAmneziaVPNClientConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var outer amneziaVPNConfig
	if err := json.Unmarshal(jsonBytes, &outer); err != nil {
		t.Fatal(err)
	}
	if outer.Containers[0].AWG.ProtocolVersion != "2" {
		t.Fatalf("protocol_version = %q, want 2", outer.Containers[0].AWG.ProtocolVersion)
	}
	var last map[string]any
	if err := json.Unmarshal([]byte(outer.Containers[0].AWG.LastConfig), &last); err != nil {
		t.Fatal(err)
	}
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	updated, ok := tunnelByID(state, tunnel.ID)
	if !ok {
		t.Fatal("AWG3 tunnel disappeared from state")
	}
	if last["HeaderProtectionKey"] != updated.ProtocolSecrets["HeaderProtectionKey"] {
		t.Fatal("AmneziaVPN QR did not preserve the AWG3 header protection key")
	}
	if outer.Containers[0].AWG.HeaderProtectionKey != updated.ProtocolSecrets["HeaderProtectionKey"] {
		t.Fatal("AmneziaVPN QR did not mirror the AWG3 header protection key in server metadata")
	}
	for key, want := range map[string]string{
		"ContentPaddingAddition": "10-20",
		"RekeyAfterTime":         "120",
		"RekeyTimeout":           "5-10",
		"RejectAfterTime":        "300",
		"KeepaliveTimeout":       "15",
		"MaxHandshakeAttempts":   "3-5",
		"persistent_keep_alive":  "20-30",
	} {
		if last[key] != want {
			t.Fatalf("AWG3 field %s = %q, want %q", key, last[key], want)
		}
		if key == "persistent_keep_alive" {
			continue
		}
		if got := awg3OuterField(outer.Containers[0].AWG, key); got != want {
			t.Fatalf("AWG3 server field %s = %q, want %q", key, got, want)
		}
	}

	rr := httptest.NewRecorder()
	w.clientAmneziaVPNQRAPI(rr, httptest.NewRequest(http.MethodGet, "/", nil), client.ID)
	if rr.Code != http.StatusOK || rr.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("AmneziaVPN QR response = %d %q, want 200 image/png", rr.Code, rr.Header().Get("Content-Type"))
	}
	rr = httptest.NewRecorder()
	w.clientAmneziaVPNQRSeriesAPI(rr, httptest.NewRequest(http.MethodGet, "/", nil), client.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("AmneziaVPN QR series status = %d, want 200", rr.Code)
	}
	rr = httptest.NewRecorder()
	w.clientQRAPI(rr, httptest.NewRequest(http.MethodGet, "/", nil), client.ID)
	if rr.Code != http.StatusConflict {
		t.Fatalf("raw QR status = %d, want %d: %s", rr.Code, http.StatusConflict, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	w.clientImportKeyAPI(rr, httptest.NewRequest(http.MethodPost, "http://127.0.0.1:51821/", nil), client.ID)
	if rr.Code != http.StatusConflict {
		t.Fatalf("import key status = %d, want %d: %s", rr.Code, http.StatusConflict, rr.Body.String())
	}
}

func awg3OuterField(awg amneziaVPNAWG, key string) string {
	switch key {
	case "ContentPaddingAddition":
		return awg.ContentPaddingAddition
	case "RekeyAfterTime":
		return awg.RekeyAfterTime
	case "RekeyTimeout":
		return awg.RekeyTimeout
	case "RejectAfterTime":
		return awg.RejectAfterTime
	case "KeepaliveTimeout":
		return awg.KeepaliveTimeout
	case "MaxHandshakeAttempts":
		return awg.MaxHandshakeAttempts
	}
	return ""
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
