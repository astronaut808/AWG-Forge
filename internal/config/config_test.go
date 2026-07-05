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
