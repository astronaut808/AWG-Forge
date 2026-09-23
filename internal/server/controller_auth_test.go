package server

import (
	"context"
	"encoding/base32"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlauth"
	"github.com/astronaut808/awg-forge/internal/sqldb"
	"github.com/pquerna/otp/totp"
)

func TestControllerAuthWiringRejectsLegacyPasswordAndValidatesOpaqueSession(t *testing.T) {
	auth, closeDB, now := controllerServerTestAuth(t)
	defer closeDB()
	w := &web{
		cfg:            config.Config{Password: "legacy-password"},
		controllerAuth: auth,
		sessions:       []byte("legacy-session-secret"),
	}

	login := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/login", nil)
	login.Header.Set("Origin", "http://127.0.0.1")
	loginResult := httptest.NewRecorder()
	w.loginAPI(loginResult, login)
	if loginResult.Code != http.StatusServiceUnavailable {
		t.Fatalf("legacy login status = %d, body = %s", loginResult.Code, loginResult.Body.String())
	}
	var loginError apiErrorResponse
	if err := json.Unmarshal(loginResult.Body.Bytes(), &loginError); err != nil {
		t.Fatal(err)
	}
	if loginError.Code != "controller_login_unavailable" {
		t.Fatalf("login error code = %q", loginError.Code)
	}

	protected := w.requireAuth(func(rw http.ResponseWriter, _ *http.Request) { rw.WriteHeader(http.StatusNoContent) })
	legacy := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/state", nil)
	legacy.AddCookie(sessionCookie(legacy, "9999999999."+w.sign("9999999999"), 0, false))
	legacyResult := httptest.NewRecorder()
	protected(legacyResult, legacy)
	if legacyResult.Code != http.StatusUnauthorized {
		t.Fatalf("legacy signed session status = %d", legacyResult.Code)
	}

	loginAt := now.Add(30 * time.Second)
	authentication, err := auth.Authenticate(context.Background(), "admin", "correct horse battery staple", controllerServerTOTPCode(t, loginAt), "127.0.0.1", loginAt)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/state", nil)
	request.AddCookie(sessionCookie(request, authentication.Token, 0, false))
	result := httptest.NewRecorder()
	protected(result, request)
	if result.Code != http.StatusNoContent {
		t.Fatalf("opaque controller session status = %d", result.Code)
	}

	logout := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/logout", nil)
	logout.Header.Set("Origin", "http://127.0.0.1")
	logout.AddCookie(sessionCookie(logout, authentication.Token, 0, false))
	logoutResult := httptest.NewRecorder()
	w.logoutAPI(logoutResult, logout)
	if logoutResult.Code != http.StatusOK {
		t.Fatalf("logout status = %d, body = %s", logoutResult.Code, logoutResult.Body.String())
	}
	if _, err := auth.ValidateSession(context.Background(), authentication.Token, loginAt.Add(time.Minute)); !errors.Is(err, controlauth.ErrSessionNotFound) {
		t.Fatalf("revoked session error = %v", err)
	}
}

func controllerServerTestAuth(t *testing.T) (*controlauth.Service, func(), time.Time) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Config{
		ConfigDir:            dir,
		DatabaseMode:         sqldb.ModeSQLite,
		DatabasePath:         filepath.Join(dir, "awg-forge.db"),
		DatabaseBusyTimeout:  5 * time.Second,
		DatabaseQueryTimeout: 2 * time.Second,
		DatabaseMaxOpenConns: 1,
		DatabaseMaxIdleConns: 1,
	}
	db, err := sqldb.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(context.Background()); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	keys, err := controlauth.LoadOrCreateKeys(filepath.Join(dir, controlauth.KeyFileName), nil)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	auth, err := controlauth.NewService(db, keys, controlauth.Options{
		PasswordParams: controlauth.PasswordParams{Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32},
	})
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_015, 0).UTC()
	if _, err := auth.EnrollAdmin(context.Background(), "admin", "correct horse battery staple", controllerServerTOTPSecret(), controllerServerTOTPCode(t, now), now); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	return auth, func() { _ = db.Close() }, now
}

func controllerServerTOTPSecret() string {
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("controller-server-test-secret"))
}

func controllerServerTOTPCode(t *testing.T, at time.Time) string {
	t.Helper()
	code, err := totp.GenerateCode(controllerServerTOTPSecret(), at)
	if err != nil {
		t.Fatal(err)
	}
	return code
}
