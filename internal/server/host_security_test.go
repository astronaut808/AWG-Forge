package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/webtls"
)

func TestPasswordlessSecurityRequiresLoopbackHost(t *testing.T) {
	for _, tc := range []struct {
		host     string
		loopback bool
	}{
		{"localhost:51821", true}, {"LOCALHOST:51821", true}, {"127.0.0.1:51821", true},
		{"[::1]:51821", true}, {"[::ffff:127.0.0.1]:51821", true},
		{"panel.example:51821", false}, {"192.0.2.1:51821", false}, {"", false}, {"localhost.example:51821", false},
	} {
		for _, password := range []string{"", "configured-password"} {
			for _, wrapper := range []string{"function", "handler"} {
				t.Run(tc.host+"/"+password+"/"+wrapper, func(t *testing.T) {
					w := &web{cfg: config.Config{Password: password}}
					called := false
					next := http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) { called = true; rw.WriteHeader(http.StatusNoContent) })
					var handler http.Handler = w.security(next)
					if wrapper == "handler" {
						handler = w.securityHandler(next)
					}
					r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
					r.Host = tc.host
					recorder := httptest.NewRecorder()
					handler.ServeHTTP(recorder, r)
					allowed := password != "" || tc.loopback
					want := http.StatusForbidden
					if allowed {
						want = http.StatusNoContent
					}
					if recorder.Code != want || called != allowed {
						t.Fatalf("status=%d called=%t, want status=%d called=%t", recorder.Code, called, want, allowed)
					}
					if recorder.Header().Get("Content-Security-Policy") == "" {
						t.Fatal("missing security headers")
					}
				})
			}
		}
	}
}

func TestPasswordlessHandlerRejectsNonLoopbackHost(t *testing.T) {
	cfg := config.Config{ConfigDir: t.TempDir(), WebUIHost: "127.0.0.1", ExternalInterface: "eth0", ServerHost: "vpn.example.com"}
	w := newWeb(context.Background(), cfg, app.New(cfg), "synthetic-session-secret", webtls.Runtime{})
	handler := newHandler(w)
	for _, path := range []string{"/", "/api/state", "/static/assets/missing.css"} {
		r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+path, nil)
		r.Host = "panel.example:51821"
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, r)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("%s: status=%d", path, recorder.Code)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/state", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, r)
	if recorder.Code != http.StatusOK {
		t.Fatalf("loopback control: status=%d", recorder.Code)
	}
}
