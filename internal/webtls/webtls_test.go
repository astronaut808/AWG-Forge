package webtls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
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
