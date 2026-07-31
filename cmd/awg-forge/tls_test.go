package main

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/webtls"
)

func TestTLSUseACMEDomainSavesValidatedManagedSettings(t *testing.T) {
	cfg := config.Config{
		ConfigDir:           t.TempDir(),
		Password:            "admin-password",
		WebUIHost:           "0.0.0.0",
		WebUIPort:           8443,
		SessionCookieSecure: "auto",
	}
	svc := app.New(cfg)
	if _, err := svc.Init(); err != nil {
		t.Fatal(err)
	}
	if err := runTLS(cfg, svc, []string{"use", "acme-domain", "--domain", "Panel.Example.com", "--email", "admin@example.com", "--accept-tos"}); err != nil {
		t.Fatal(err)
	}
	runtime, err := webtls.Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Status.Mode != webtls.ModeACMEDomain || runtime.Settings.ACMEDomain != "panel.example.com" {
		t.Fatalf("unexpected ACME runtime: %#v", runtime)
	}
	data, err := os.ReadFile(filepath.Join(cfg.ConfigDir, filepath.FromSlash(webtls.SettingsRelativePath)))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "Panel.Example.com") || !strings.Contains(string(data), "panel.example.com") {
		t.Fatalf("managed settings did not persist the normalized domain: %s", data)
	}
}

func TestTLSUseReverseProxySavesSettings(t *testing.T) {
	cfg := config.Config{
		ConfigDir:              t.TempDir(),
		Password:               "admin-password",
		WebUITrustProxyHeaders: true,
		WebUITrustedProxyCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")},
	}
	svc := app.New(cfg)
	if _, err := svc.Init(); err != nil {
		t.Fatal(err)
	}
	if err := runTLS(cfg, svc, []string{"use", "reverse-proxy"}); err != nil {
		t.Fatal(err)
	}
	runtime, err := webtls.Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Status.Mode != webtls.ModeReverseProxy {
		t.Fatalf("mode = %q, want %q", runtime.Status.Mode, webtls.ModeReverseProxy)
	}
}

func TestTLSUseEnvironmentIsRejected(t *testing.T) {
	if err := runTLS(config.Config{}, app.New(config.Config{}), []string{"use", "environment"}); err == nil {
		t.Fatal("environment must not be a TLS configuration source")
	}
}
