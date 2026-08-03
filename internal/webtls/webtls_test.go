package webtls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
)

func TestServerConfigLoadsValidManualCertificate(t *testing.T) {
	certPath, keyPath := writeCertificate(t, "panel.example.com", nil)
	cfg := config.Config{
		ConfigDir: t.TempDir(),
	}
	saveSettings(t, cfg, Settings{Mode: ModeManual, CertFile: certPath, KeyFile: keyPath, ServerName: "panel.example.com"})
	runtime, err := Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	tlsConfig := runtime.TLSConfig
	status := runtime.Status
	if tlsConfig == nil || len(tlsConfig.Certificates) != 1 {
		t.Fatal("manual TLS config should include one certificate")
	}
	if status.Mode != ModeManual || status.Subject == "" || status.NotAfter.IsZero() {
		t.Fatalf("unexpected manual TLS status: %#v", status)
	}
}

func TestManualCertificateRejectsInsecureKeyPermissions(t *testing.T) {
	certPath, keyPath := writeCertificate(t, "panel.example.com", nil)
	if err := os.Chmod(keyPath, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{ConfigDir: t.TempDir()}
	saveSettingsFile(t, cfg, Settings{Mode: ModeManual, CertFile: certPath, KeyFile: keyPath})
	_, err := Load(cfg)
	if err == nil {
		t.Fatal("expected insecure key permission error")
	}
}

func TestManualCertificateRejectsInsecureKeyDirectory(t *testing.T) {
	certPath, keyPath := writeCertificate(t, "panel.example.com", nil)
	if err := os.Chmod(filepath.Dir(keyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{ConfigDir: t.TempDir()}
	saveSettingsFile(t, cfg, Settings{Mode: ModeManual, CertFile: certPath, KeyFile: keyPath})
	_, err := Load(cfg)
	if err == nil {
		t.Fatal("expected insecure key directory permission error")
	}
}

func TestManualCertificateRejectsKeySymlink(t *testing.T) {
	certPath, keyPath := writeCertificate(t, "panel.example.com", nil)
	linkPath := filepath.Join(t.TempDir(), "key.pem")
	if err := os.Symlink(keyPath, linkPath); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{ConfigDir: t.TempDir()}
	saveSettingsFile(t, cfg, Settings{Mode: ModeManual, CertFile: certPath, KeyFile: linkPath})
	_, err := Load(cfg)
	if err == nil {
		t.Fatal("expected key symlink error")
	}
}

func TestManualCertificateRejectsMismatchedServerName(t *testing.T) {
	certPath, keyPath := writeCertificate(t, "panel.example.com", nil)
	cfg := config.Config{ConfigDir: t.TempDir()}
	saveSettingsFile(t, cfg, Settings{Mode: ModeManual, CertFile: certPath, KeyFile: keyPath, ServerName: "other.example.com"})
	_, err := Load(cfg)
	if err == nil {
		t.Fatal("expected certificate server-name mismatch error")
	}
}

func TestACMEDomainBuildsHTTP01Runtime(t *testing.T) {
	cfg := config.Config{ConfigDir: t.TempDir(), Password: "secret", WebUIHost: "0.0.0.0", WebUIPort: 8443, SessionCookieSecure: "auto"}
	runtime, err := buildRuntime(cfg, Settings{Mode: ModeACMEDomain, ACMEDomain: "Panel.Example.com.", ACMEEmail: "admin@example.com", ACMEAcceptTOS: true})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.TLSConfig == nil || runtime.ACMEHTTPHandler == nil {
		t.Fatal("ACME runtime did not configure HTTPS and HTTP-01 handlers")
	}
	if runtime.Status.Domain != "panel.example.com" || runtime.Status.State != "pending" {
		t.Fatalf("unexpected ACME status: %#v", runtime.Status)
	}
	info, err := os.Stat(filepath.Join(cfg.ConfigDir, filepath.FromSlash(ACMECacheRelativePath)))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("ACME cache permissions = %v, want 0700", info.Mode())
	}

	request := httptest.NewRequest(http.MethodGet, "http://panel.example.com/settings?view=tls", nil)
	request.Host = "panel.example.com"
	response := httptest.NewRecorder()
	runtime.ACMEHTTPHandler.ServeHTTP(response, request)
	if response.Code != http.StatusMovedPermanently || response.Header().Get("Location") != "https://panel.example.com:8443/" {
		t.Fatalf("ACME fallback = %d %q", response.Code, response.Header().Get("Location"))
	}

	request = httptest.NewRequest(http.MethodGet, "http://panel.example.com/", nil)
	request.Host = "panel.example.com"
	request.URL.Scheme = "https"
	request.URL.Host = "attacker.example"
	request.URL.Path = "/phish"
	response = httptest.NewRecorder()
	runtime.ACMEHTTPHandler.ServeHTTP(response, request)
	if location := response.Header().Get("Location"); location != "https://panel.example.com:8443/" {
		t.Fatalf("ACME fallback reflected request target: %q", location)
	}

	request = httptest.NewRequest(http.MethodGet, "http://attacker.example/", nil)
	request.Host = "attacker.example"
	response = httptest.NewRecorder()
	runtime.ACMEHTTPHandler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("unexpected host status = %d, want 404", response.Code)
	}
}

func TestACMEIPBuildsIsolatedHTTP01Runtime(t *testing.T) {
	cfg := config.Config{ConfigDir: t.TempDir(), Password: "secret", WebUIHost: "0.0.0.0", WebUIPort: 8443, SessionCookieSecure: "auto"}
	runtime, err := buildRuntime(cfg, Settings{Mode: ModeACMEIP, ACMEIP: "8.8.8.8", ACMEEmail: "admin@example.com", ACMEAcceptTOS: true})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.TLSConfig == nil || runtime.ACMEHTTPHandler == nil || runtime.Status.IP != "8.8.8.8" || runtime.Status.State != "pending" {
		t.Fatalf("unexpected ACME IP runtime: %#v", runtime)
	}
	response := httptest.NewRecorder()
	runtime.ACMEHTTPHandler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://8.8.8.8/", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("ACME IP handler exposed a non-challenge request: %d", response.Code)
	}
}

func TestACMEIPRequiresMatchingWebUIBind(t *testing.T) {
	tests := []struct {
		name    string
		host    string
		ip      string
		wantErr bool
	}{
		{name: "IPv4 wildcard", host: "0.0.0.0", ip: "8.8.8.8"},
		{name: "IPv4 exact address", host: "8.8.8.8", ip: "8.8.8.8"},
		{name: "IPv6 wildcard", host: "::", ip: "2001:4860:4860::8888"},
		{name: "IPv6 exact address", host: "2001:4860:4860::8888", ip: "2001:4860:4860::8888"},
		{name: "IPv4 bind for IPv6 certificate", host: "0.0.0.0", ip: "2001:4860:4860::8888", wantErr: true},
		{name: "IPv6 bind for IPv4 certificate", host: "::", ip: "8.8.8.8", wantErr: true},
		{name: "different public address", host: "1.1.1.1", ip: "8.8.8.8", wantErr: true},
		{name: "hostname", host: "panel.example.com", ip: "8.8.8.8", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := config.Config{
				ConfigDir:           t.TempDir(),
				Password:            "secret",
				WebUIHost:           test.host,
				WebUIPort:           8443,
				SessionCookieSecure: "auto",
			}
			_, err := buildRuntime(cfg, Settings{Mode: ModeACMEIP, ACMEIP: test.ip, ACMEEmail: "admin@example.com", ACMEAcceptTOS: true})
			if (err != nil) != test.wantErr {
				t.Fatalf("buildRuntime error = %v, want error: %t", err, test.wantErr)
			}
		})
	}
}

func TestACMEIPHTTP01HandlerOnlyServesLiveGETToken(t *testing.T) {
	state := &acmeIPState{tokens: map[string]string{"token": "token.authorization"}}
	handler := state.httpHandler()
	tests := []struct {
		name       string
		method     string
		path       string
		statusCode int
		body       string
	}{
		{name: "active token", method: http.MethodGet, path: "/.well-known/acme-challenge/token", statusCode: http.StatusOK, body: "token.authorization"},
		{name: "unknown token", method: http.MethodGet, path: "/.well-known/acme-challenge/other", statusCode: http.StatusNotFound},
		{name: "nested path", method: http.MethodGet, path: "/.well-known/acme-challenge/token/extra", statusCode: http.StatusNotFound},
		{name: "head", method: http.MethodHead, path: "/.well-known/acme-challenge/token", statusCode: http.StatusNotFound},
		{name: "post", method: http.MethodPost, path: "/.well-known/acme-challenge/token", statusCode: http.StatusNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(test.method, "http://example.test"+test.path, nil))
			if response.Code != test.statusCode {
				t.Fatalf("status = %d, want %d", response.Code, test.statusCode)
			}
			if test.body != "" && response.Body.String() != test.body {
				t.Fatalf("body = %q, want %q", response.Body.String(), test.body)
			}
			if test.body != "" {
				if got, want := response.Header().Get("Content-Type"), "text/plain; charset=utf-8"; got != want {
					t.Fatalf("Content-Type = %q, want %q", got, want)
				}
				if got, want := response.Header().Get("Cache-Control"), "no-store"; got != want {
					t.Fatalf("Cache-Control = %q, want %q", got, want)
				}
				if got, want := response.Header().Get("X-Content-Type-Options"), "nosniff"; got != want {
					t.Fatalf("X-Content-Type-Options = %q, want %q", got, want)
				}
			}
		})
	}
}

func TestACMEIPRetryScheduleAndInitialAttempt(t *testing.T) {
	now := time.Date(2026, time.August, 2, 12, 0, 0, 0, time.UTC)
	state := &acmeIPState{status: Status{Mode: ModeACMEIP, State: "pending"}}
	if got := state.nextAttemptDelay(now); got != 0 {
		t.Fatalf("initial delay = %s, want immediate attempt", got)
	}
	state.status.NextAttempt = now.Add(5 * time.Minute)
	if got, want := state.nextAttemptDelay(now), 5*time.Minute; got != want {
		t.Fatalf("persisted delay = %s, want %s", got, want)
	}
	for attempt, want := range map[int]time.Duration{1: time.Minute, 2: 2 * time.Minute, 3: 4 * time.Minute, 7: time.Hour} {
		if got := acmeIPRetryDelay(attempt); got != want {
			t.Fatalf("attempt %d delay = %s, want %s", attempt, got, want)
		}
	}
}

func TestACMEIPRestoresRenewalBackoffForCachedCertificate(t *testing.T) {
	cfg := config.Config{ConfigDir: t.TempDir(), Password: "secret", WebUIHost: "0.0.0.0", WebUIPort: 8443, SessionCookieSecure: "auto"}
	ip := netip.MustParseAddr("8.8.8.8")
	cacheDir := filepath.Join(cfg.ConfigDir, filepath.FromSlash(ACMECacheRelativePath), "ip")
	if err := ensurePrivateDirectory(cacheDir); err != nil {
		t.Fatal(err)
	}
	certPath, keyPath := writeCertificate(t, "unused.example", []net.IP{net.IP(ip.AsSlice())})
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := saveACMEIPCertificate(cacheDir, pair); err != nil {
		t.Fatal(err)
	}
	next := time.Now().UTC().Add(10 * time.Minute).Truncate(time.Second)
	if err := saveACMEIPStatus(cacheDir, Status{Mode: ModeACMEIP, IP: ip.String(), State: "active", Warning: "certificate renewal failed", NextAttempt: next, AttemptCount: 3}); err != nil {
		t.Fatal(err)
	}
	runtime, err := buildRuntime(cfg, Settings{Mode: ModeACMEIP, ACMEIP: ip.String(), ACMEEmail: "admin@example.com", ACMEAcceptTOS: true})
	if err != nil {
		t.Fatal(err)
	}
	status := runtime.ReadStatus()
	if status.Warning != "certificate renewal failed" || !status.NextAttempt.Equal(next) || status.AttemptCount != 3 {
		t.Fatalf("renewal backoff was not restored: %#v", status)
	}
}

func TestACMEIPDoesNotTreatStatusAsCertificate(t *testing.T) {
	cfg := config.Config{ConfigDir: t.TempDir(), Password: "secret", WebUIHost: "0.0.0.0", WebUIPort: 8443, SessionCookieSecure: "auto"}
	ip := netip.MustParseAddr("8.8.8.8")
	cacheDir := filepath.Join(cfg.ConfigDir, filepath.FromSlash(ACMECacheRelativePath), "ip")
	if err := ensurePrivateDirectory(cacheDir); err != nil {
		t.Fatal(err)
	}
	if err := saveACMEIPStatus(cacheDir, Status{Mode: ModeACMEIP, IP: ip.String(), State: "active", NextAttempt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	runtime, err := buildRuntime(cfg, Settings{Mode: ModeACMEIP, ACMEIP: ip.String(), ACMEEmail: "admin@example.com", ACMEAcceptTOS: true})
	if err != nil {
		t.Fatal(err)
	}
	status := runtime.ReadStatus()
	if status.State != "pending" || !status.NextAttempt.IsZero() {
		t.Fatalf("missing certificate must retry immediately, got %#v", status)
	}
}

func TestACMEIPRejectsUnsafeCacheFiles(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "ip")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(cacheDir, "certificate.pem")); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := loadACMEIPCertificate(cacheDir, netip.MustParseAddr("8.8.8.8")); err == nil {
		t.Fatal("expected certificate cache symlink to be rejected")
	}
	if err := os.Remove(filepath.Join(cacheDir, "certificate.pem")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(cacheDir, "status.json")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadACMEIPStatus(cacheDir, netip.MustParseAddr("8.8.8.8")); err == nil {
		t.Fatal("expected status cache symlink to be rejected")
	}
}

func TestACMEIPRejectsNonPublicAddresses(t *testing.T) {
	cfg := config.Config{ConfigDir: t.TempDir(), Password: "secret", WebUIHost: "0.0.0.0", WebUIPort: 8443, SessionCookieSecure: "auto"}
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "::1", "::", "not-an-ip"} {
		if _, err := buildRuntime(cfg, Settings{Mode: ModeACMEIP, ACMEIP: ip, ACMEEmail: "admin@example.com", ACMEAcceptTOS: true}); err == nil {
			t.Fatalf("%s: expected validation error", ip)
		}
	}
}

func TestACMEFallbackRedirectNeverReflectsRequestTarget(t *testing.T) {
	handler := acmeFallback("panel.example.com", 8443)
	tests := []struct {
		name    string
		request *http.Request
	}{
		{
			name:    "scheme relative path",
			request: httptest.NewRequest(http.MethodGet, "http://panel.example.com//attacker.example/login?next=https://evil.example", nil),
		},
		{
			name: "absolute request target",
			request: func() *http.Request {
				request := httptest.NewRequest(http.MethodGet, "http://panel.example.com/", nil)
				request.RequestURI = "https://attacker.example/login?next=https://evil.example"
				request.URL.Scheme = "https"
				request.URL.Host = "attacker.example"
				request.URL.Path = "/login"
				request.URL.RawQuery = "next=https://evil.example"
				return request
			}(),
		},
		{
			name:    "encoded path",
			request: httptest.NewRequest(http.MethodGet, "http://panel.example.com/%2F%2Fevil.example?return_to=%2F%2Fevil.example", nil),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.request.Host = "panel.example.com"
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, test.request)
			if response.Code != http.StatusMovedPermanently {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusMovedPermanently)
			}
			if location := response.Header().Get("Location"); location != "https://panel.example.com:8443/" {
				t.Fatalf("Location = %q, want canonical URL", location)
			}
		})
	}
}

func TestACMEDomainRejectsUnsafeDeployment(t *testing.T) {
	base := config.Config{ConfigDir: t.TempDir(), Password: "secret", WebUIHost: "0.0.0.0", WebUIPort: 443, SessionCookieSecure: "auto"}
	settings := Settings{Mode: ModeACMEDomain, ACMEDomain: "panel.example.com", ACMEEmail: "admin@example.com", ACMEAcceptTOS: true}
	tests := []struct {
		name   string
		mutate func(*config.Config, *Settings)
	}{
		{name: "loopback bind", mutate: func(cfg *config.Config, _ *Settings) { cfg.WebUIHost = "127.0.0.1" }},
		{name: "insecure cookie", mutate: func(cfg *config.Config, _ *Settings) { cfg.SessionCookieSecure = "false" }},
		{name: "trusted proxy", mutate: func(cfg *config.Config, _ *Settings) { cfg.WebUITrustProxyHeaders = true }},
		{name: "IP address", mutate: func(_ *config.Config, settings *Settings) { settings.ACMEDomain = "203.0.113.4" }},
		{name: "missing tos", mutate: func(_ *config.Config, settings *Settings) { settings.ACMEAcceptTOS = false }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := base
			cfg.ConfigDir = t.TempDir()
			candidate := settings
			test.mutate(&cfg, &candidate)
			if _, err := buildRuntime(cfg, candidate); err == nil {
				t.Fatal("expected ACME deployment validation failure")
			}
		})
	}
}

func TestSaveAndLoadTLSSettings(t *testing.T) {
	cfg := config.Config{
		ConfigDir:              t.TempDir(),
		Password:               "secret",
		WebUITrustProxyHeaders: true,
		WebUITrustedProxyCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")},
	}
	if err := Save(cfg, Settings{Mode: ModeReverseProxy}); err != nil {
		t.Fatal(err)
	}
	runtime, err := Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	status := runtime.Status
	if status.Mode != ModeReverseProxy {
		t.Fatalf("status = %#v", status)
	}
	info, err := os.Stat(filepath.Join(cfg.ConfigDir, filepath.FromSlash(SettingsRelativePath)))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("settings mode = %o, want 600", info.Mode().Perm())
	}
}

func TestPersistedReverseProxyRequiresSecureRuntimeSettings(t *testing.T) {
	secureCfg := config.Config{
		ConfigDir:              t.TempDir(),
		Password:               "secret",
		WebUITrustProxyHeaders: true,
		WebUITrustedProxyCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")},
	}
	if err := Save(secureCfg, Settings{Mode: ModeReverseProxy}); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(config.Config{ConfigDir: secureCfg.ConfigDir}); err == nil {
		t.Fatal("expected reverse-proxy runtime settings error")
	}
}

func TestLoadSettingsFileMatrix(t *testing.T) {
	certPath, keyPath := writeCertificate(t, "panel.example.com", nil)
	tests := []struct {
		name     string
		setup    func(t *testing.T) config.Config
		wantMode Mode
		wantErr  bool
	}{
		{
			name: "missing settings defaults to off",
			setup: func(t *testing.T) config.Config {
				return config.Config{ConfigDir: t.TempDir()}
			},
			wantMode: ModeOff,
		},
		{
			name: "saved off configuration",
			setup: func(t *testing.T) config.Config {
				cfg := config.Config{ConfigDir: t.TempDir()}
				if err := Save(cfg, Settings{Mode: ModeOff}); err != nil {
					t.Fatal(err)
				}
				return cfg
			},
			wantMode: ModeOff,
		},
		{
			name: "manual configuration does not ignore insecure key permissions",
			setup: func(t *testing.T) config.Config {
				cfg := config.Config{ConfigDir: t.TempDir()}
				if err := Save(cfg, Settings{Mode: ModeManual, CertFile: certPath, KeyFile: keyPath}); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(keyPath, 0o644); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(keyPath, 0o600) })
				return cfg
			},
			wantErr: true,
		},
		{
			name: "malformed settings do not fall back",
			setup: func(t *testing.T) config.Config {
				cfg := config.Config{ConfigDir: t.TempDir()}
				dir := filepath.Join(cfg.ConfigDir, "tls")
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("not json"), 0o600); err != nil {
					t.Fatal(err)
				}
				return cfg
			},
			wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := test.setup(t)
			runtime, err := Load(cfg)
			if test.wantErr {
				if err == nil {
					t.Fatal("expected Load error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if runtime.Settings.Mode != test.wantMode {
				t.Fatalf("runtime = %#v", runtime)
			}
		})
	}
}

func TestLoadIgnoresLegacyTLSEnvironment(t *testing.T) {
	certPath, keyPath := writeCertificate(t, "panel.example.com", nil)
	t.Setenv("CONFIG_DIR", t.TempDir())
	t.Setenv("WEBUI_TLS_MODE", "manual")
	t.Setenv("WEBUI_TLS_CERT_FILE", certPath)
	t.Setenv("WEBUI_TLS_KEY_FILE", keyPath)

	cfg, err := config.FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Status.Mode != ModeOff {
		t.Fatalf("legacy environment enabled TLS mode %q", runtime.Status.Mode)
	}
}

func saveSettings(t *testing.T, cfg config.Config, settings Settings) {
	t.Helper()
	if err := Save(cfg, settings); err != nil {
		t.Fatal(err)
	}
}

func saveSettingsFile(t *testing.T, cfg config.Config, settings Settings) {
	t.Helper()
	dir := filepath.Dir(settingsPath(cfg))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath(cfg), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeCertificate(t *testing.T, dnsName string, ips []net.IP) (string, string) {
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
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: dnsName},
		DNSNames:     []string{dnsName},
		IPAddresses:  ips,
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
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
