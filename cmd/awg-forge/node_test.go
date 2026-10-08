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

func TestNodeRecoveryRequiresLinuxRootAndExplicitConfirmation(t *testing.T) {
	for _, a := range []struct {
		os  string
		uid int
	}{{"darwin", 0}, {"linux", 1000}} {
		if err := runNodeRecoveryWithAuthority(config.Config{}, nil, []string{"detach"}, a.os, a.uid); err == nil {
			t.Fatal("unauthorized recovery admitted")
		}
	}
	for _, args := range [][]string{{"detach"}, {"detach", "--secret", "x"}, {"rebind", "--input-file", "unused"}, {"detach", "--confirm-node-id", "bad", "--confirm-controller-id", "bad", "extra"}} {
		if err := runNodeRecoveryWithAuthority(config.Config{}, nil, args, "linux", 0); err == nil {
			t.Fatal("unsafe or unconfirmed recovery admitted")
		}
	}
}
