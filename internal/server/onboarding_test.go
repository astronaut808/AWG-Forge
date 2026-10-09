package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOnboardingRoutesAuthenticateAndDoNotCache(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Second)
	auth, closeDB, base := controllerServerTestAuthAt(t, base)
	defer closeDB()
	w := &web{controllerAuth: auth, sessions: []byte("test-session-secret")}
	handler := http.NewServeMux()
	handler.HandleFunc("/api/controller/enrollments/status", w.security(w.requireAuth(w.enrollmentOnboardingStatusAPI)))
	handler.HandleFunc("/api/controller/installer-info", w.security(w.requireAuth(w.installerInfoAPI)))
	handler.HandleFunc("/api/logout", w.security(w.requireAuth(w.logoutAPI)))
	request := func(method, path, body string, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
		if token != "" {
			r.AddCookie(sessionCookie(r, token, 0, false))
		}
		rw := httptest.NewRecorder()
		handler.ServeHTTP(rw, r)
		if got := rw.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("%s %s cache control = %q", method, path, got)
		}
		return rw
	}
	missing := request(http.MethodPost, "/api/controller/enrollments/status", `{"invitation_id":"10000000-0000-4000-8000-000000000001"}`, "")
	if missing.Code != http.StatusUnauthorized {
		t.Fatalf("missing session status = %d", missing.Code)
	}
	staleAt := base.Add(-10 * time.Minute)
	stale, err := auth.Authenticate(context.Background(), "admin", "correct horse battery staple", controllerServerTOTPCode(t, staleAt), "127.0.0.1", staleAt)
	if err != nil {
		t.Fatal(err)
	}
	recent := request(http.MethodPost, "/api/controller/enrollments/status", `{"invitation_id":"10000000-0000-4000-8000-000000000001"}`, stale.Token)
	if recent.Code != http.StatusForbidden || !strings.Contains(recent.Body.String(), "recent_auth_required") {
		t.Fatalf("stale session = %d %s", recent.Code, recent.Body.String())
	}
	currentAt := base.Add(30 * time.Second)
	current, err := auth.Authenticate(context.Background(), "admin", "correct horse battery staple", controllerServerTOTPCode(t, currentAt), "127.0.0.1", currentAt)
	if err != nil {
		t.Fatal(err)
	}
	info := request(http.MethodGet, "/api/controller/installer-info", "", current.Token)
	if info.Code != http.StatusOK {
		t.Fatalf("installer info status = %d: %s", info.Code, info.Body.String())
	}
	var value map[string]any
	if err := json.Unmarshal(info.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if len(value) != 2 || value["supported"] != false || value["reason"] != "unpublished_build" {
		t.Fatalf("installer info = %#v", value)
	}
	logout := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/logout", nil)
	logout.Header.Set("Origin", "http://127.0.0.1")
	logout.AddCookie(sessionCookie(logout, current.Token, 0, false))
	logoutResult := httptest.NewRecorder()
	handler.ServeHTTP(logoutResult, logout)
	if logoutResult.Code != http.StatusOK {
		t.Fatalf("logout = %d", logoutResult.Code)
	}
	afterLogout := request(http.MethodGet, "/api/controller/installer-info", "", current.Token)
	if afterLogout.Code != http.StatusUnauthorized {
		t.Fatalf("logged-out session status = %d", afterLogout.Code)
	}
}
