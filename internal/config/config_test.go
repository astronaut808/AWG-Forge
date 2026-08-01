package config_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
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

func TestLogLevelValidation(t *testing.T) {
	t.Run("accepts debug", func(t *testing.T) {
		t.Setenv("LOG_LEVEL", "debug")
		if _, err := config.FromEnv(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("rejects unknown level", func(t *testing.T) {
		t.Setenv("LOG_LEVEL", "verbose")
		if _, err := config.FromEnv(); err == nil {
			t.Fatal("expected LOG_LEVEL validation error")
		}
	})
}

func TestTrustedProxyValidation(t *testing.T) {
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
	if cfg.LogLevel != "info" {
		t.Fatalf("LogLevel = %q, want info", cfg.LogLevel)
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
