package config_test

import (
	"testing"

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
