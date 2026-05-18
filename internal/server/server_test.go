package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
)

func TestValidOriginAllowsMissingOriginAndReferer(t *testing.T) {
	w := &web{}
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:51821/login", nil)
	if !w.validOrigin(r) {
		t.Fatal("missing Origin and Referer should be allowed for localhost/tunnel login")
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

func TestValidOriginAllowsLoopbackAlias(t *testing.T) {
	w := &web{}
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:51821/login", nil)
	r.Header.Set("Origin", "http://localhost:51821")
	if !w.validOrigin(r) {
		t.Fatal("localhost and 127.0.0.1 with same port should be allowed")
	}
}

func TestValidOriginAllowsOpaqueLocalBrowserOrigins(t *testing.T) {
	w := &web{}
	for _, origin := range []string{"null", "browser-extension://abc123"} {
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:51821/clients/create", nil)
		r.Header.Set("Origin", origin)
		if !w.validOrigin(r) {
			t.Fatalf("origin %q should be allowed for local authenticated UI", origin)
		}
	}
}

func TestLoginPostDoesNotRequireOrigin(t *testing.T) {
	w := &web{sessions: []byte("test-secret"), limits: map[string][]time.Time{}, cfg: config.Config{Password: "secret"}}
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:51821/api/login", strings.NewReader(`{"password":"secret"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "browser-extension://extension")
	rr := httptest.NewRecorder()

	w.loginAPI(rr, r)

	if rr.Code == http.StatusForbidden {
		t.Fatal("login POST should not be blocked by Origin validation")
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
