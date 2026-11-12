package config_test

import (
	"testing"

	"github.com/astronaut808/awg-forge/internal/config"
)

func TestWebUIUsernameDefaultsToAdmin(t *testing.T) {
	t.Setenv("WEBUI_USERNAME", "")
	cfg, err := config.FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.WebUIUsername, "admin"; got != want {
		t.Fatalf("WebUIUsername = %q, want %q", got, want)
	}
	if got, want := (config.Config{}).WebUIUsernameOrDefault(), "admin"; got != want {
		t.Fatalf("zero-value username = %q, want %q", got, want)
	}
}

func TestWebUIUsernameReadsEnvironment(t *testing.T) {
	t.Setenv("WEBUI_USERNAME", "operator")
	cfg, err := config.FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.WebUIUsername, "operator"; got != want {
		t.Fatalf("WebUIUsername = %q, want %q", got, want)
	}
}
