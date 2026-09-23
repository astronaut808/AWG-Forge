package server

import (
	"bytes"
	"context"
	"encoding/base32"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlauth"
	"github.com/astronaut808/awg-forge/internal/observability"
	"github.com/astronaut808/awg-forge/internal/sqldb"
	"github.com/astronaut808/awg-forge/internal/webtls"
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

	login := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/login", strings.NewReader(`{"password":"legacy-password"}`))
	login.Header.Set("Origin", "http://127.0.0.1")
	loginResult := httptest.NewRecorder()
	w.loginAPI(loginResult, login)
	if loginResult.Code != http.StatusUnauthorized {
		t.Fatalf("legacy login status = %d, body = %s", loginResult.Code, loginResult.Body.String())
	}
	var loginError apiErrorResponse
	if err := json.Unmarshal(loginResult.Body.Bytes(), &loginError); err != nil {
		t.Fatal(err)
	}
	if loginError.Code != "invalid_credentials" {
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

func TestControllerLoginHidesAccountExistenceAndReturnsRateLimit(t *testing.T) {
	auth, closeDB, _ := controllerServerTestAuth(t)
	defer closeDB()
	w := &web{controllerAuth: auth}
	login := func(username, sourceHeader string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/controller/login", strings.NewReader(`{"username":"`+username+`","password":"wrong password","code":"000000"}`))
		r.Header.Set("Origin", "http://127.0.0.1")
		r.Header.Set("X-Forwarded-For", sourceHeader)
		r.RemoteAddr = "192.0.2.1:12345"
		rw := httptest.NewRecorder()
		w.loginAPI(rw, r)
		return rw
	}
	unknown := login("missing", "198.51.100.1")
	wrong := login("admin", "198.51.100.2")
	if unknown.Code != http.StatusUnauthorized || wrong.Code != http.StatusUnauthorized || unknown.Body.String() != wrong.Body.String() {
		t.Fatal("controller login exposed account existence")
	}
	if wrong.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("login error can be cached")
	}
	for index := 0; index < 4; index++ {
		if got := login("admin", "198.51.100."+strconv.Itoa(index+3)).Code; got != http.StatusUnauthorized {
			t.Fatalf("failed login %d status = %d", index, got)
		}
	}
	if got := login("admin", "203.0.113.8").Code; got != http.StatusTooManyRequests {
		t.Fatalf("rate-limited status = %d", got)
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

func TestControllerActivationBarrierClosesLegacyAuthentication(t *testing.T) {
	cfg := controllerHTTPTestConfig(t)
	var runtimeLog bytes.Buffer
	svc := app.NewWithRuntimeLog(cfg, observability.NewWithWriter("debug", &runtimeLog))
	if _, err := svc.Init(); err != nil {
		t.Fatal(err)
	}
	secret, err := svc.SessionSecret()
	if err != nil {
		t.Fatal(err)
	}
	w := newWeb(context.Background(), cfg, svc, secret, webtls.Runtime{}, nil)
	defer w.closeControllerDB()
	legacyValue := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	legacyValue += "." + w.sign(legacyValue)
	protected := w.requireAuth(func(rw http.ResponseWriter, _ *http.Request) { rw.WriteHeader(http.StatusNoContent) })
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	blocking := w.requireAuth(func(rw http.ResponseWriter, _ *http.Request) {
		once.Do(func() { close(started) })
		<-release
		rw.WriteHeader(http.StatusNoContent)
	})
	first := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/state", nil)
	first.AddCookie(sessionCookie(first, legacyValue, 0, false))
	firstDone := make(chan struct{})
	go func() { blocking(httptest.NewRecorder(), first); close(firstDone) }()
	<-started
	activation := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/controller/activate", strings.NewReader(`{"username":"admin","password":"correct horse battery staple","totp_secret":"`+controllerServerTOTPSecret()+`","confirmation_code":"`+controllerServerTOTPCode(t, time.Now())+`"}`))
	activation.Header.Set("Origin", "http://127.0.0.1")
	activation.AddCookie(sessionCookie(activation, legacyValue, 0, false))
	activationResult := httptest.NewRecorder()
	activationDone := make(chan struct{})
	go func() { w.controllerActivateAPI(activationResult, activation); close(activationDone) }()
	deadline := time.After(2 * time.Second)
	for !w.activating.Load() {
		select {
		case <-deadline:
			t.Fatal("activation never entered transition")
		case <-time.After(time.Millisecond):
		}
	}
	pendingLogin := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/login", strings.NewReader(`{"password":"legacy-password"}`))
	pendingLogin.Header.Set("Origin", "http://127.0.0.1")
	pendingResult := httptest.NewRecorder()
	pendingDone := make(chan struct{})
	go func() { w.loginAPI(pendingResult, pendingLogin); close(pendingDone) }()
	select {
	case <-activationDone:
		t.Fatal("activation passed an authorized request still in flight")
	case <-pendingDone:
		if pendingResult.Code != http.StatusServiceUnavailable {
			t.Fatalf("legacy login passed activation barrier: %d", pendingResult.Code)
		}
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	<-firstDone
	<-activationDone
	<-pendingDone
	if activationResult.Code != http.StatusOK {
		t.Fatalf("activation status = %d, body = %s", activationResult.Code, activationResult.Body.String())
	}
	if pendingResult.Code != http.StatusUnauthorized && pendingResult.Code != http.StatusServiceUnavailable {
		t.Fatalf("racing legacy login status = %d", pendingResult.Code)
	}
	if activationResult.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("activation response can be cached")
	}
	var payload map[string]any
	if err := json.Unmarshal(activationResult.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if _, exposed := payload["token"]; exposed {
		t.Fatal("session token appeared in JSON")
	}
	if len(activationResult.Result().Cookies()) == 0 {
		t.Fatal("initial session cookie missing")
	}
	auditLog, err := os.ReadFile(cfg.AuditLogPath)
	if err != nil {
		t.Fatal(err)
	}
	logs := runtimeLog.String() + string(auditLog)
	for _, secret := range []string{"correct horse battery staple", controllerServerTOTPSecret(), activationResult.Result().Cookies()[0].Value} {
		if strings.Contains(logs, secret) {
			t.Fatal("controller auth secret reached logs")
		}
	}
	for _, value := range payload["recovery_codes"].([]any) {
		if strings.Contains(logs, value.(string)) {
			t.Fatal("recovery code reached logs")
		}
	}
	legacy := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/state", nil)
	legacy.AddCookie(sessionCookie(legacy, legacyValue, 0, false))
	denied := httptest.NewRecorder()
	protected(denied, legacy)
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("legacy cookie status = %d", denied.Code)
	}
	login := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/login", strings.NewReader(`{"password":"legacy-password"}`))
	login.Header.Set("Origin", "http://127.0.0.1")
	loginResult := httptest.NewRecorder()
	w.loginAPI(loginResult, login)
	if loginResult.Code != http.StatusUnauthorized {
		t.Fatalf("legacy password status = %d", loginResult.Code)
	}
}

func TestCancelledControllerActivationReturnsToStandalone(t *testing.T) {
	cfg := controllerHTTPTestConfig(t)
	svc := app.New(cfg)
	if _, err := svc.Init(); err != nil {
		t.Fatal(err)
	}
	secret, err := svc.SessionSecret()
	if err != nil {
		t.Fatal(err)
	}
	w := newWeb(context.Background(), cfg, svc, secret, webtls.Runtime{}, nil)
	legacyValue := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	legacyValue += "." + w.sign(legacyValue)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	release := make(chan struct{})
	blocking := w.requireAuth(func(rw http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		rw.WriteHeader(http.StatusNoContent)
	})
	first := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/state", nil)
	first.AddCookie(sessionCookie(first, legacyValue, 0, false))
	firstDone := make(chan struct{})
	go func() { blocking(httptest.NewRecorder(), first); close(firstDone) }()
	<-started
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/controller/activate", strings.NewReader(`{"username":"admin","password":"correct horse battery staple"}`)).WithContext(ctx)
	r.Header.Set("Origin", "http://127.0.0.1")
	r.AddCookie(sessionCookie(r, legacyValue, 0, false))
	rw := httptest.NewRecorder()
	activationDone := make(chan struct{})
	go func() { w.controllerActivateAPI(rw, r); close(activationDone) }()
	deadline := time.After(2 * time.Second)
	for !w.activating.Load() {
		select {
		case <-deadline:
			t.Fatal("cancelled activation never entered transition")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	close(release)
	<-firstDone
	<-activationDone
	if rw.Code != http.StatusServiceUnavailable {
		t.Fatalf("cancelled activation status = %d", rw.Code)
	}
	if w.activating.Load() {
		t.Fatal("standalone mode remained blocked after rollback")
	}
	protected := w.requireAuth(func(rw http.ResponseWriter, _ *http.Request) { rw.WriteHeader(http.StatusNoContent) })
	legacy := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/state", nil)
	legacy.AddCookie(sessionCookie(legacy, legacyValue, 0, false))
	allowed := httptest.NewRecorder()
	protected(allowed, legacy)
	if allowed.Code != http.StatusNoContent {
		t.Fatalf("legacy status after rollback = %d", allowed.Code)
	}
}

func controllerHTTPTestConfig(t *testing.T) config.Config {
	t.Helper()
	dir := t.TempDir()
	return config.Config{ConfigDir: dir, TunnelName: "awg0", ServerHost: "vpn.example.com", ListenPort: 51820,
		WebUIHost: "127.0.0.1", WebUIPort: 51821, ExternalInterface: "eth0", IPv4Subnet: "10.8.0.0/24",
		DNS: "1.1.1.1", AllowedIPs: "0.0.0.0/0", MTU: 1420, ProtocolProfile: "awg_legacy_1_0",
		Password: "legacy-password", AuditLogEnabled: true, AuditLogPath: filepath.Join(dir, "audit.log"),
		DatabaseMode: sqldb.ModeSQLite, DatabasePath: filepath.Join(dir, "awg-forge.db"),
		DatabaseBusyTimeout: 5 * time.Second, DatabaseQueryTimeout: 2 * time.Second, DatabaseMaxOpenConns: 1, DatabaseMaxIdleConns: 1}
}
