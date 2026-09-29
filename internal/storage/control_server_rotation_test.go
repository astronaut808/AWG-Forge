package storage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlpki"
)

func TestControlServerRotationJournalStrictAndPrivate(t *testing.T) {
	store, journal, _, _ := controlServerRotationFixture(t)
	if err := store.SaveControlServerRotationJournal(journal, nil); err != nil {
		t.Fatal(err)
	}
	assertControlRotationMode(t, store.ControlServerRotationJournalPath(), 0600)
	if got, err := store.LoadControlServerRotationJournal(); err != nil || got != journal {
		t.Fatalf("load journal = %#v, %v", got, err)
	}

	for name, body := range map[string]string{
		"version":   `{"version":2,"controller_id":"11111111-1111-4111-8111-111111111111","ca_generation":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","old_server_generation":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","new_server_generation":"cccccccccccccccccccccccccccccccc","started_at":"2026-09-30T12:00:00Z"}`,
		"unknown":   `{"version":1,"controller_id":"11111111-1111-4111-8111-111111111111","ca_generation":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","old_server_generation":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","new_server_generation":"cccccccccccccccccccccccccccccccc","started_at":"2026-09-30T12:00:00Z","extra":true}`,
		"duplicate": `{"version":1,"version":1,"controller_id":"11111111-1111-4111-8111-111111111111","ca_generation":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","old_server_generation":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","new_server_generation":"cccccccccccccccccccccccccccccccc","started_at":"2026-09-30T12:00:00Z"}`,
		"trailing":  `{"version":1,"controller_id":"11111111-1111-4111-8111-111111111111","ca_generation":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","old_server_generation":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","new_server_generation":"cccccccccccccccccccccccccccccccc","started_at":"2026-09-30T12:00:00Z"} null`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(store.ControlServerRotationJournalPath(), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.LoadControlServerRotationJournal(); err == nil {
				t.Fatal("malformed rotation journal was accepted")
			}
			if err := store.CheckNoControlServerRotation(); err == nil {
				t.Fatal("malformed rotation journal did not fence work")
			}
		})
	}
	if err := os.Chmod(store.ControlServerRotationJournalPath(), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadControlServerRotationJournal(); err == nil {
		t.Fatal("non-private rotation journal was accepted")
	}
	if err := os.Remove(store.ControlServerRotationJournalPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(filepath.Dir(store.ControlServerRotationJournalPath()), "state.json"), store.ControlServerRotationJournalPath()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadControlServerRotationJournal(); err == nil {
		t.Fatal("symlinked rotation journal was accepted")
	}
}

func TestWriteControlServerGenerationIsImmutableAndPrivate(t *testing.T) {
	store, journal, old, pin := controlServerRotationFixture(t)
	endpoint, _ := controlpki.NormalizeEndpoint("127.0.0.1", "control.example.com", 8443, 51821)
	candidate, err := controlpki.RotateServerLeaf(old, endpoint, pin, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteControlServerGeneration(journal, candidate, nil); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadControlIdentity(journal.CAGeneration, journal.NewServerGeneration)
	if err != nil {
		t.Fatal(err)
	}
	if string(loaded.CAKey) != string(old.CAKey) || string(loaded.CACert) != string(old.CACert) {
		t.Fatal("server generation changed committed CA material")
	}
	if string(loaded.ServerKey) == string(old.ServerKey) || string(loaded.ServerCert) == string(old.ServerCert) {
		t.Fatal("server generation reused old leaf material")
	}
	dir := filepath.Join(filepath.Dir(store.ControlServerRotationJournalPath()), "control", "server", journal.NewServerGeneration)
	assertControlRotationMode(t, dir, 0700)
	assertControlRotationMode(t, filepath.Join(dir, controlpki.ServerKeyFile), 0600)
	assertControlRotationMode(t, filepath.Join(dir, controlpki.ServerCertFile), 0600)
	if err := store.WriteControlServerGeneration(journal, candidate, nil); err == nil {
		t.Fatal("existing immutable server generation was overwritten")
	}
}

func TestControlServerGenerationWriteAndRetirementFaults(t *testing.T) {
	store, journal, old, pin := controlServerRotationFixture(t)
	endpoint, _ := controlpki.NormalizeEndpoint("127.0.0.1", "control.example.com", 8443, 51821)
	candidate, err := controlpki.RotateServerLeaf(old, endpoint, pin, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveControlServerRotationJournal(journal, func(step string) error {
		if step == "before-sync:journal" {
			return errors.New("injected journal sync failure")
		}
		return nil
	}); err == nil {
		t.Fatal("journal sync fault was ignored")
	}
	if err := store.WriteControlServerGeneration(journal, candidate, func(step string) error {
		if step == "before-write:"+controlpki.ServerCertFile {
			return errors.New("injected cert write failure")
		}
		return nil
	}); err == nil {
		t.Fatal("generation write fault was ignored")
	}
	newDir := filepath.Join(filepath.Dir(store.ControlServerRotationJournalPath()), "control", "server", journal.NewServerGeneration)
	if _, err := os.Lstat(newDir); err != nil {
		t.Fatalf("partial generation disappeared: %v", err)
	}
	if err := os.Remove(filepath.Join(newDir, controlpki.ServerKeyFile)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(newDir); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteControlServerGeneration(journal, candidate, nil); err != nil {
		t.Fatal(err)
	}
	oldDir := filepath.Join(filepath.Dir(store.ControlServerRotationJournalPath()), "control", "server", journal.OldServerGeneration)
	if err := os.WriteFile(filepath.Join(oldDir, "unexpected"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveControlServerGeneration(journal, journal.OldServerGeneration, nil); err == nil {
		t.Fatal("retirement accepted an unexpected entry")
	}
	if err := os.Remove(filepath.Join(oldDir, "unexpected")); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveControlServerGeneration(journal, journal.OldServerGeneration, func(step string) error {
		if step == "after-retire:"+controlpki.ServerKeyFile {
			return errors.New("injected partial retirement")
		}
		return nil
	}); err == nil {
		t.Fatal("partial retirement fault was ignored")
	}
	if _, err := os.Lstat(oldDir); err != nil {
		t.Fatalf("partial retirement removed directory: %v", err)
	}
	if err := store.RemoveControlServerGeneration(journal, journal.OldServerGeneration, nil); err != nil {
		t.Fatalf("retirement retry failed: %v", err)
	}
	if _, err := os.Lstat(oldDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retired generation remains: %v", err)
	}
}

func TestControlServerGenerationRejectsUnsafeTree(t *testing.T) {
	store, journal, _, _ := controlServerRotationFixture(t)
	server := filepath.Join(filepath.Dir(store.ControlServerRotationJournalPath()), "control", "server")
	if err := os.Chmod(server, 0755); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckControlServerGeneration(journal.NewServerGeneration); err == nil {
		t.Fatal("non-private server parent was accepted")
	}
	if err := os.Chmod(server, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(server, server+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), server); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckControlServerGeneration(journal.NewServerGeneration); err == nil {
		t.Fatal("symlinked server parent was accepted")
	}
}

func controlServerRotationFixture(t *testing.T) (Store, ControlServerRotationJournal, controlpki.Material, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	store := New(root)
	endpoint, _ := controlpki.NormalizeEndpoint("127.0.0.1", "control.example.com", 8443, 51821)
	material, pin, err := controlpki.Generate(endpoint, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	identity := ControlIdentityJournal{ControllerID: "11111111-1111-4111-8111-111111111111", CAGeneration: strings.Repeat("a", 32), ServerGeneration: strings.Repeat("b", 32), StartedAt: time.Now().UTC()}
	if err := store.SaveControlIdentityJournal(identity); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteControlIdentity(identity, material, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteControlIdentityJournal(); err != nil {
		t.Fatal(err)
	}
	return store, ControlServerRotationJournal{Version: 1, ControllerID: identity.ControllerID, CAGeneration: identity.CAGeneration, OldServerGeneration: identity.ServerGeneration, NewServerGeneration: strings.Repeat("c", 32), StartedAt: time.Now().UTC()}, material, pin
}

func assertControlRotationMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != want {
		t.Fatalf("mode %s = %o, want %o", path, info.Mode().Perm(), want)
	}
}

func TestRotationJournalRejectsCaseAliases(t *testing.T) {
	store, j, _, _ := controlServerRotationFixture(t)
	if err := store.SaveControlServerRotationJournal(j, nil); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(store.ControlServerRotationJournalPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, alias := range []string{`"Version":1,`, `"VERSION":1,`, `"OLD_SERVER_GENERATION":"dddddddddddddddddddddddddddddddd",`, `"CA_GENERATION":"dddddddddddddddddddddddddddddddd",`} {
		body := append([]byte("{"+alias), original[1:]...)
		if err := os.WriteFile(store.ControlServerRotationJournalPath(), body, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.LoadControlServerRotationJournal(); err == nil {
			t.Fatalf("alias accepted: %s", alias)
		}
	}
}
