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

func TestControlIdentityStoreRejectsUnsafePaths(t *testing.T) {
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	store := New(root)
	caGeneration := strings.Repeat("a", 32)
	serverGeneration := strings.Repeat("b", 32)
	endpoint, _ := controlpki.NormalizeEndpoint("127.0.0.1", "control.example.com", 8443, 51821)
	material, _, err := controlpki.Generate(endpoint, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "control")); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckControlIdentityPath(caGeneration, serverGeneration); err == nil {
		t.Fatal("symlinked control directory was accepted")
	}
	if err := os.Remove(filepath.Join(root, "control")); err != nil {
		t.Fatal(err)
	}
	journal := ControlIdentityJournal{ControllerID: "11111111-1111-4111-8111-111111111111", CAGeneration: caGeneration, ServerGeneration: serverGeneration, StartedAt: time.Now().UTC()}
	if err := store.SaveControlIdentityJournal(journal); err != nil {
		t.Fatal(err)
	}
	if loaded, err := store.LoadControlIdentityJournal(); err != nil || loaded.CAGeneration != caGeneration {
		t.Fatalf("load journal = %+v, %v", loaded, err)
	}
	if err := store.WriteControlIdentity(journal, material, nil); err != nil {
		t.Fatal(err)
	}
	paths, _ := ControlIdentityRelativePaths(caGeneration, serverGeneration)
	keyPath := filepath.Join(root, filepath.FromSlash(paths[0]))
	if err := os.Chmod(keyPath, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadControlIdentity(caGeneration, serverGeneration); err == nil {
		t.Fatal("overly permissive control key was accepted")
	}
	if err := os.Chmod(keyPath, 0600); err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(root, filepath.FromSlash(paths[1]))
	if err := os.Remove(certPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "state.json"), certPath); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadControlIdentity(caGeneration, serverGeneration); err == nil {
		t.Fatal("symlinked control certificate was accepted")
	}
	if err := store.RemoveJournalOwnedControlIdentity(journal); err == nil {
		t.Fatal("uncertain symlinked generation was deleted")
	}
	if _, err := os.Lstat(store.ControlIdentityJournalPath()); err != nil {
		t.Fatalf("recovery journal was lost: %v", err)
	}
	if err := os.Remove(certPath); err != nil {
		t.Fatal(err)
	}
	caParent := filepath.Join(root, "control", "ca")
	if err := store.removeJournalOwnedControlIdentity(journal, func(path string) error {
		if path == caParent {
			return errors.New("injected parent sync failure after generation unlink")
		}
		return syncRestoreDirectory(path)
	}); err == nil {
		t.Fatal("parent sync failure was ignored")
	}
	if _, err := os.Lstat(filepath.Join(caParent, caGeneration)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("CA generation was not unlinked before injected sync failure: %v", err)
	}
	resyncedAbsentCA := false
	if err := store.removeJournalOwnedControlIdentity(journal, func(path string) error {
		if path == caParent {
			resyncedAbsentCA = true
		}
		return syncRestoreDirectory(path)
	}); err != nil {
		t.Fatal(err)
	}
	if !resyncedAbsentCA {
		t.Fatal("retry did not sync parent of already unlinked CA generation")
	}
	if err := store.DeleteControlIdentityJournal(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(store.ControlIdentityJournalPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal remains after cleanup: %v", err)
	}
}

func TestControlIdentityStoreRejectsSymlinkedConfigAncestor(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	realConfig := filepath.Join(parent, "real", "config")
	if err := os.MkdirAll(realConfig, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(parent, "real"), filepath.Join(parent, "link")); err != nil {
		t.Fatal(err)
	}
	store := New(filepath.Join(parent, "link", "config"))
	if err := store.CheckControlIdentityPath(strings.Repeat("a", 32), strings.Repeat("b", 32)); err == nil {
		t.Fatal("symlinked CONFIG_DIR ancestor was accepted")
	}
}
