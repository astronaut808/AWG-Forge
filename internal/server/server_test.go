package server

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"image/png"
	"io"
	"math/big"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/backup"
	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/firewall"
	"github.com/astronaut808/awg-forge/internal/sqldb"
	"github.com/astronaut808/awg-forge/internal/webtls"
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
	r := httptest.NewRequest(http.MethodPost, "https://admin.example.com/api/login", nil)
	w.setSession(rr, r)
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

func TestSessionCookieAllowsLoopbackHTTP(t *testing.T) {
	w := &web{sessions: []byte("test-secret")}
	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:51821/api/login", nil)
	w.setSession(rr, r)
	cookies := rr.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d, want 1", len(cookies))
	}
	if cookies[0].Secure {
		t.Fatal("loopback HTTP session cookie must not use Secure or Safari may ignore it")
	}
	if !cookies[0].HttpOnly {
		t.Fatal("session cookie must use HttpOnly")
	}
	if cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookie SameSite = %v, want Strict", cookies[0].SameSite)
	}
}

func TestSessionCookieSecureCanBeDisabledForHTTPDomain(t *testing.T) {
	w := &web{sessions: []byte("test-secret"), cfg: config.Config{SessionCookieSecure: "false"}}
	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "http://admin.example.com/api/login", nil)
	w.setSession(rr, r)
	cookies := rr.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d, want 1", len(cookies))
	}
	if cookies[0].Secure {
		t.Fatal("SESSION_COOKIE_SECURE=false must allow HTTP domain cookies")
	}
}

func TestSessionCookieSecureCanBeForcedForLoopback(t *testing.T) {
	w := &web{sessions: []byte("test-secret"), cfg: config.Config{SessionCookieSecure: "true"}}
	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:51821/api/login", nil)
	w.setSession(rr, r)
	cookies := rr.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d, want 1", len(cookies))
	}
	if !cookies[0].Secure {
		t.Fatal("SESSION_COOKIE_SECURE=true must force Secure cookies")
	}
}

func TestTrustedProxyHeadersApplyOnlyToTrustedRemoteAddress(t *testing.T) {
	w := &web{cfg: config.Config{
		WebUITrustProxyHeaders: true,
		WebUITrustedProxyCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("10.0.0.0/8")},
	}}
	r := httptest.NewRequest(http.MethodPost, "http://admin.example.com/api/login", nil)
	r.RemoteAddr = "127.0.0.1:12345"
	r.Host = "admin.example.com"
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-For", "198.51.100.24, 10.1.2.3")
	r.Header.Set("Origin", "https://admin.example.com")
	if !w.validOrigin(r) {
		t.Fatal("trusted HTTPS proxy origin should be accepted")
	}
	if w.effectiveScheme(r) != "https" {
		t.Fatalf("effective scheme = %q, want https", w.effectiveScheme(r))
	}
	if got := w.clientIP(r); got != "198.51.100.24" {
		t.Fatalf("client IP = %q, want first untrusted address", got)
	}
	rr := httptest.NewRecorder()
	w.setSession(rr, r)
	if !rr.Result().Cookies()[0].Secure {
		t.Fatal("trusted HTTPS proxy must set a Secure session cookie")
	}
}

func TestSpoofedForwardedHeadersAreIgnored(t *testing.T) {
	w := &web{cfg: config.Config{
		WebUITrustProxyHeaders: true,
		WebUITrustedProxyCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")},
	}}
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:51821/api/login", nil)
	r.RemoteAddr = "198.51.100.10:12345"
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	if w.effectiveScheme(r) != "http" {
		t.Fatal("untrusted forwarded scheme must be ignored")
	}
	if got := w.clientIP(r); got != "198.51.100.10" {
		t.Fatalf("client IP = %q, want direct remote address", got)
	}
	rr := httptest.NewRecorder()
	w.setSession(rr, r)
	if rr.Result().Cookies()[0].Secure {
		t.Fatal("spoofed forwarded scheme must not change loopback HTTP cookie policy")
	}
}

func TestMissingOriginIsRejectedFromTrustedProxy(t *testing.T) {
	w := &web{cfg: config.Config{
		WebUITrustProxyHeaders: true,
		WebUITrustedProxyCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")},
	}}
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:51821/api/login", nil)
	r.RemoteAddr = "127.0.0.1:12345"
	if w.validOrigin(r) {
		t.Fatal("trusted proxy requests without Origin or Referer must be rejected")
	}
}

func TestHTTPServerUsesBoundedTimeouts(t *testing.T) {
	server := newHTTPServer("127.0.0.1:0", http.NewServeMux())
	if server.ReadHeaderTimeout != webReadHeaderTimeout || server.ReadTimeout != webReadTimeout || server.WriteTimeout != webWriteTimeout || server.IdleTimeout != webIdleTimeout {
		t.Fatalf("unexpected server timeouts: %#v", server)
	}
}

func TestManualTLSServerHandshake(t *testing.T) {
	certPath, keyPath := writeManualTLSCertificate(t, "panel.example.com")
	cfg := config.Config{
		ConfigDir:          t.TempDir(),
		WebUITLSMode:       string(webtls.ModeManual),
		WebUITLSCertFile:   certPath,
		WebUITLSKeyFile:    keyPath,
		WebUITLSServerName: "panel.example.com",
	}
	tlsRuntime, err := webtls.Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	tlsConfig := tlsRuntime.TLSConfig
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := newHTTPServer(listener.Addr().String(), http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusNoContent)
	}))
	server.TLSConfig = tlsConfig
	errCh := make(chan error, 1)
	go func() { errCh <- server.ServeTLS(listener, "", "") }()

	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certPEM) {
		t.Fatal("test certificate was not added to root pool")
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: "panel.example.com"}}}
	response, err := client.Get("https://" + listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusNoContent)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		t.Fatal(err)
	}
}

func TestPublicTLSDoesNotExposeCertificatePaths(t *testing.T) {
	cfg := config.Config{
		WebUITLSMode: string(webtls.ModeManual),
	}
	summary := publicTLS(webtls.Status{Mode: webtls.ModeManual, Subject: "CN=panel.example.com", Issuer: "CN=issuer"}, cfg)
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "/private/") {
		t.Fatalf("TLS summary exposes a certificate path: %s", encoded)
	}
}

func writeManualTLSCertificate(t *testing.T, dnsName string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: dnsName},
		DNSNames:              []string{dnsName},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
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
	if got, want := rr.Header().Get("Cache-Control"), "no-store"; got != want {
		t.Fatalf("Cache-Control = %q, want %q", got, want)
	}
}

func TestTrafficSummaryAPIDisabledDatabase(t *testing.T) {
	w := &web{cfg: config.Config{DatabaseMode: "off"}}
	r := httptest.NewRequest(http.MethodGet, "/api/traffic-summary", nil)
	rr := httptest.NewRecorder()

	w.trafficSummaryAPI(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got, want := rr.Header().Get("Cache-Control"), "no-store"; got != want {
		t.Fatalf("Cache-Control = %q, want %q", got, want)
	}
	var payload struct {
		Enabled bool  `json:"enabled"`
		Rows    []any `json:"rows"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Enabled {
		t.Fatal("traffic summary should report disabled database")
	}
	if payload.Rows == nil || len(payload.Rows) != 0 {
		t.Fatalf("rows = %#v, want empty array", payload.Rows)
	}
}

func TestUpdateClientTrafficLimitAPI(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{
		ConfigDir:            dir,
		TunnelName:           "awg0",
		ServerHost:           "vpn.example.com",
		ListenPort:           51820,
		WebUIHost:            "127.0.0.1",
		WebUIPort:            51821,
		ExternalInterface:    "eth0",
		IPv4Subnet:           "10.8.0.0/24",
		DNS:                  "1.1.1.1",
		AllowedIPs:           "0.0.0.0/0",
		ProtocolProfile:      "awg_legacy_1_0",
		DatabaseMode:         sqldb.ModeSQLite,
		DatabasePath:         filepath.Join(dir, "awg-forge.db"),
		DatabaseBusyTimeout:  5 * time.Second,
		DatabaseQueryTimeout: 2 * time.Second,
		DatabaseMaxOpenConns: 1,
		DatabaseMaxIdleConns: 1,
	}
	if _, err := sqldb.Migrate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	svc := app.New(cfg)
	client, err := svc.AddClient("phone")
	if err != nil {
		t.Fatal(err)
	}
	w := &web{cfg: cfg, service: svc, idem: map[string]*idempotencyEntry{}}

	r := httptest.NewRequest(http.MethodPatch, "http://127.0.0.1/api/clients/"+client.ID+"/traffic-limit", strings.NewReader(`{"limit_bytes":53687091200}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", "set-limit")
	r.Header.Set("Origin", "http://127.0.0.1")
	rr := httptest.NewRecorder()
	w.clientAPI(rr, r)
	if rr.Code != http.StatusOK {
		t.Fatalf("set status = %d body = %s", rr.Code, rr.Body.String())
	}
	limits, err := sqldb.ListClientTrafficLimits(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(limits) != 1 || limits[0].LimitBytes != 53687091200 || limits[0].Period != sqldb.TrafficLimitPeriodLifetime {
		t.Fatalf("limits = %#v, want one 50 GiB limit", limits)
	}

	r = httptest.NewRequest(http.MethodPatch, "http://127.0.0.1/api/clients/"+client.ID+"/traffic-limit", strings.NewReader(`{"limit_bytes":null}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", "clear-limit")
	r.Header.Set("Origin", "http://127.0.0.1")
	rr = httptest.NewRecorder()
	w.clientAPI(rr, r)
	if rr.Code != http.StatusOK {
		t.Fatalf("clear status = %d body = %s", rr.Code, rr.Body.String())
	}
	limits, err = sqldb.ListClientTrafficLimits(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(limits) != 0 {
		t.Fatalf("limits after clear = %#v, want none", limits)
	}
}

func TestCreateClientCanSetTrafficLimit(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{
		ConfigDir:            dir,
		TunnelName:           "awg0",
		ServerHost:           "vpn.example.com",
		ListenPort:           51820,
		WebUIHost:            "127.0.0.1",
		WebUIPort:            51821,
		ExternalInterface:    "eth0",
		IPv4Subnet:           "10.8.0.0/24",
		DNS:                  "1.1.1.1",
		AllowedIPs:           "0.0.0.0/0",
		ProtocolProfile:      "awg_legacy_1_0",
		DatabaseMode:         sqldb.ModeSQLite,
		DatabasePath:         filepath.Join(dir, "awg-forge.db"),
		DatabaseBusyTimeout:  5 * time.Second,
		DatabaseQueryTimeout: 2 * time.Second,
		DatabaseMaxOpenConns: 1,
		DatabaseMaxIdleConns: 1,
	}
	if _, err := sqldb.Migrate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	svc := app.New(cfg)
	state, err := svc.Init()
	if err != nil {
		t.Fatal(err)
	}
	tunnel := state.Tunnels[0]
	w := &web{cfg: cfg, service: svc, idem: map[string]*idempotencyEntry{}}

	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/clients", strings.NewReader(`{"tunnel_id":"`+tunnel.ID+`","name":"phone","expires_at":"","traffic_limit_bytes":104857600,"traffic_limit_period":"rolling_30d"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", "create-with-limit")
	r.Header.Set("Origin", "http://127.0.0.1")
	rr := httptest.NewRecorder()
	w.clientsAPI(rr, r)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}
	limits, err := sqldb.ListClientTrafficLimits(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(limits) != 1 || limits[0].LimitBytes != 104857600 || limits[0].Period != sqldb.TrafficLimitPeriodRolling30Days {
		t.Fatalf("limits = %#v, want one 100 MiB limit", limits)
	}
}

func TestParseTrafficLimitPeriod(t *testing.T) {
	for _, tt := range []struct {
		name  string
		value string
		want  sqldb.TrafficLimitPeriod
		valid bool
	}{
		{name: "default", want: sqldb.TrafficLimitPeriodLifetime, valid: true},
		{name: "lifetime", value: "lifetime", want: sqldb.TrafficLimitPeriodLifetime, valid: true},
		{name: "rolling 30 days", value: "rolling_30d", want: sqldb.TrafficLimitPeriodRolling30Days, valid: true},
		{name: "invalid", value: "weekly"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseTrafficLimitPeriod(tt.value)
			if tt.valid && err != nil {
				t.Fatal(err)
			}
			if !tt.valid && err == nil {
				t.Fatal("expected invalid period error")
			}
			if tt.valid && got != tt.want {
				t.Fatalf("period = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCreateClientRejectsTrafficLimitWithoutDatabase(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{
		ConfigDir:         dir,
		TunnelName:        "awg0",
		ServerHost:        "vpn.example.com",
		ListenPort:        51820,
		WebUIHost:         "127.0.0.1",
		WebUIPort:         51821,
		ExternalInterface: "eth0",
		IPv4Subnet:        "10.8.0.0/24",
		DNS:               "1.1.1.1",
		AllowedIPs:        "0.0.0.0/0",
		ProtocolProfile:   "awg_legacy_1_0",
		DatabaseMode:      sqldb.ModeOff,
	}
	svc := app.New(cfg)
	state, err := svc.Init()
	if err != nil {
		t.Fatal(err)
	}
	tunnel := state.Tunnels[0]
	w := &web{cfg: cfg, service: svc, idem: map[string]*idempotencyEntry{}}

	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/clients", strings.NewReader(`{"tunnel_id":"`+tunnel.ID+`","name":"phone","expires_at":"","traffic_limit_bytes":104857600}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", "create-with-limit-no-db")
	r.Header.Set("Origin", "http://127.0.0.1")
	rr := httptest.NewRecorder()
	w.clientsAPI(rr, r)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}
	state, err = svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Tunnels[0].Clients) != 0 {
		t.Fatal("client should not be created when requested traffic limit cannot be stored")
	}
}

func TestCreateClientRejectsTrafficLimitWithOldDatabaseSchema(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{
		ConfigDir:            dir,
		TunnelName:           "awg0",
		ServerHost:           "vpn.example.com",
		ListenPort:           51820,
		WebUIHost:            "127.0.0.1",
		WebUIPort:            51821,
		ExternalInterface:    "eth0",
		IPv4Subnet:           "10.8.0.0/24",
		DNS:                  "1.1.1.1",
		AllowedIPs:           "0.0.0.0/0",
		ProtocolProfile:      "awg_legacy_1_0",
		DatabaseMode:         sqldb.ModeSQLite,
		DatabasePath:         filepath.Join(dir, "awg-forge.db"),
		DatabaseBusyTimeout:  5 * time.Second,
		DatabaseQueryTimeout: 2 * time.Second,
		DatabaseMaxOpenConns: 1,
		DatabaseMaxIdleConns: 1,
	}
	if _, err := sqldb.Migrate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), "DELETE FROM schema_migrations WHERE version = ?", sqldb.CurrentSchemaVersion); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	svc := app.New(cfg)
	state, err := svc.Init()
	if err != nil {
		t.Fatal(err)
	}
	tunnel := state.Tunnels[0]
	w := &web{cfg: cfg, service: svc, idem: map[string]*idempotencyEntry{}}

	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/clients", strings.NewReader(`{"tunnel_id":"`+tunnel.ID+`","name":"phone","expires_at":"","traffic_limit_bytes":104857600}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", "create-with-limit-old-schema")
	r.Header.Set("Origin", "http://127.0.0.1")
	rr := httptest.NewRecorder()
	w.clientsAPI(rr, r)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}
	state, err = svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Tunnels[0].Clients) != 0 {
		t.Fatal("client should not be created when requested traffic limit cannot be stored")
	}
}

func TestClientTrafficSummaryEnablesLimitSettingsWithoutHistory(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{
		ConfigDir:            dir,
		TunnelName:           "awg0",
		ServerHost:           "vpn.example.com",
		ListenPort:           51820,
		WebUIHost:            "127.0.0.1",
		WebUIPort:            51821,
		ExternalInterface:    "eth0",
		IPv4Subnet:           "10.8.0.0/24",
		DNS:                  "1.1.1.1",
		AllowedIPs:           "0.0.0.0/0",
		ProtocolProfile:      "awg_legacy_1_0",
		DatabaseMode:         sqldb.ModeSQLite,
		DatabasePath:         filepath.Join(dir, "awg-forge.db"),
		DatabaseBusyTimeout:  5 * time.Second,
		DatabaseQueryTimeout: 2 * time.Second,
		DatabaseMaxOpenConns: 1,
		DatabaseMaxIdleConns: 1,
	}
	if _, err := sqldb.Migrate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	svc := app.New(cfg)
	client, err := svc.AddClient("phone")
	if err != nil {
		t.Fatal(err)
	}
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}

	w := &web{cfg: cfg, service: svc}
	traffic := w.clientTrafficSummary(context.Background(), state)
	summary := traffic[client.TunnelID][client.ID]
	if !summary.Enabled {
		t.Fatal("client traffic summary should be enabled when sqlite is available, even before traffic history exists")
	}
	if summary.RxTotal != 0 || summary.TxTotal != 0 || summary.LimitBytes != nil || summary.Exceeded {
		t.Fatalf("summary = %#v, want zero totals with no limit", summary)
	}
}

func TestEnableClientRechecksExceededTrafficLimit(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{
		ConfigDir:            dir,
		TunnelName:           "awg0",
		ServerHost:           "vpn.example.com",
		ListenPort:           51820,
		WebUIHost:            "127.0.0.1",
		WebUIPort:            51821,
		ExternalInterface:    "eth0",
		IPv4Subnet:           "10.8.0.0/24",
		DNS:                  "1.1.1.1",
		AllowedIPs:           "0.0.0.0/0",
		ProtocolProfile:      "awg_legacy_1_0",
		DatabaseMode:         sqldb.ModeSQLite,
		DatabasePath:         filepath.Join(dir, "awg-forge.db"),
		DatabaseBusyTimeout:  5 * time.Second,
		DatabaseQueryTimeout: 2 * time.Second,
		DatabaseMaxOpenConns: 1,
		DatabaseMaxIdleConns: 1,
	}
	if _, err := sqldb.Migrate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	svc := app.New(cfg)
	client, err := svc.AddClient("phone")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	if err := sqldb.RecordTrafficSamples(context.Background(), cfg, []sqldb.TrafficSample{
		{SampledAt: now.Add(-time.Minute), TunnelID: client.TunnelID, ClientID: client.ID, RxBytes: 0, TxBytes: 0, Present: true},
		{SampledAt: now, TunnelID: client.TunnelID, ClientID: client.ID, RxBytes: 6000, TxBytes: 0, Present: true},
	}); err != nil {
		t.Fatal(err)
	}
	limit := uint64(5000)
	if err := sqldb.SetClientTrafficLimit(context.Background(), cfg, client.TunnelID, client.ID, &limit); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetClientEnabled(client.ID, false); err != nil {
		t.Fatal(err)
	}
	w := &web{cfg: cfg, service: svc, idem: map[string]*idempotencyEntry{}}

	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/clients/"+client.ID+"/enable", nil)
	r.Header.Set("Idempotency-Key", "enable-over-limit")
	r.Header.Set("Origin", "http://127.0.0.1")
	rr := httptest.NewRecorder()
	w.clientAPI(rr, r)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d body = %s, want %d", rr.Code, rr.Body.String(), http.StatusConflict)
	}
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload.Error, "traffic limit exceeded") {
		t.Fatalf("error = %q, want traffic limit reason", payload.Error)
	}
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if state.Tunnels[0].Clients[0].Enabled {
		t.Fatal("client should remain disabled when re-enabled over traffic limit")
	}
}

func TestTrafficLimitAutoReleasesQuotaBlockedClient(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{
		ConfigDir:            dir,
		TunnelName:           "awg0",
		ServerHost:           "vpn.example.com",
		ListenPort:           51820,
		WebUIHost:            "127.0.0.1",
		WebUIPort:            51821,
		ExternalInterface:    "eth0",
		IPv4Subnet:           "10.8.0.0/24",
		DNS:                  "1.1.1.1",
		AllowedIPs:           "0.0.0.0/0",
		ProtocolProfile:      "awg_legacy_1_0",
		DatabaseMode:         sqldb.ModeSQLite,
		DatabasePath:         filepath.Join(dir, "awg-forge.db"),
		DatabaseBusyTimeout:  5 * time.Second,
		DatabaseQueryTimeout: 2 * time.Second,
		DatabaseMaxOpenConns: 1,
		DatabaseMaxIdleConns: 1,
	}
	if _, err := sqldb.Migrate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	svc := app.New(cfg)
	client, err := svc.AddClient("phone")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := sqldb.RecordTrafficSamples(context.Background(), cfg, []sqldb.TrafficSample{
		{SampledAt: now.Add(-time.Minute), TunnelID: client.TunnelID, ClientID: client.ID, Present: true},
		{SampledAt: now, TunnelID: client.TunnelID, ClientID: client.ID, RxBytes: 6000, Present: true},
	}); err != nil {
		t.Fatal(err)
	}
	limit := uint64(5000)
	if err := sqldb.SetClientTrafficLimitWithPeriod(context.Background(), cfg, client.TunnelID, client.ID, &limit, sqldb.TrafficLimitPeriodRolling30Days); err != nil {
		t.Fatal(err)
	}
	enforceTrafficLimits(context.Background(), cfg, svc)
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if state.Tunnels[0].Clients[0].Enabled {
		t.Fatal("quota-exceeded client should be disabled")
	}
	blocks, err := sqldb.ListTrafficLimitBlocks(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || blocks[0].ClientID != client.ID {
		t.Fatalf("blocks = %#v, want client block", blocks)
	}

	limit = 7000
	if err := sqldb.SetClientTrafficLimitWithPeriod(context.Background(), cfg, client.TunnelID, client.ID, &limit, sqldb.TrafficLimitPeriodRolling30Days); err != nil {
		t.Fatal(err)
	}
	enforceTrafficLimits(context.Background(), cfg, svc)
	state, err = svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if !state.Tunnels[0].Clients[0].Enabled {
		t.Fatal("quota-blocked client should be re-enabled after usage falls below the limit")
	}
	blocks, err = sqldb.ListTrafficLimitBlocks(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 0 {
		t.Fatalf("blocks after release = %#v, want none", blocks)
	}

	limit = 5000
	if err := sqldb.SetClientTrafficLimitWithPeriod(context.Background(), cfg, client.TunnelID, client.ID, &limit, sqldb.TrafficLimitPeriodRolling30Days); err != nil {
		t.Fatal(err)
	}
	enforceTrafficLimits(context.Background(), cfg, svc)
	w := &web{cfg: cfg, service: svc, idem: map[string]*idempotencyEntry{}}
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/clients/"+client.ID+"/disable", nil)
	r.Header.Set("Idempotency-Key", "manual-disable-after-quota")
	r.Header.Set("Origin", "http://127.0.0.1")
	rr := httptest.NewRecorder()
	w.clientAPI(rr, r)
	if rr.Code != http.StatusOK {
		t.Fatalf("manual disable status = %d body = %s", rr.Code, rr.Body.String())
	}
	limit = 7000
	if err := sqldb.SetClientTrafficLimitWithPeriod(context.Background(), cfg, client.TunnelID, client.ID, &limit, sqldb.TrafficLimitPeriodRolling30Days); err != nil {
		t.Fatal(err)
	}
	enforceTrafficLimits(context.Background(), cfg, svc)
	state, err = svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if state.Tunnels[0].Clients[0].Enabled {
		t.Fatal("manually disabled client must not be auto-enabled after quota release")
	}
}

func TestManualDisableKeepsClientEnabledWhenQuotaBlockCannotBeCleared(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{
		ConfigDir:            dir,
		TunnelName:           "awg0",
		ServerHost:           "vpn.example.com",
		ListenPort:           51820,
		WebUIHost:            "127.0.0.1",
		WebUIPort:            51821,
		ExternalInterface:    "eth0",
		IPv4Subnet:           "10.8.0.0/24",
		DNS:                  "1.1.1.1",
		AllowedIPs:           "0.0.0.0/0",
		ProtocolProfile:      "awg_legacy_1_0",
		DatabaseMode:         sqldb.ModeSQLite,
		DatabasePath:         filepath.Join(dir, "awg-forge.db"),
		DatabaseBusyTimeout:  5 * time.Second,
		DatabaseQueryTimeout: 2 * time.Second,
		DatabaseMaxOpenConns: 1,
		DatabaseMaxIdleConns: 1,
	}
	if _, err := sqldb.Migrate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	svc := app.New(cfg)
	client, err := svc.AddClient("phone")
	if err != nil {
		t.Fatal(err)
	}
	if err := sqldb.MarkClientTrafficLimitBlocked(context.Background(), cfg, client.TunnelID, client.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	brokenCfg := cfg
	brokenCfg.DatabasePath = filepath.Join(t.TempDir(), "not-a-database")
	if err := os.Mkdir(brokenCfg.DatabasePath, 0700); err != nil {
		t.Fatal(err)
	}
	w := &web{cfg: brokenCfg, service: svc, idem: map[string]*idempotencyEntry{}}
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/clients/"+client.ID+"/disable", nil)
	r.Header.Set("Idempotency-Key", "disable-with-unavailable-db")
	r.Header.Set("Origin", "http://127.0.0.1")
	rr := httptest.NewRecorder()
	w.clientAPI(rr, r)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body = %s, want %d", rr.Code, rr.Body.String(), http.StatusServiceUnavailable)
	}
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if !state.Tunnels[0].Clients[0].Enabled {
		t.Fatal("client must remain enabled when its quota block cannot be cleared")
	}
}

func TestPublicDatabaseSummary(t *testing.T) {
	off := publicDatabase(config.Config{})
	if off["mode"] != "off" || off["enabled"] != false {
		t.Fatalf("zero config database summary = %#v, want off disabled", off)
	}
	sqlite := publicDatabase(config.Config{DatabaseMode: "sqlite"})
	if sqlite["mode"] != "sqlite" || sqlite["enabled"] != true {
		t.Fatalf("sqlite database summary = %#v, want sqlite enabled", sqlite)
	}
}

func TestClientQRAPIReturnsPNG(t *testing.T) {
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
	client, err := svc.AddClient("Phone QR")
	if err != nil {
		t.Fatal(err)
	}
	w := &web{service: svc}
	r := httptest.NewRequest(http.MethodGet, "/api/clients/"+client.ID+"/qr", nil)
	rr := httptest.NewRecorder()

	w.clientQRAPI(rr, r, client.ID)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}
	if got, want := rr.Header().Get("Content-Type"), "image/png"; got != want {
		t.Fatalf("Content-Type = %q, want %q", got, want)
	}
	if got, want := rr.Header().Get("Content-Disposition"), `inline; filename="Phone-QR.png"`; got != want {
		t.Fatalf("Content-Disposition = %q, want %q", got, want)
	}
	if got, want := rr.Header().Get("Cache-Control"), "no-store"; got != want {
		t.Fatalf("Cache-Control = %q, want %q", got, want)
	}
	requireReadableQRCodePNG(t, rr.Body.Bytes())
}

func TestClientAmneziaVPNQRAPIReturnsPNG(t *testing.T) {
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
		ProtocolProfile:     "awg_2_0",
	}
	svc := app.New(cfg)
	client, err := svc.AddClient("Phone QR")
	if err != nil {
		t.Fatal(err)
	}
	w := &web{service: svc}
	r := httptest.NewRequest(http.MethodGet, "/api/clients/"+client.ID+"/amnezia-vpn-qr", nil)
	rr := httptest.NewRecorder()

	w.clientAmneziaVPNQRAPI(rr, r, client.ID)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}
	if got, want := rr.Header().Get("Content-Type"), "image/png"; got != want {
		t.Fatalf("Content-Type = %q, want %q", got, want)
	}
	if got, want := rr.Header().Get("Content-Disposition"), `inline; filename="Phone-QR-amneziavpn.png"`; got != want {
		t.Fatalf("Content-Disposition = %q, want %q", got, want)
	}
	if got := rr.Header().Get("X-QR-Chunk"); got != "1" {
		t.Fatalf("X-QR-Chunk = %q, want 1", got)
	}
	if got := rr.Header().Get("X-QR-Chunks"); got != "1" {
		t.Fatalf("X-QR-Chunks = %q, want 1", got)
	}
	if got, want := rr.Header().Get("Cache-Control"), "no-store"; got != want {
		t.Fatalf("Cache-Control = %q, want %q", got, want)
	}
	requireReadableQRCodePNG(t, rr.Body.Bytes())
}

func TestAmneziaVPNQRSeriesReturnsSingleChunk(t *testing.T) {
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
		ProtocolProfile:     "awg_2_0",
	}
	svc := app.New(cfg)
	client, err := svc.AddClient("Phone QR")
	if err != nil {
		t.Fatal(err)
	}
	w := &web{service: svc}
	r := httptest.NewRequest(http.MethodGet, "/api/clients/"+client.ID+"/amnezia-vpn-qr-series", nil)
	rr := httptest.NewRecorder()

	w.clientAmneziaVPNQRSeriesAPI(rr, r, client.ID)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}
	var payload struct {
		Chunks int `json:"chunks"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Chunks != 1 {
		t.Fatalf("chunks = %d, want 1", payload.Chunks)
	}
}

func TestBuildAmneziaVPNQRPackHeaderAndDecompression(t *testing.T) {
	original := []byte(`{"description":"phone","hostName":"vpn.example.com"}`)
	payload, err := buildAmneziaVPNQRPack(original)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		t.Fatal(err)
	}
	if got := binary.BigEndian.Uint32(decoded[0:4]); got != amneziaVPNQRPackMagic {
		t.Fatalf("magic = %#x, want %#x", got, amneziaVPNQRPackMagic)
	}
	if got, want := binary.BigEndian.Uint32(decoded[4:8]), uint32(len(decoded[amneziaVPNQRPackHeaderLen:])+4); got != want {
		t.Fatalf("compressed length field = %d, want %d", got, want)
	}
	if got, want := binary.BigEndian.Uint32(decoded[8:amneziaVPNQRPackHeaderLen]), uint32(len(original)); got != want {
		t.Fatalf("uncompressed length field = %d, want %d", got, want)
	}
	decompressed := decompressZlibPayload(t, decoded[amneziaVPNQRPackHeaderLen:])
	if !bytes.Equal(decompressed, original) {
		t.Fatalf("decompressed JSON = %s, want %s", decompressed, original)
	}
}

func TestBuildAmneziaVPNQRPackRejectsOversizedInput(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		wantErr string
	}{
		{
			name:    "empty",
			payload: nil,
			wantErr: "empty",
		},
		{
			name:    "too large",
			payload: bytes.Repeat([]byte("x"), amneziaVPNQRMaxInputBytes+1),
			wantErr: "too large",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := buildAmneziaVPNQRPack(tt.payload)
			if err == nil {
				t.Fatal("expected AmneziaVPN QR packer to reject payload")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestBuildAmneziaVPNQRPayloadRoundTripsToExpectedJSON(t *testing.T) {
	ctx := amneziaVPNTestExportContext(t)
	payload, err := buildAmneziaVPNQRPayload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	actualJSON := decodeAmneziaVPNQRPayload(t, payload).JSON
	expectedJSON := expectedAmneziaVPNQRJSON(t, ctx)
	if !reflect.DeepEqual(actualJSON, expectedJSON) {
		t.Fatalf("decoded AmneziaVPN QR JSON mismatch\nactual: %#v\nexpected: %#v", actualJSON, expectedJSON)
	}
}

func TestAmneziaVPNJSONCompatibility(t *testing.T) {
	ctx := amneziaVPNTestExportContext(t)
	payload, err := buildAmneziaVPNQRPayload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	decoded := decodeAmneziaVPNQRPayload(t, payload)
	var outer decodedAmneziaVPNConfig
	if err := json.Unmarshal(decoded.JSONBytes, &outer); err != nil {
		t.Fatal(err)
	}
	if outer.DefaultContainer != "amnezia-awg" {
		t.Fatalf("defaultContainer = %q, want amnezia-awg", outer.DefaultContainer)
	}
	if len(outer.Containers) != 1 {
		t.Fatalf("containers length = %d, want 1", len(outer.Containers))
	}
	container := outer.Containers[0]
	if container.Container != "amnezia-awg" {
		t.Fatalf("container = %q, want amnezia-awg", container.Container)
	}
	if container.AWG.Port != "51820" {
		t.Fatalf("outer awg.port = %q, want string 51820", container.AWG.Port)
	}
	if container.AWG.ProtocolVersion != "2" {
		t.Fatalf("protocol_version = %q, want 2", container.AWG.ProtocolVersion)
	}
	if container.AWG.TransportProto != "udp" {
		t.Fatalf("transport_proto = %q, want udp", container.AWG.TransportProto)
	}
	if container.AWG.LastConfig == "" {
		t.Fatal("last_config must be a JSON string")
	}
	var last decodedAmneziaVPNLastConfig
	if err := json.Unmarshal([]byte(container.AWG.LastConfig), &last); err != nil {
		t.Fatalf("last_config must be a JSON string with compatible field types: %v", err)
	}
	if last.Port != 51820 {
		t.Fatalf("last_config.port = %d, want JSON number 51820", last.Port)
	}
	if !reflect.DeepEqual(last.AllowedIPs, []string{"0.0.0.0/0"}) {
		t.Fatalf("last_config.allowed_ips = %#v, want JSON string array", last.AllowedIPs)
	}
}

func TestAmneziaVPNQRPayloadSemanticRoundTrip(t *testing.T) {
	ctx := amneziaVPNTestExportContext(t)
	payload, err := buildAmneziaVPNQRPayload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	decoded := decodeAmneziaVPNQRPayload(t, payload)
	reencoded, err := json.Marshal(decoded.JSON)
	if err != nil {
		t.Fatal(err)
	}
	var reparsed map[string]any
	if err := json.Unmarshal(reencoded, &reparsed); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded.JSON, reparsed) {
		t.Fatalf("re-encoded AmneziaVPN QR JSON changed semantics\nactual: %#v\nreparsed: %#v", decoded.JSON, reparsed)
	}
}

func TestBuildAmneziaVPNClientConfigShape(t *testing.T) {
	ctx := amneziaVPNTestExportContext(t)
	jsonBytes, err := buildAmneziaVPNClientConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var outer map[string]any
	if err := json.Unmarshal(jsonBytes, &outer); err != nil {
		t.Fatal(err)
	}
	if outer["defaultContainer"] != "amnezia-awg" {
		t.Fatalf("defaultContainer = %v", outer["defaultContainer"])
	}
	if outer["description"] != "Phone QR" {
		t.Fatalf("description = %v", outer["description"])
	}
	if outer["hostName"] != "vpn.example.com" {
		t.Fatalf("hostName = %v", outer["hostName"])
	}
	if outer["dns1"] != "1.1.1.1" || outer["dns2"] != "9.9.9.9" {
		t.Fatalf("dns fields = %v/%v", outer["dns1"], outer["dns2"])
	}
	containers := outer["containers"].([]any)
	awg := containers[0].(map[string]any)["awg"].(map[string]any)
	if awg["isThirdPartyConfig"] != true {
		t.Fatalf("isThirdPartyConfig = %v", awg["isThirdPartyConfig"])
	}
	if awg["port"] != "51820" || awg["transport_proto"] != "udp" || awg["protocol_version"] != "2" {
		t.Fatalf("unexpected awg metadata: %#v", awg)
	}
	lastConfig, ok := awg["last_config"].(string)
	if !ok {
		t.Fatalf("last_config type = %T, want string", awg["last_config"])
	}
	var last map[string]any
	if err := json.Unmarshal([]byte(lastConfig), &last); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"client_ip", "client_priv_key", "config", "hostName", "mtu", "persistent_keep_alive", "psk_key", "server_pub_key"} {
		if last[key] == "" {
			t.Fatalf("last_config missing %s: %#v", key, last)
		}
	}
	if got, ok := last["port"].(float64); !ok || got != 51820 {
		t.Fatalf("last_config port = %#v (%T), want number 51820", last["port"], last["port"])
	}
	allowedIPs, ok := last["allowed_ips"].([]any)
	if !ok || len(allowedIPs) != 1 || allowedIPs[0] != "0.0.0.0/0" {
		t.Fatalf("last_config allowed_ips = %#v (%T), want string array", last["allowed_ips"], last["allowed_ips"])
	}
	for _, key := range []string{"S3", "S4", "H1", "H2", "H3", "H4", "I1", "I2", "I3", "I4", "I5"} {
		if last[key] == "" {
			t.Fatalf("last_config missing AWG 2.0 param %s: %#v", key, last)
		}
	}
	if !strings.Contains(last["config"].(string), "[Interface]") || !strings.Contains(last["config"].(string), "[Peer]") {
		t.Fatalf("last_config config does not contain rendered .conf: %v", last["config"])
	}
}

func TestBuildAmneziaVPNClientConfigOmitsEmptyOptionalParams(t *testing.T) {
	ctx := amneziaVPNTestExportContext(t)
	for _, key := range []string{"I2", "I3", "I4", "I5"} {
		delete(ctx.Tunnel.ProtocolParams, key)
	}
	jsonBytes, err := buildAmneziaVPNClientConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var outer amneziaVPNConfig
	if err := json.Unmarshal(jsonBytes, &outer); err != nil {
		t.Fatal(err)
	}
	var last map[string]any
	if err := json.Unmarshal([]byte(outer.Containers[0].AWG.LastConfig), &last); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"I2", "I3", "I4", "I5"} {
		if _, ok := last[key]; ok {
			t.Fatalf("optional empty param %s must be omitted: %#v", key, last)
		}
	}
}

type decodedAmneziaVPNPayload struct {
	JSONBytes []byte
	JSON      map[string]any
}

type decodedAmneziaVPNConfig struct {
	Containers       []decodedAmneziaVPNContainer `json:"containers"`
	DefaultContainer string                       `json:"defaultContainer"`
	Description      string                       `json:"description"`
	DNS1             string                       `json:"dns1,omitempty"`
	DNS2             string                       `json:"dns2,omitempty"`
	HostName         string                       `json:"hostName"`
}

type decodedAmneziaVPNContainer struct {
	AWG       decodedAmneziaVPNAWG `json:"awg"`
	Container string               `json:"container"`
}

type decodedAmneziaVPNAWG struct {
	IsThirdPartyConfig bool   `json:"isThirdPartyConfig"`
	LastConfig         string `json:"last_config"`
	Port               string `json:"port"`
	ProtocolVersion    string `json:"protocol_version"`
	TransportProto     string `json:"transport_proto"`
}

type decodedAmneziaVPNLastConfig struct {
	AllowedIPs          []string `json:"allowed_ips"`
	ClientIP            string   `json:"client_ip"`
	ClientPrivateKey    string   `json:"client_priv_key"`
	Config              string   `json:"config"`
	HostName            string   `json:"hostName"`
	MTU                 string   `json:"mtu"`
	PersistentKeepalive string   `json:"persistent_keep_alive"`
	Port                int      `json:"port"`
	PresharedKey        string   `json:"psk_key"`
	ServerPublicKey     string   `json:"server_pub_key"`

	Jc   string `json:"Jc"`
	Jmin string `json:"Jmin"`
	Jmax string `json:"Jmax"`
	S1   string `json:"S1"`
	S2   string `json:"S2"`
	S3   string `json:"S3"`
	S4   string `json:"S4"`
	H1   string `json:"H1"`
	H2   string `json:"H2"`
	H3   string `json:"H3"`
	H4   string `json:"H4"`
	I1   string `json:"I1,omitempty"`
	I2   string `json:"I2,omitempty"`
	I3   string `json:"I3,omitempty"`
	I4   string `json:"I4,omitempty"`
	I5   string `json:"I5,omitempty"`
}

func amneziaVPNTestExportContext(t *testing.T) app.ClientExportContext {
	t.Helper()
	cfg := config.Config{
		ConfigDir:           t.TempDir(),
		TunnelName:          "awg0",
		ServerHost:          "vpn.example.com",
		ListenPort:          51820,
		WebUIHost:           "127.0.0.1",
		WebUIPort:           51821,
		ExternalInterface:   "eth0",
		IPv4Subnet:          "10.8.0.0/24",
		DNS:                 "1.1.1.1, 2001:4860:4860::8888, 9.9.9.9",
		AllowedIPs:          "0.0.0.0/0",
		PersistentKeepalive: 25,
		MTU:                 1280,
		ProtocolProfile:     "awg_2_0",
	}
	svc := app.New(cfg)
	client, err := svc.AddClient("Phone QR")
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := svc.ClientExportContext(client.ID)
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

func decompressZlibPayload(t *testing.T, payload []byte) []byte {
	t.Helper()
	zr, err := zlib.NewReader(bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := zr.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func expectedAmneziaVPNQRJSON(t *testing.T, ctx app.ClientExportContext) map[string]any {
	t.Helper()
	lastConfigJSON, err := json.Marshal(amneziaVPNLastConfig{
		AllowedIPs:          []string{"0.0.0.0/0"},
		ClientIP:            "10.8.0.2/32",
		ClientPrivateKey:    ctx.Client.PrivateKey,
		Config:              ctx.RenderedConf,
		HostName:            "vpn.example.com",
		MTU:                 "1280",
		PersistentKeepalive: "25",
		Port:                51820,
		PresharedKey:        ctx.Client.PresharedKey,
		ServerPublicKey:     ctx.Tunnel.ServerPublicKey,
		Jc:                  ctx.Tunnel.ProtocolParams["Jc"],
		Jmin:                ctx.Tunnel.ProtocolParams["Jmin"],
		Jmax:                ctx.Tunnel.ProtocolParams["Jmax"],
		S1:                  ctx.Tunnel.ProtocolParams["S1"],
		S2:                  ctx.Tunnel.ProtocolParams["S2"],
		S3:                  ctx.Tunnel.ProtocolParams["S3"],
		S4:                  ctx.Tunnel.ProtocolParams["S4"],
		H1:                  ctx.Tunnel.ProtocolParams["H1"],
		H2:                  ctx.Tunnel.ProtocolParams["H2"],
		H3:                  ctx.Tunnel.ProtocolParams["H3"],
		H4:                  ctx.Tunnel.ProtocolParams["H4"],
		I1:                  ctx.Tunnel.ProtocolParams["I1"],
		I2:                  ctx.Tunnel.ProtocolParams["I2"],
		I3:                  ctx.Tunnel.ProtocolParams["I3"],
		I4:                  ctx.Tunnel.ProtocolParams["I4"],
		I5:                  ctx.Tunnel.ProtocolParams["I5"],
	})
	if err != nil {
		t.Fatal(err)
	}
	expectedBytes, err := json.Marshal(amneziaVPNConfig{
		Containers: []amneziaVPNContainer{{
			AWG: amneziaVPNAWG{
				IsThirdPartyConfig: true,
				LastConfig:         string(lastConfigJSON),
				Port:               "51820",
				ProtocolVersion:    "2",
				TransportProto:     "udp",
			},
			Container: "amnezia-awg",
		}},
		DefaultContainer: "amnezia-awg",
		Description:      "Phone QR",
		DNS1:             "1.1.1.1",
		DNS2:             "9.9.9.9",
		HostName:         "vpn.example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	var expected map[string]any
	if err := json.Unmarshal(expectedBytes, &expected); err != nil {
		t.Fatal(err)
	}
	return expected
}

func decodeAmneziaVPNQRPayload(t *testing.T, payload string) decodedAmneziaVPNPayload {
	t.Helper()
	decoded, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) < amneziaVPNQRPackHeaderLen {
		t.Fatalf("decoded AmneziaVPN QR payload too short: %d", len(decoded))
	}
	if got := binary.BigEndian.Uint32(decoded[0:4]); got != amneziaVPNQRPackMagic {
		t.Fatalf("magic = %#x, want %#x", got, amneziaVPNQRPackMagic)
	}
	jsonBytes := decompressZlibPayload(t, decoded[amneziaVPNQRPackHeaderLen:])
	if got, want := binary.BigEndian.Uint32(decoded[4:8]), uint32(len(decoded[amneziaVPNQRPackHeaderLen:])+4); got != want {
		t.Fatalf("compressed length field = %d, want %d", got, want)
	}
	if got, want := binary.BigEndian.Uint32(decoded[8:amneziaVPNQRPackHeaderLen]), uint32(len(jsonBytes)); got != want {
		t.Fatalf("uncompressed length field = %d, want %d", got, want)
	}
	var out map[string]any
	if err := json.Unmarshal(jsonBytes, &out); err != nil {
		t.Fatal(err)
	}
	return decodedAmneziaVPNPayload{JSONBytes: jsonBytes, JSON: out}
}

type lockedRecorder struct {
	*httptest.ResponseRecorder
	mu sync.Mutex
}

func newLockedRecorder() *lockedRecorder {
	return &lockedRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func (r *lockedRecorder) Write(data []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ResponseRecorder.Write(data)
}

func (r *lockedRecorder) WriteHeader(code int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ResponseRecorder.WriteHeader(code)
}

func (r *lockedRecorder) Flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ResponseRecorder.Flush()
}

func (r *lockedRecorder) BodyString() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Body.String()
}

func requireReadableQRCodePNG(t *testing.T, body []byte) {
	t.Helper()
	if !bytes.HasPrefix(body, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("response is not a PNG")
	}
	img, err := png.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("PNG decode failed: %v", err)
	}
	bounds := img.Bounds()
	if bounds.Dx() < 128 || bounds.Dy() < 128 {
		t.Fatalf("QR image too small: %dx%d", bounds.Dx(), bounds.Dy())
	}
	if isDark(img.At(bounds.Min.X, bounds.Min.Y)) ||
		isDark(img.At(bounds.Max.X-1, bounds.Min.Y)) ||
		isDark(img.At(bounds.Min.X, bounds.Max.Y-1)) ||
		isDark(img.At(bounds.Max.X-1, bounds.Max.Y-1)) {
		t.Fatalf("QR image corners must be quiet-zone white")
	}

	blackPixels := 0
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if isDark(img.At(x, y)) {
				blackPixels++
			}
		}
	}
	if blackPixels == 0 {
		t.Fatal("QR image does not contain black modules")
	}
}

func TestEventsAPIStreamsPublicState(t *testing.T) {
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
		ApplyConfig:         false,
	}
	w := &web{cfg: cfg, service: app.New(cfg)}
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:51821/api/events", nil)
	rr := newLockedRecorder()
	done := make(chan struct{})

	go func() {
		defer close(done)
		w.eventsAPI(rr, r)
	}()

	deadline := time.After(2 * time.Second)
	for !strings.Contains(rr.BodyString(), "event: state") {
		select {
		case <-deadline:
			cancel()
			t.Fatalf("event stream did not include state event; body = %q", rr.BodyString())
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	cancel()
	<-done

	if got, want := rr.Header().Get("Content-Type"), "text/event-stream; charset=utf-8"; got != want {
		t.Fatalf("Content-Type = %q, want %q", got, want)
	}
	if !strings.Contains(rr.BodyString(), `"authenticated":true`) {
		t.Fatalf("event stream body does not include public state: %q", rr.BodyString())
	}
}

func TestClientImportKeyAPIReturnsVPNKey(t *testing.T) {
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
		ProtocolProfile:     "awg_2_0",
	}
	svc := app.New(cfg)
	client, err := svc.AddClient("iPhone")
	if err != nil {
		t.Fatal(err)
	}
	w := &web{service: svc}
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:51821/api/clients/"+client.ID+"/import-key", nil)
	rr := httptest.NewRecorder()

	w.clientImportKeyAPI(rr, r, client.ID)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}
	var payload struct {
		ImportKey string `json:"import_key"`
		Format    string `json:"format"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Format != "vpn-conf-base64url" {
		t.Fatalf("format = %q", payload.Format)
	}
	if got, want := rr.Header().Get("Cache-Control"), "no-store"; got != want {
		t.Fatalf("Cache-Control = %q, want %q", got, want)
	}
	if !strings.HasPrefix(payload.ImportKey, "vpn://") {
		t.Fatalf("import key prefix mismatch: %q", payload.ImportKey)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(payload.ImportKey, "vpn://"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(decoded), "S3 =") || !strings.Contains(string(decoded), "S4 =") {
		t.Fatalf("decoded AWG 2.0 key does not contain S3/S4:\n%s", decoded)
	}
}

func TestRestoreVerifyAPIValidatesBackupWithoutWritingState(t *testing.T) {
	const password = "correct horse battery staple"
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
		ProtocolProfile:     "awg_2_0",
	}
	svc := app.New(cfg)
	if _, err := svc.AddClient("phone"); err != nil {
		t.Fatal(err)
	}
	archive, err := backup.Create(context.Background(), cfg, svc, password, backup.Options{})
	if err != nil {
		t.Fatal(err)
	}

	verifyCfg := cfg
	verifyCfg.ConfigDir = t.TempDir()
	w := &web{cfg: verifyCfg, service: app.New(verifyCfg)}
	r := multipartRestoreVerifyRequest(t, archive.Data, password)
	rr := httptest.NewRecorder()

	w.restoreVerifyAPI(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}
	if got, want := rr.Header().Get("Cache-Control"), "no-store"; got != want {
		t.Fatalf("Cache-Control = %q, want %q", got, want)
	}
	var payload struct {
		Report backup.VerifyReport `json:"report"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Report.ClientCount != 1 {
		t.Fatalf("client count = %d, want 1", payload.Report.ClientCount)
	}
	if payload.Report.ServerHost != cfg.ServerHost {
		t.Fatalf("server host = %q, want %q", payload.Report.ServerHost, cfg.ServerHost)
	}
	if _, err := os.Stat(filepath.Join(verifyCfg.ConfigDir, "state.json")); !os.IsNotExist(err) {
		t.Fatalf("restore verify must not write state into the target config dir, stat err = %v", err)
	}
}

func TestRestoreVerifyAPIRejectsWrongPassword(t *testing.T) {
	const password = "correct horse battery staple"
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
	if _, err := svc.AddClient("phone"); err != nil {
		t.Fatal(err)
	}
	archive, err := backup.Create(context.Background(), cfg, svc, password, backup.Options{})
	if err != nil {
		t.Fatal(err)
	}
	w := &web{cfg: cfg, service: svc}
	r := multipartRestoreVerifyRequest(t, archive.Data, "wrong password")
	rr := httptest.NewRecorder()

	w.restoreVerifyAPI(rr, r)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body = %s", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
	if got, want := rr.Header().Get("Cache-Control"), "no-store"; got != want {
		t.Fatalf("Cache-Control = %q, want %q", got, want)
	}
}

func TestBackupAPIUsesNoStore(t *testing.T) {
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
	if _, err := svc.AddClient("phone"); err != nil {
		t.Fatal(err)
	}
	w := &web{cfg: cfg, service: svc}
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:51821/api/backup", strings.NewReader(`{"password":"correct horse battery staple"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://127.0.0.1:51821")
	rr := httptest.NewRecorder()

	w.backupAPI(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}
	if got, want := rr.Header().Get("Cache-Control"), "no-store"; got != want {
		t.Fatalf("Cache-Control = %q, want %q", got, want)
	}
}

func TestBackupAPIRejectsOversizedJSONBody(t *testing.T) {
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
	w := &web{cfg: cfg, service: app.New(cfg)}
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:51821/api/backup", strings.NewReader(`{"password":"`+strings.Repeat("a", maxJSONBodyBytes)+`"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://127.0.0.1:51821")
	rr := httptest.NewRecorder()

	w.backupAPI(rr, r)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func multipartRestoreVerifyRequest(t *testing.T, archive []byte, password string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("password", password); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("backup", "backup.afbackup")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(archive); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:51821/api/restore/verify", &body)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	r.Header.Set("Origin", "http://127.0.0.1:51821")
	return r
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

func TestPublicTunnelReportsStaleClientCount(t *testing.T) {
	tunnel := config.Tunnel{
		ID:             "tunnel-1",
		Name:           "awg0",
		InterfaceName:  "awg0",
		Enabled:        true,
		ConfigRevision: 3,
		Clients: []config.Client{
			{ID: "fresh", ConfigRevision: 3},
			{ID: "stale", ConfigRevision: 2},
		},
	}

	payload := publicTunnel(tunnel, app.TunnelStatus{})
	status, ok := payload["status"].(map[string]any)
	if !ok {
		t.Fatal("status payload missing")
	}
	if got, want := status["stale_clients"], 1; got != want {
		t.Fatalf("stale_clients = %v, want %d", got, want)
	}
}

func TestPublicTunnelIncludesClientTrafficSummary(t *testing.T) {
	tunnel := config.Tunnel{
		ID: "tunnel-1",
		Clients: []config.Client{{
			ID:       "client-1",
			TunnelID: "tunnel-1",
			Name:     "phone",
		}},
	}
	payload := publicTunnelWithFirewall(tunnel, app.TunnelStatus{}, firewallSummary{}, nil, map[string]clientTrafficSummary{
		"client-1": {Enabled: true, RxTotal: 1024, TxTotal: 2048, LimitBytes: uint64Ptr(4096)},
	})
	clients := payload["clients"].([]map[string]any)
	traffic := clients[0]["traffic"].(map[string]any)
	if traffic["enabled"] != true || traffic["rx_total"] != uint64(1024) || traffic["tx_total"] != uint64(2048) {
		t.Fatalf("client traffic = %#v, want enabled totals", traffic)
	}
	if traffic["limit_bytes"] != uint64(4096) {
		t.Fatalf("limit_bytes = %#v, want 4096", traffic["limit_bytes"])
	}
	if traffic["exceeded"] != false {
		t.Fatalf("exceeded = %#v, want false", traffic["exceeded"])
	}
	payload = publicTunnelWithFirewall(tunnel, app.TunnelStatus{}, firewallSummary{}, nil, map[string]clientTrafficSummary{
		"client-1": {Enabled: true, RxTotal: 3072, TxTotal: 1024, LimitBytes: uint64Ptr(4096), Exceeded: true},
	})
	clients = payload["clients"].([]map[string]any)
	traffic = clients[0]["traffic"].(map[string]any)
	if traffic["exceeded"] != true {
		t.Fatalf("exceeded = %#v, want true", traffic["exceeded"])
	}
}

func TestPublicClientIncludesNotes(t *testing.T) {
	payload := publicClient(config.Client{ID: "client-1", Name: "phone", Notes: "router in office"})
	if got, want := payload["notes"], "router in office"; got != want {
		t.Fatalf("notes = %v, want %q", got, want)
	}
}

func TestPublicClientIncludesPersistentConnectionStatus(t *testing.T) {
	lastSeen := time.Date(2026, 6, 6, 10, 30, 0, 0, time.UTC)
	payload := publicClient(config.Client{ID: "client-1", Name: "phone", EverConnected: true, LastSeenAt: lastSeen})
	if got, want := payload["ever_connected"], true; got != want {
		t.Fatalf("ever_connected = %v, want %v", got, want)
	}
	if got, want := payload["last_seen_at"], "2026-06-06T10:30:00Z"; got != want {
		t.Fatalf("last_seen_at = %v, want %v", got, want)
	}
}

func TestPublicClientOmitsZeroLastSeen(t *testing.T) {
	payload := publicClient(config.Client{ID: "client-1", Name: "phone"})
	if got, want := payload["last_seen_at"], ""; got != want {
		t.Fatalf("last_seen_at = %v, want empty string", got)
	}
	runtime, ok := payload["runtime"].(map[string]any)
	if !ok {
		t.Fatalf("runtime has unexpected type %T", payload["runtime"])
	}
	if got, want := runtime["last_seen_at"], ""; got != want {
		t.Fatalf("runtime.last_seen_at = %v, want empty string", got)
	}
}

func TestPublicClientIncludesExpirationStatus(t *testing.T) {
	expiresAt := time.Now().UTC().Add(-time.Hour)
	payload := publicClient(config.Client{ID: "client-1", Name: "phone", Enabled: true, ExpiresAt: expiresAt})
	if got, want := payload["active"], false; got != want {
		t.Fatalf("active = %v, want %v", got, want)
	}
	if got, want := payload["expired"], true; got != want {
		t.Fatalf("expired = %v, want %v", got, want)
	}
	if got := payload["expires_at"]; got == "" {
		t.Fatal("expires_at is empty, want RFC3339 timestamp")
	}
}

func TestFirewallSummaryForTunnelFlagsMissingRules(t *testing.T) {
	tunnel := config.Tunnel{Name: "awg0", Enabled: true}
	report := firewall.Report{
		ApplyEnabled: true,
		Results: []firewall.RuleReport{
			{Tunnel: "awg0", Rule: "masquerade", Status: "ok"},
			{Tunnel: "awg0", Rule: "forward-in", Status: "missing"},
		},
	}

	got := firewallSummaryForTunnel(tunnel, report, nil)
	if got.Level != "bad" || got.Label != "firewall repair" {
		t.Fatalf("summary = %+v, want bad firewall repair", got)
	}
}

func TestFirewallSummaryForDisabledApplyModeIsNeutral(t *testing.T) {
	got := firewallSummaryForTunnel(config.Tunnel{Name: "awg0", Enabled: true}, firewall.Report{ApplyEnabled: false}, nil)
	if got.Level != "neutral" || got.Label != "firewall manual" {
		t.Fatalf("summary = %+v, want neutral firewall manual", got)
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
