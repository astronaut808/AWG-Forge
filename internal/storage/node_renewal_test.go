package storage

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func renewalJournalFixture() NodeRenewalJournal {
	return NodeRenewalJournal{
		PredecessorGeneration: "0123456789abcdef0123456789abcdef",
		Generation:            "fedcba9876543210fedcba9876543210",
		ControllerID:          "d71b3655-20d5-41c9-b25b-dbca85de26b7",
		NodeID:                "9526d3da-f289-4c8e-9862-ae9de3c13a70",
		BindingEpoch:          1,
		StateEpoch:            "a06d7eba-7ca9-4ca4-8d98-f581e98c6fda",
		ControllerURL:         "https://127.0.0.1:9443",
		CAPin:                 "public-ca-pin",
	}
}

func privateRenewalStore(t *testing.T) Store {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return New(dir)
}

func TestNodeRenewalJournalAtomicPublication(t *testing.T) {
	for _, step := range []string{"created", "written", "synced", "published"} {
		t.Run(step, func(t *testing.T) {
			store := privateRenewalStore(t)
			journal := renewalJournalFixture()
			fault := errors.New("publication interrupted")
			err := store.saveNodeRenewalJournal(journal, func(current string) error {
				if current == step {
					return fault
				}
				return nil
			})
			if !errors.Is(err, fault) {
				t.Fatalf("publication error = %v", err)
			}
			loaded, err := store.LoadNodeRenewalJournal()
			if step == "published" {
				if err != nil || loaded != journal {
					t.Fatal("published journal was incomplete")
				}
			} else {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("incomplete journal became visible: %v", err)
				}
				// An actual process crash can leave an empty or partial private
				// temporary file. It must not fence the next complete attempt.
				if err := os.WriteFile(filepath.Join(store.dir, ".node-renewal-journal-orphan"), []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := store.SaveNodeRenewalJournal(journal); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(store.NodeRenewalJournalPath())
			if err != nil {
				t.Fatal(err)
			}
			other := journal
			other.BindingEpoch++
			if err := store.SaveNodeRenewalJournal(other); err == nil {
				t.Fatal("existing journal was overwritten")
			}
			after, err := os.ReadFile(store.NodeRenewalJournalPath())
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("refused overwrite changed the journal")
			}
		})
	}
}

func TestNodeRenewalPrivateCandidateAndJournal(t *testing.T) {
	store := privateRenewalStore(t)
	j := renewalJournalFixture()
	candidate := NodeRenewalCandidate{PrivateKey: []byte("private fixture"), CSR: []byte("signed request fixture")}
	if err := store.SaveNodeRenewalCandidate(j.Generation, candidate); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveNodeRenewalCandidate(j.Generation, candidate); err == nil {
		t.Fatal("candidate generation was reused")
	}
	loaded, err := store.LoadNodeRenewalCandidate(j.Generation)
	if err != nil || !bytes.Equal(loaded.PrivateKey, candidate.PrivateKey) || !bytes.Equal(loaded.CSR, candidate.CSR) {
		t.Fatal("candidate did not survive reload")
	}
	keyPath := filepath.Join(store.dir, "node-renewal", j.Generation, "key.pem")
	if err := os.Chmod(keyPath, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadNodeRenewalCandidate(j.Generation); err == nil {
		t.Fatal("public candidate key was accepted")
	}
	if _, err := store.LoadNodeRenewalCandidate("../other"); err == nil {
		t.Fatal("unsafe candidate path was accepted")
	}
	if err := store.SaveNodeRenewalJournal(j); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store.NodeRenewalJournalPath(), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadNodeRenewalJournal(); err == nil {
		t.Fatal("public journal was accepted")
	}
	if err := os.Remove(store.NodeRenewalJournalPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(keyPath, store.NodeRenewalJournalPath()); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveNodeRenewalJournal(j); err == nil {
		t.Fatal("journal symlink was overwritten")
	}
	if _, err := store.LoadNodeRenewalJournal(); err == nil {
		t.Fatal("journal symlink was followed")
	}
}
