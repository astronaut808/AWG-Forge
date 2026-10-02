package main

import (
	"github.com/astronaut808/awg-forge/internal/config"
	"os"
	"path/filepath"
	"testing"
)

func TestReadNodeInvitationRequiresPrivateRegularFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "invite.json")
	if err := os.WriteFile(path, []byte(`{"invitation_id":"x"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readNodeInvitation(path); err != nil {
		t.Fatalf("private input = %v", err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readNodeInvitation(path); err == nil {
		t.Fatal("world-readable input was accepted")
	}
}

func TestRunNodeRejectsSecretBearingArgumentShapes(t *testing.T) {
	if err := runNode(config.Config{}, nil, []string{"enroll", "--secret", "x"}); err == nil {
		t.Fatal("unknown secret flag accepted")
	}
}
