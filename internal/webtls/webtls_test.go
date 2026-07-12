package webtls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
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
		ConfigDir:          t.TempDir(),
		WebUITLSMode:       string(ModeManual),
		WebUITLSCertFile:   certPath,
		WebUITLSKeyFile:    keyPath,
		WebUITLSServerName: "panel.example.com",
	}
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
	_, err := Load(config.Config{
		ConfigDir:        t.TempDir(),
		WebUITLSMode:     string(ModeManual),
		WebUITLSCertFile: certPath,
		WebUITLSKeyFile:  keyPath,
	})
	if err == nil {
		t.Fatal("expected insecure key permission error")
	}
}

func TestManualCertificateRejectsInsecureKeyDirectory(t *testing.T) {
	certPath, keyPath := writeCertificate(t, "panel.example.com", nil)
	if err := os.Chmod(filepath.Dir(keyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Load(config.Config{
		ConfigDir:        t.TempDir(),
		WebUITLSMode:     string(ModeManual),
		WebUITLSCertFile: certPath,
		WebUITLSKeyFile:  keyPath,
	})
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
	_, err := Load(config.Config{
		ConfigDir:        t.TempDir(),
		WebUITLSMode:     string(ModeManual),
		WebUITLSCertFile: certPath,
		WebUITLSKeyFile:  linkPath,
	})
	if err == nil {
		t.Fatal("expected key symlink error")
	}
}

func TestManualCertificateRejectsMismatchedServerName(t *testing.T) {
	certPath, keyPath := writeCertificate(t, "panel.example.com", nil)
	_, err := Load(config.Config{
		ConfigDir:          t.TempDir(),
		WebUITLSMode:       string(ModeManual),
		WebUITLSCertFile:   certPath,
		WebUITLSKeyFile:    keyPath,
		WebUITLSServerName: "other.example.com",
	})
	if err == nil {
		t.Fatal("expected certificate server-name mismatch error")
	}
}

func TestSaveOverridesEnvironmentSettings(t *testing.T) {
	cfg := config.Config{
		ConfigDir:              t.TempDir(),
		WebUITLSMode:           string(ModeOff),
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
	if status.Mode != ModeReverseProxy || status.Source != SourceManaged {
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

func TestPersistedSettingsOverrideIncompleteManualEnvironment(t *testing.T) {
	cfg := config.Config{ConfigDir: t.TempDir(), WebUITLSMode: string(ModeManual)}
	if err := Save(cfg, Settings{Mode: ModeOff}); err != nil {
		t.Fatal(err)
	}
	runtime, err := Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	status := runtime.Status
	if status.Mode != ModeOff {
		t.Fatalf("TLS mode = %q, want off", status.Mode)
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

func TestUseEnvironmentKeepsManagedSettingsWhenEnvironmentIsInvalid(t *testing.T) {
	cfg := config.Config{ConfigDir: t.TempDir(), WebUITLSMode: string(ModeManual)}
	if err := Save(cfg, Settings{Mode: ModeOff}); err != nil {
		t.Fatal(err)
	}
	if _, err := UseEnvironment(cfg); err == nil {
		t.Fatal("expected invalid environment error")
	}
	if _, err := os.Stat(filepath.Join(cfg.ConfigDir, filepath.FromSlash(SettingsRelativePath))); err != nil {
		t.Fatalf("managed settings were removed after invalid environment: %v", err)
	}
}

func TestUseEnvironmentRemovesManagedSettingsAfterValidation(t *testing.T) {
	certPath, keyPath := writeCertificate(t, "panel.example.com", nil)
	cfg := config.Config{
		ConfigDir:        t.TempDir(),
		WebUITLSMode:     string(ModeManual),
		WebUITLSCertFile: certPath,
		WebUITLSKeyFile:  keyPath,
	}
	if err := Save(cfg, Settings{Mode: ModeOff}); err != nil {
		t.Fatal(err)
	}
	runtime, err := UseEnvironment(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Source != SourceEnvironment || runtime.Settings.Mode != ModeManual {
		t.Fatalf("runtime = %#v", runtime)
	}
	if _, err := os.Stat(filepath.Join(cfg.ConfigDir, filepath.FromSlash(SettingsRelativePath))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed settings still exist: %v", err)
	}
}

func TestLoadSettingsSourceMatrix(t *testing.T) {
	certPath, keyPath := writeCertificate(t, "panel.example.com", nil)
	tests := []struct {
		name       string
		setup      func(t *testing.T) config.Config
		wantMode   Mode
		wantSource Source
		wantErr    bool
	}{
		{
			name: "environment off without managed settings",
			setup: func(t *testing.T) config.Config {
				return config.Config{ConfigDir: t.TempDir(), WebUITLSMode: string(ModeOff)}
			},
			wantMode: ModeOff, wantSource: SourceEnvironment,
		},
		{
			name: "environment manual without managed settings",
			setup: func(t *testing.T) config.Config {
				return config.Config{ConfigDir: t.TempDir(), WebUITLSMode: string(ModeManual), WebUITLSCertFile: certPath, WebUITLSKeyFile: keyPath}
			},
			wantMode: ModeManual, wantSource: SourceEnvironment,
		},
		{
			name: "managed off overrides incomplete environment manual",
			setup: func(t *testing.T) config.Config {
				cfg := config.Config{ConfigDir: t.TempDir(), WebUITLSMode: string(ModeManual)}
				if err := Save(cfg, Settings{Mode: ModeOff}); err != nil {
					t.Fatal(err)
				}
				return cfg
			},
			wantMode: ModeOff, wantSource: SourceManaged,
		},
		{
			name: "managed manual does not fall back to environment off",
			setup: func(t *testing.T) config.Config {
				cfg := config.Config{ConfigDir: t.TempDir(), WebUITLSMode: string(ModeOff)}
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
			name: "malformed managed settings do not fall back to environment",
			setup: func(t *testing.T) config.Config {
				cfg := config.Config{ConfigDir: t.TempDir(), WebUITLSMode: string(ModeOff)}
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
			if runtime.Settings.Mode != test.wantMode || runtime.Source != test.wantSource {
				t.Fatalf("runtime = %#v", runtime)
			}
		})
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
