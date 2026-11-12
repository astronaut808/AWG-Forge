package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/storage"
)

func TestServerNameAPIRejectsMissingNullAndInvalidRequests(t *testing.T) {
	cfg := config.Config{ConfigDir: t.TempDir(), ServerHost: "vpn.example.com", ExternalInterface: "eth0"}
	w := &web{cfg: cfg, service: app.New(cfg), idem: map[string]*idempotencyEntry{}}
	if _, err := w.service.UpdateServerName("PreservedName"); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{}`, `null`, `{"name":null}`, `{"name":1}`, `{"name":"bad-name"}`, `{`} {
		r := httptest.NewRequest(http.MethodPatch, "http://127.0.0.1/api/server-name", strings.NewReader(body))
		r.Header.Set("Origin", "http://127.0.0.1")
		recorder := httptest.NewRecorder()
		w.serverNameAPI(recorder, r)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("invalid request status = %d", recorder.Code)
		}
		state, err := w.service.State()
		if err != nil {
			t.Fatal(err)
		}
		if state.ServerName != "PreservedName" {
			t.Fatal("invalid request changed name")
		}
	}
}

func TestServerNameAPIFencesAuthOriginAndIdempotency(t *testing.T) {
	cfg := config.Config{ConfigDir: t.TempDir(), Password: "test-password", ServerHost: "vpn.example.com", ExternalInterface: "eth0"}
	w := &web{cfg: cfg, service: app.New(cfg), idem: map[string]*idempotencyEntry{}}
	request := func(origin, body string) *http.Request {
		r := httptest.NewRequest(http.MethodPatch, "http://127.0.0.1/api/server-name", strings.NewReader(body))
		r.Header.Set("Origin", origin)
		r.Header.Set("Idempotency-Key", "server-name-test")
		return r
	}
	unauthenticated := httptest.NewRecorder()
	w.requireAuth(w.serverNameAPI)(unauthenticated, request("http://127.0.0.1", `{"name":"Name1"}`))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated = %d", unauthenticated.Code)
	}
	foreign := httptest.NewRecorder()
	w.serverNameAPI(foreign, request("https://foreign.example", `{"name":"Name1"}`))
	if foreign.Code != http.StatusForbidden {
		t.Fatalf("foreign origin = %d", foreign.Code)
	}
	first := httptest.NewRecorder()
	w.serverNameAPI(first, request("http://127.0.0.1", `{"name":"Name1"}`))
	if first.Code != http.StatusOK {
		t.Fatalf("rename = %d", first.Code)
	}
	if _, err := w.service.UpdateServerName("Name2"); err != nil {
		t.Fatal(err)
	}
	replay := httptest.NewRecorder()
	w.serverNameAPI(replay, request("http://127.0.0.1", `{"name":"Name1"}`))
	if replay.Code != http.StatusOK || replay.Body.String() != first.Body.String() {
		t.Fatal("replay changed response")
	}
	state, err := w.service.State()
	if err != nil {
		t.Fatal(err)
	}
	if state.ServerName != "Name2" {
		t.Fatal("replay reapplied mutation")
	}
	conflict := httptest.NewRecorder()
	w.serverNameAPI(conflict, request("http://127.0.0.1", `{"name":"Name3"}`))
	if conflict.Code != http.StatusConflict {
		t.Fatalf("reused key = %d", conflict.Code)
	}
}

func TestServerNameAPIStorageFailureIsSafeServerError(t *testing.T) {
	cfg := config.Config{ConfigDir: t.TempDir(), ServerHost: "vpn.example.com", ExternalInterface: "eth0"}
	w := &web{cfg: cfg, service: app.New(cfg), idem: map[string]*idempotencyEntry{}}
	if _, err := w.service.Init(); err != nil {
		t.Fatal(err)
	}
	path := storage.New(cfg.ConfigDir).StatePath()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPatch, "http://127.0.0.1/api/server-name", strings.NewReader(`{"name":"Name1"}`))
	r.Header.Set("Origin", "http://127.0.0.1")
	recorder := httptest.NewRecorder()
	w.serverNameAPI(recorder, r)
	if recorder.Code != http.StatusInternalServerError || strings.Contains(recorder.Body.String(), cfg.ConfigDir) || !strings.Contains(recorder.Body.String(), "server_name_update_failed") {
		t.Fatalf("unsafe or misclassified storage error: status = %d", recorder.Code)
	}
}
