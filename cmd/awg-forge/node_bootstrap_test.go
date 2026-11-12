package main

import (
	"context"
	"encoding/base64"
	"github.com/astronaut808/awg-forge/internal/buildinfo"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNodeSecretPrivateFDAndCancellation(t *testing.T) {
	secret := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	for _, body := range []string{secret, secret + "\n", "short\n", strings.Repeat("A", 45), secret + "x"} {
		p := filepath.Join(t.TempDir(), "token")
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		got, err := readNodeSecret(context.Background(), int(f.Fd()))
		_ = f.Close()
		valid := body == secret || body == secret+"\n"
		if valid && (err != nil || got != secret) || !valid && err == nil {
			t.Fatal("FD input validation failed")
		}
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := readNodeSecret(ctx, int(r.Fd())); err == nil || time.Since(start) > time.Second {
		t.Fatal("secret read ignored cancellation")
	}
	if _, err := readNodeSecret(context.Background(), 1024); err == nil {
		t.Fatal("closed FD accepted")
	}
	p := filepath.Join(t.TempDir(), "public-token")
	if err := os.WriteFile(p, []byte(secret), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := readNodeSecret(context.Background(), int(f.Fd())); err == nil {
		t.Fatal("public token file accepted")
	}
}

func TestNodeInvitationNoFollowLinksAndBoundedStrictJSON(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "invite")
	if err := os.WriteFile(p, []byte(`{"invitation_id":"x"}`), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(d, "link")
	if err := os.Symlink(p, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readNodeInvitation(link); err == nil {
		t.Fatal("symlink admitted")
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(p, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readNodeInvitation(p); err == nil {
		t.Fatal("hardlink admitted")
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"secret":"x","unknown":1}`, `{} {}`, strings.Repeat("x", 65537), `{"secret":`} {
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readNodeInvitation(p); err == nil {
			t.Fatal("malformed or oversized invitation admitted")
		}
	}
}

func TestInstallerCheckUsesCompiledMetadataAndRejectsUnknownBuild(t *testing.T) {
	oldV, oldC := buildinfo.Version, buildinfo.Commit
	defer func() { buildinfo.Version = oldV; buildinfo.Commit = oldC }()
	buildinfo.Version = "local-0123456789ab"
	buildinfo.Commit = strings.Repeat("a", 40)
	t.Setenv("AWG_FORGE_VERSION", "v999.0.0")
	t.Setenv("AWG_FORGE_COMMIT", strings.Repeat("b", 40))
	if err := runNodeInstallerCheck([]string{"--artifact-version", buildinfo.Version, "--artifact-commit", buildinfo.Commit}); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"dev", "unknown", "latest", "v999.0.0", "local-0123456789ab;touch x"} {
		if err := runNodeInstallerCheck([]string{"--artifact-version", v, "--artifact-commit", buildinfo.Commit}); err == nil {
			t.Fatal("unsupported artifact admitted")
		}
	}
}
