package config_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/webtls"
)

func TestPublicBindRequiresPasswordButNotSessionSecret(t *testing.T) {
	t.Setenv("WEBUI_HOST", "0.0.0.0")
	t.Setenv("PASSWORD", "secret")
	t.Setenv("SESSION_SECRET", "")
	if _, err := config.FromEnv(); err != nil {
		t.Fatal(err)
	}
}

func TestPublicBindWithoutPasswordRejected(t *testing.T) {
	t.Setenv("WEBUI_HOST", "0.0.0.0")
	t.Setenv("PASSWORD", "")
	if _, err := config.FromEnv(); err == nil {
		t.Fatal("expected PASSWORD requirement")
	}
}

func TestSessionCookieSecureModeValidation(t *testing.T) {
	t.Setenv("SESSION_COOKIE_SECURE", "sometimes")
	if _, err := config.FromEnv(); err == nil {
		t.Fatal("expected SESSION_COOKIE_SECURE validation error")
	}
}

func TestTLSModeAndTrustedProxyValidation(t *testing.T) {
	t.Run("rejects unknown TLS mode", func(t *testing.T) {
		t.Setenv("WEBUI_TLS_MODE", "acme-domain")
		cfg, err := config.FromEnv()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := webtls.Load(cfg); err == nil {
			t.Fatal("expected TLS mode validation error")
		}
	})
	t.Run("manual requires cert and key", func(t *testing.T) {
		t.Setenv("WEBUI_TLS_MODE", "manual")
		cfg, err := config.FromEnv()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := webtls.Load(cfg); err == nil {
			t.Fatal("expected manual TLS file validation error")
		}
	})
	t.Run("trusted headers require CIDRs", func(t *testing.T) {
		t.Setenv("WEBUI_TRUST_PROXY_HEADERS", "true")
		if _, err := config.FromEnv(); err == nil {
			t.Fatal("expected trusted proxy CIDR validation error")
		}
	})
	t.Run("parses trusted proxy CIDRs", func(t *testing.T) {
		t.Setenv("WEBUI_TRUST_PROXY_HEADERS", "true")
		t.Setenv("WEBUI_TRUSTED_PROXY_CIDRS", "127.0.0.1/32, 10.0.0.0/8")
		cfg, err := config.FromEnv()
		if err != nil {
			t.Fatal(err)
		}
		if len(cfg.WebUITrustedProxyCIDRs) != 2 {
			t.Fatalf("trusted proxy CIDRs = %d, want 2", len(cfg.WebUITrustedProxyCIDRs))
		}
	})
	t.Run("reverse proxy requires password and trusted headers", func(t *testing.T) {
		t.Setenv("WEBUI_TLS_MODE", "reverse-proxy")
		cfg, err := config.FromEnv()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := webtls.Load(cfg); err == nil {
			t.Fatal("expected reverse proxy validation error")
		}
		t.Setenv("PASSWORD", "secret")
		cfg, err = config.FromEnv()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := webtls.Load(cfg); err == nil {
			t.Fatal("expected trusted proxy headers validation error")
		}
		t.Setenv("WEBUI_TRUST_PROXY_HEADERS", "true")
		t.Setenv("WEBUI_TRUSTED_PROXY_CIDRS", "127.0.0.1/32")
		cfg, err = config.FromEnv()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := webtls.Load(cfg); err != nil {
			t.Fatal(err)
		}
	})
}

func TestDatabaseConfigDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONFIG_DIR", dir)
	cfg, err := config.FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseMode != "off" {
		t.Fatalf("DatabaseMode = %q, want off", cfg.DatabaseMode)
	}
	if cfg.DatabasePath != filepath.Join(dir, "awg-forge.db") {
		t.Fatalf("DatabasePath = %q", cfg.DatabasePath)
	}
	if cfg.DatabaseBusyTimeout != 5*time.Second {
		t.Fatalf("DatabaseBusyTimeout = %s", cfg.DatabaseBusyTimeout)
	}
	if cfg.DatabaseQueryTimeout != 2*time.Second {
		t.Fatalf("DatabaseQueryTimeout = %s", cfg.DatabaseQueryTimeout)
	}
}

func TestDatabaseModeValidation(t *testing.T) {
	t.Setenv("DATABASE_MODE", "mysql")
	if _, err := config.FromEnv(); err == nil {
		t.Fatal("expected DATABASE_MODE validation error")
	}
}
