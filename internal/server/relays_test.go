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

func TestAPIContractRelayLifecycle(t *testing.T) {
	cfg := config.Config{ConfigDir: t.TempDir(), Password: "test-password", ServerHost: "192.0.2.20", ExternalInterface: "eth0"}
	w := &web{cfg: cfg, service: app.New(cfg), idem: map[string]*idempotencyEntry{}}
	state, err := w.service.Init()
	if err != nil {
		t.Fatal(err)
	}
	tunnelID := state.Tunnels[0].ID
	base := "/api/tunnels/" + tunnelID + "/relays"
	do := func(handler http.HandlerFunc, method, path, origin, key, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
		r.Header.Set("Origin", origin)
		r.Header.Set("Idempotency-Key", key)
		recorder := httptest.NewRecorder()
		handler(recorder, r)
		if recorder.Code != want {
			t.Fatalf("%s %s: status %d, want %d; body %s", method, path, recorder.Code, want, recorder.Body.String())
		}
		return recorder
	}
	origin := "http://127.0.0.1"
	do(w.requireAuth(w.tunnelAPI), http.MethodGet, base+"/suggestion", origin, "", "", http.StatusUnauthorized)
	suggestion := do(w.tunnelAPI, http.MethodGet, base+"/suggestion", origin, "", "", http.StatusOK)
	if strings.TrimSpace(suggestion.Body.String()) != `{"target_ipv4":"192.0.2.20"}` {
		t.Fatalf("unexpected relay suggestion: %s", suggestion.Body.String())
	}
	body := `{"name":"Edge","host":"edge.example.com","port":40123,"target_ipv4":"192.0.2.20"}`
	do(w.requireAuth(w.tunnelAPI), http.MethodGet, base, origin, "", "", http.StatusUnauthorized)
	do(w.tunnelAPI, http.MethodPost, base, "https://foreign.example", "", body, http.StatusForbidden)
	do(w.tunnelAPI, http.MethodPost, base, origin, "", strings.Replace(body, "192.0.2.20", "192.0.2.20;id", 1), http.StatusBadRequest)
	created := do(w.tunnelAPI, http.MethodPost, base, origin, "create-relay", body, http.StatusCreated)
	replay := do(w.tunnelAPI, http.MethodPost, base, origin, "create-relay", body, http.StatusCreated)
	if created.Body.String() != replay.Body.String() {
		t.Fatal("idempotent request did not replay")
	}
	var result struct {
		Relay config.Relay `json:"relay"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &result); err != nil || result.Relay.ID == "" {
		t.Fatal("relay ID missing from response")
	}
	list := do(w.tunnelAPI, http.MethodGet, base, origin, "", "", http.StatusOK)
	if strings.Count(list.Body.String(), result.Relay.ID) != 1 {
		t.Fatal("expected one saved relay")
	}
	client := do(w.clientsAPI, http.MethodPost, "/api/clients", origin, "",
		`{"name":"router","tunnel_id":"`+tunnelID+`","relay_id":"`+result.Relay.ID+`"}`, http.StatusCreated)
	if !strings.Contains(client.Body.String(), result.Relay.ID) {
		t.Fatal("client did not retain relay association")
	}
	path := base + "/" + result.Relay.ID
	t.Setenv("AWG_FORGE_VERSION", "v1.2.3")
	command := do(w.tunnelAPI, http.MethodGet, path+"/install", origin, "", "", http.StatusOK)
	if command.Header().Get("Cache-Control") != "no-store" ||
		!strings.Contains(command.Body.String(), "/v1.2.3/install.sh") ||
		!strings.Contains(command.Body.String(), "--target 192.0.2.20:") {
		t.Fatal("install command did not pin the release or destination")
	}
	do(w.tunnelAPI, http.MethodDelete, path, origin, "", "", http.StatusBadRequest)
	do(w.tunnelAPI, http.MethodPut, path, origin, "", strings.Replace(body, "40123", "40124", 1), http.StatusOK)
	stale := strings.TrimSuffix(body, "}") + `,"expected":` + body + `}`
	conflict := do(w.tunnelAPI, http.MethodPut, path, origin, "", stale, http.StatusConflict)
	if !strings.Contains(conflict.Body.String(), "relay_changed") {
		t.Fatal("expected relay_changed conflict")
	}
	current := do(w.tunnelAPI, http.MethodGet, base, origin, "", "", http.StatusOK)
	if !strings.Contains(current.Body.String(), "40124") {
		t.Fatal("stale update overwrote the relay port")
	}
	do(w.requireAuth(w.tunnelAPI), http.MethodGet, base+"/health", origin, "", "", http.StatusUnauthorized)
	health := do(w.tunnelAPI, http.MethodGet, base+"/health", origin, "", "", http.StatusOK)
	if health.Header().Get("Cache-Control") != "no-store" || !strings.Contains(health.Body.String(), `"`+result.Relay.ID+`":{"status":"unavailable"}`) {
		t.Fatalf("unexpected relay health response: %s", health.Body.String())
	}

	dns := `{"name":"Edge","host":"edge.example.com","port":40124,"target_host":"exit.example.com"}`
	do(w.tunnelAPI, http.MethodPut, path, origin, "", strings.Replace(dns, "exit.example.com", "exit.example.com;id", 1), http.StatusBadRequest)
	do(w.tunnelAPI, http.MethodPut, path, origin, "", strings.TrimSuffix(dns, "}")+`,"target_ipv4":"192.0.2.20"}`, http.StatusBadRequest)
	do(w.tunnelAPI, http.MethodPut, path, origin, "", dns, http.StatusOK)
	command = do(w.tunnelAPI, http.MethodGet, path+"/install", origin, "", "", http.StatusOK)
	if !strings.Contains(command.Body.String(), "--target exit.example.com:") {
		t.Fatal("install command did not preserve the DNS destination")
	}

}

func TestAmneziaVPNRelayEndpoint(t *testing.T) {
	ctx := amneziaVPNTestExportContext(t)
	relay := config.Relay{ID: "0123456789abcdef", Name: "Edge", Host: "edge.example.com", Port: 40123, TargetIPv4: "192.0.2.20"}
	ctx.Client.RelayID = relay.ID
	ctx.Tunnel.Relays = []config.Relay{relay}
	payload, err := buildAmneziaVPNClientConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var outer amneziaVPNConfig
	if err := json.Unmarshal(payload, &outer); err != nil {
		t.Fatal(err)
	}
	if outer.HostName != relay.Host || len(outer.Containers) != 1 || outer.Containers[0].AWG.Port != "40123" {
		t.Fatal("AmneziaVPN QR contains the wrong outer endpoint")
	}
	var inner amneziaVPNLastConfig
	if err := json.Unmarshal([]byte(outer.Containers[0].AWG.LastConfig), &inner); err != nil {
		t.Fatal(err)
	}
	if inner.HostName != relay.Host || inner.Port != relay.Port {
		t.Fatal("AmneziaVPN QR contains the wrong inner endpoint")
	}
}
