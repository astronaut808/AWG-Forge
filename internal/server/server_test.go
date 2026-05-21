package server

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
)

func TestValidOriginAllowsMissingOriginAndRefererForLoopback(t *testing.T) {
	w := &web{}
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:51821/login", nil)
	if !w.validOrigin(r) {
		t.Fatal("missing Origin and Referer should be allowed for localhost/tunnel login")
	}
}

func TestValidOriginRejectsMissingOriginAndRefererForPublicHost(t *testing.T) {
	w := &web{}
	r := httptest.NewRequest(http.MethodPost, "https://admin.example.com/api/login", nil)
	r.Host = "admin.example.com"
	if w.validOrigin(r) {
		t.Fatal("missing Origin and Referer should be rejected for public hosts")
	}
}

func TestValidOriginRejectsMismatchedOrigin(t *testing.T) {
	w := &web{}
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:51821/login", nil)
	r.Header.Set("Origin", "http://evil.example")
	if w.validOrigin(r) {
		t.Fatal("mismatched Origin should be rejected")
	}
}

func TestValidOriginAllowsMatchingOrigin(t *testing.T) {
	w := &web{}
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:51821/login", nil)
	r.Header.Set("Origin", "http://127.0.0.1:51821")
	if !w.validOrigin(r) {
		t.Fatal("matching Origin should be allowed")
	}
}

func TestValidOriginAllowsPublishedSameOrigin(t *testing.T) {
	w := &web{}
	r := httptest.NewRequest(http.MethodPost, "https://admin.example.com/api/clients", nil)
	r.Host = "admin.example.com"
	r.Header.Set("Origin", "https://admin.example.com")
	if !w.validOrigin(r) {
		t.Fatal("published same-origin request should be allowed")
	}
}

func TestValidOriginAllowsLoopbackAlias(t *testing.T) {
	w := &web{}
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:51821/login", nil)
	r.Header.Set("Origin", "http://localhost:51821")
	if !w.validOrigin(r) {
		t.Fatal("localhost and 127.0.0.1 with same port should be allowed")
	}
}

func TestValidOriginRejectsOpaqueOrigins(t *testing.T) {
	w := &web{}
	for _, origin := range []string{"null", "browser-extension://abc123"} {
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:51821/clients/create", nil)
		r.Header.Set("Origin", origin)
		if w.validOrigin(r) {
			t.Fatalf("origin %q should be rejected", origin)
		}
	}
}

func TestLoginPostAcceptsSameOrigin(t *testing.T) {
	w := &web{sessions: []byte("test-secret"), limits: map[string][]time.Time{}, cfg: config.Config{Password: "secret"}}
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:51821/api/login", strings.NewReader(`{"password":"secret"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://127.0.0.1:51821")
	rr := httptest.NewRecorder()

	w.loginAPI(rr, r)

	if rr.Code == http.StatusForbidden {
		t.Fatal("login POST should not be blocked by Origin validation")
	}
}

func TestSessionExpiresInThirtyMinutes(t *testing.T) {
	w := &web{sessions: []byte("test-secret")}
	rr := httptest.NewRecorder()
	w.setSession(rr)
	cookies := rr.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d, want 1", len(cookies))
	}
	if !cookies[0].Secure {
		t.Fatal("session cookie must use Secure")
	}
	if !cookies[0].HttpOnly {
		t.Fatal("session cookie must use HttpOnly")
	}
	if cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookie SameSite = %v, want Strict", cookies[0].SameSite)
	}
	parts := strings.Split(cookies[0].Value, ".")
	if len(parts) != 2 {
		t.Fatalf("invalid session cookie %q", cookies[0].Value)
	}
	exp, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	ttl := time.Until(time.Unix(exp, 0))
	if ttl < 29*time.Minute || ttl > 31*time.Minute {
		t.Fatalf("session ttl = %s, want about 30m", ttl)
	}
}

func TestConfigFilenameUsesSanitizedClientName(t *testing.T) {
	got := configFilename(config.Client{ID: "abc123", Name: "My iPhone 15"})
	if got != "My-iPhone-15" {
		t.Fatalf("filename = %q, want %q", got, "My-iPhone-15")
	}
}

func TestConfigFilenameFallsBackToID(t *testing.T) {
	got := configFilename(config.Client{ID: "abc123", Name: "...---___"})
	if got != "abc123" {
		t.Fatalf("filename = %q, want id fallback", got)
	}
}

func TestClientConfigDownloadUsesClientNameFilename(t *testing.T) {
	cfg := config.Config{
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
	svc := app.New(cfg)
	client, err := svc.AddClient("My iPhone 15")
	if err != nil {
		t.Fatal(err)
	}
	w := &web{service: svc}
	r := httptest.NewRequest(http.MethodGet, "/clients/config/"+client.ID, nil)
	rr := httptest.NewRecorder()

	w.clientConfig(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if got, want := rr.Header().Get("Content-Disposition"), `attachment; filename="My-iPhone-15.conf"`; got != want {
		t.Fatalf("Content-Disposition = %q, want %q", got, want)
	}
}

func TestIdempotentCreateClientDoesNotCreateDuplicate(t *testing.T) {
	cfg := config.Config{
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
		MTU:                 0,
		ProtocolProfile:     "awg_legacy_1_0",
	}
	svc := app.New(cfg)
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	w := &web{service: svc, idem: map[string]*idempotencyEntry{}}

	body := `{"tunnel_id":"` + state.Tunnels[0].ID + `","name":"phone"}`
	for i := 0; i < 2; i++ {
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/clients", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", "same-create-key")
		r.Header.Set("Origin", "http://127.0.0.1")
		rr := httptest.NewRecorder()
		w.clientsAPI(rr, r)
		if rr.Code != http.StatusCreated {
			t.Fatalf("request %d status = %d", i+1, rr.Code)
		}
	}
	state, err = svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if got := len(state.Tunnels[0].Clients); got != 1 {
		t.Fatalf("clients = %d, want 1", got)
	}
}

func TestApplyFailureReturnsServerErrorForMutation(t *testing.T) {
	cfg := config.Config{
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
		MTU:                 0,
		ProtocolProfile:     "awg_legacy_1_0",
		ApplyConfig:         true,
	}
	svc := app.New(cfg)
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	w := &web{service: svc, idem: map[string]*idempotencyEntry{}}

	body := `{"tunnel_id":"` + state.Tunnels[0].ID + `","name":"phone"}`
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/clients", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", "apply-fails")
	r.Header.Set("Origin", "http://127.0.0.1")
	rr := httptest.NewRecorder()
	w.clientsAPI(rr, r)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body = %s", rr.Code, http.StatusInternalServerError, rr.Body.String())
	}
	state, err = svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if got := len(state.Tunnels[0].Clients); got != 0 {
		t.Fatalf("clients = %d, want 0", got)
	}
}

func TestDeleteTunnelApplyFailureReturnsServerError(t *testing.T) {
	cfg := config.Config{
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
		MTU:                 0,
		ProtocolProfile:     "awg_legacy_1_0",
	}
	svc := app.New(cfg)
	tunnel, err := svc.CreateTunnel("awg_1_5", "awg15", "10.15.0.0/24", 51825)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ApplyConfig = true
	svc = app.New(cfg)
	w := &web{service: svc, idem: map[string]*idempotencyEntry{}}

	r := httptest.NewRequest(http.MethodDelete, "http://127.0.0.1/api/tunnels/"+tunnel.ID+"/delete", nil)
	r.Header.Set("Idempotency-Key", "delete-apply-fails")
	r.Header.Set("Origin", "http://127.0.0.1")
	rr := httptest.NewRecorder()
	w.deleteTunnelAPI(rr, r, tunnel.ID)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body = %s", rr.Code, http.StatusInternalServerError, rr.Body.String())
	}
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if got := len(state.Tunnels); got != 2 {
		t.Fatalf("tunnels = %d, want rolled back 2", got)
	}
}
