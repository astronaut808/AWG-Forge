package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/webtls"
)

func TestLoginUsernameAuthentication(t *testing.T) {
	newWeb := func(username string) *web {
		return &web{
			sessions: []byte("test-secret"),
			limits:   map[string][]time.Time{},
			cfg: config.Config{
				WebUIUsername: username,
				Password:      "secret",
			},
		}
	}
	tests := []struct {
		name     string
		username string
		body     string
		status   int
	}{
		{name: "default username", body: `{"username":"admin","password":"secret"}`, status: http.StatusOK},
		{name: "legacy default username", body: `{"password":"secret"}`, status: http.StatusOK},
		{name: "wrong username", body: `{"username":"operator","password":"secret"}`, status: http.StatusUnauthorized},
		{name: "empty username", body: `{"username":"","password":"secret"}`, status: http.StatusUnauthorized},
		{name: "wrong password", body: `{"username":"admin","password":"wrong"}`, status: http.StatusUnauthorized},
		{name: "custom username", username: "operator", body: `{"username":"operator","password":"secret"}`, status: http.StatusOK},
		{name: "legacy username rejected for custom configuration", username: "operator", body: `{"password":"secret"}`, status: http.StatusUnauthorized},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			w := newWeb(test.username)
			r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:51821/api/login", strings.NewReader(test.body))
			r.Header.Set("Origin", "http://127.0.0.1:51821")
			rr := httptest.NewRecorder()

			w.loginAPI(rr, r)

			if got := rr.Code; got != test.status {
				t.Fatalf("status = %d, want %d: %s", got, test.status, rr.Body.String())
			}
			if test.status == http.StatusUnauthorized && !strings.Contains(rr.Body.String(), "invalid password") {
				t.Fatalf("unexpected login failure response: %s", rr.Body.String())
			}
		})
	}
}

func TestLoginUsernameFailuresAreRateLimited(t *testing.T) {
	w := &web{
		sessions: []byte("test-secret"),
		limits:   map[string][]time.Time{},
		cfg:      config.Config{WebUIUsername: "operator", Password: "secret"},
	}
	for attempt := 0; attempt < 6; attempt++ {
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:51821/api/login", strings.NewReader(`{"username":"wrong","password":"secret"}`))
		r.Header.Set("Origin", "http://127.0.0.1:51821")
		rr := httptest.NewRecorder()
		w.loginAPI(rr, r)
		want := http.StatusUnauthorized
		if attempt == 5 {
			want = http.StatusTooManyRequests
		}
		if got := rr.Code; got != want {
			t.Fatalf("attempt %d status = %d, want %d", attempt+1, got, want)
		}
	}
}

func TestLoginWithoutPasswordStillAllowsLocalDevelopment(t *testing.T) {
	w := &web{sessions: []byte("test-secret"), cfg: config.Config{WebUIUsername: "operator"}}
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:51821/api/login", strings.NewReader(`{"username":"wrong","password":"wrong"}`))
	r.Header.Set("Origin", "http://127.0.0.1:51821")
	rr := httptest.NewRecorder()

	w.loginAPI(rr, r)

	if got, want := rr.Code, http.StatusOK; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
}

func TestCustomUsernameSessionThroughHTTPHandler(t *testing.T) {
	for _, tc := range []struct {
		name, username string
		status         int
	}{
		{name: "configured username", username: "operator", status: http.StatusOK},
		{name: "default username rejected", username: "admin", status: http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Config{ConfigDir: t.TempDir(), WebUIUsername: "operator", Password: "secret", SessionCookieSecure: "false"}
			handler := newHandler(newWeb(context.Background(), cfg, app.New(cfg), "test-session-secret", webtls.Runtime{}))
			login := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:51821/api/login", strings.NewReader(`{"username":"`+tc.username+`","password":"secret"}`))
			login.Header.Set("Origin", "http://127.0.0.1:51821")
			login.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, login)
			if response.Code != tc.status {
				t.Fatalf("login status = %d, want %d", response.Code, tc.status)
			}
			cookies := response.Result().Cookies()
			if tc.status == http.StatusUnauthorized {
				if len(cookies) != 0 {
					t.Fatal("rejected username issued a session cookie")
				}
				return
			}
			if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
				t.Fatal("successful login did not issue a protected session cookie")
			}
			logout := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:51821/api/logout", nil)
			logout.Header.Set("Origin", "http://127.0.0.1:51821")
			logout.AddCookie(cookies[0])
			loggedOut := httptest.NewRecorder()
			handler.ServeHTTP(loggedOut, logout)
			if loggedOut.Code != http.StatusOK {
				t.Fatalf("authenticated logout status = %d", loggedOut.Code)
			}
		})
	}
}

func TestAuthenticatedStateAcrossWebUITransports(t *testing.T) {
	for _, tc := range []struct {
		name, transport, origin string
		trustedProxy            bool
		secureCookie            bool
	}{
		{"direct HTTP", "http://127.0.0.1:51821", "http://127.0.0.1:51821", false, false},
		{"direct TLS", "https://panel.example.test", "https://panel.example.test", false, true},
		{"trusted TLS proxy", "http://panel.example.test", "https://panel.example.test", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Config{
				ConfigDir:              t.TempDir(),
				WebUIHost:              "127.0.0.1",
				Password:               "synthetic-password",
				WebUITrustProxyHeaders: tc.trustedProxy,
				WebUITrustedProxyCIDRs: []netip.Prefix{netip.MustParsePrefix("192.0.2.1/32")},
			}
			service := app.New(cfg)
			if _, err := service.Init(); err != nil {
				t.Fatal(err)
			}
			handler := newHandler(newWeb(context.Background(), cfg, service, "synthetic-session-secret", webtls.Runtime{}))
			request := func(method, route, body, peer string, cookie *http.Cookie) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, tc.transport+route, strings.NewReader(body))
				r.RemoteAddr = peer
				r.Header.Set("Origin", tc.origin)
				if tc.trustedProxy {
					r.Header.Set("X-Forwarded-Proto", "https")
				}
				if cookie != nil {
					r.AddCookie(cookie)
				}
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, r)
				return response
			}
			const proxy = "192.0.2.1:54321"
			const legacyLogin = `{"password":"synthetic-password"}`
			if got := request(http.MethodGet, "/api/state", "", proxy, nil).Code; got != http.StatusUnauthorized {
				t.Fatalf("anonymous state status = %d", got)
			}
			if tc.trustedProxy {
				if got := request(http.MethodPost, "/api/login", legacyLogin, "192.0.2.2:54321", nil).Code; got != http.StatusForbidden {
					t.Fatalf("spoofed proxy login status = %d", got)
				}
			}
			response := request(http.MethodPost, "/api/login", legacyLogin, proxy, nil)
			cookies := response.Result().Cookies()
			if response.Code != http.StatusOK || len(cookies) != 1 || cookies[0].Secure != tc.secureCookie {
				t.Fatalf("login status = %d, session cookies = %d", response.Code, len(cookies))
			}
			if got := request(http.MethodGet, "/api/state", "", proxy, cookies[0]).Code; got != http.StatusOK {
				t.Fatalf("authenticated state status = %d", got)
			}
		})
	}
}
