package storage

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
)

func TestDeleteRenderedTunnelRejectsUnsafeInterfaceName(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(outside, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(outside, "marker")
	if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}

	store := New(filepath.Join(root, "data"))
	err := store.DeleteRenderedTunnel("../outside")
	if err == nil {
		t.Fatal("expected unsafe interface name to be rejected")
	}
	if !strings.Contains(err.Error(), "invalid tunnel path component") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("outside marker was touched: %v", err)
	}
}

func TestWriteRenderedTunnelRejectsUnsafeClientID(t *testing.T) {
	store := New(t.TempDir())
	err := store.WriteRenderedTunnel(config.Tunnel{InterfaceName: "awg0"}, "server", map[string]string{
		"../client": "client",
	})
	if err == nil {
		t.Fatal("expected unsafe client id to be rejected")
	}
	if !strings.Contains(err.Error(), "invalid client path component") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSaveWritesLoadableStateWithPrivatePermissions(t *testing.T) {
	store := New(t.TempDir())
	state := config.State{SchemaVersion: 2, ServerHost: "vpn.example.com"}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(store.StatePath())
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("state permissions = %v, want 0600", got)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ServerHost != state.ServerHost {
		t.Fatalf("loaded server host = %q, want %q", loaded.ServerHost, state.ServerHost)
	}
	tmpMatches, err := filepath.Glob(filepath.Join(filepath.Dir(store.StatePath()), ".state-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(tmpMatches) != 0 {
		t.Fatalf("temporary state files left behind: %v", tmpMatches)
	}
}

func TestPendingDesiredStateCommitRoundTripUsesPrivatePermissions(t *testing.T) {
	store := New(t.TempDir())
	pending := PendingDesiredStateCommit{
		OperationID:         "44444444-4444-4444-8444-444444444444",
		IdempotencyKeyHash:  strings.Repeat("a", 64),
		StateEpoch:          "33333333-3333-4333-8333-333333333333",
		PreviousGeneration:  7,
		CandidateGeneration: 8,
		CandidateRuntimeTunnels: []PendingRuntimeTunnel{{
			ID:            "tunnel-id",
			Name:          "awg-next",
			InterfaceName: "awg-next",
			EgressMode:    config.EgressWAN,
			ListenPort:    51825,
			IPv4Subnet:    "10.25.0.0/24",
		}},
		CreatedAt: time.Date(2026, time.September, 8, 8, 0, 0, 0, time.UTC),
	}

	if err := store.SavePendingDesiredStateCommit(pending); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(store.PendingDesiredStateCommitPath())
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("journal permissions = %v, want 0600", got)
	}
	loaded, err := store.LoadPendingDesiredStateCommit()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, pending) {
		t.Fatalf("loaded journal = %#v, want %#v", loaded, pending)
	}
	tmpMatches, err := filepath.Glob(filepath.Join(filepath.Dir(store.PendingDesiredStateCommitPath()), ".desired-state-commit-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(tmpMatches) != 0 {
		t.Fatalf("temporary journal files left behind: %v", tmpMatches)
	}
	if err := store.DeletePendingDesiredStateCommit(); err != nil {
		t.Fatal(err)
	}
	if err := store.DeletePendingDesiredStateCommit(); err != nil {
		t.Fatalf("second journal deletion is not idempotent: %v", err)
	}
	if _, err := store.LoadPendingDesiredStateCommit(); !os.IsNotExist(err) {
		t.Fatalf("journal remains after deletion: %v", err)
	}
}

func TestControllerActivationJournalRoundTripUsesPrivatePermissions(t *testing.T) {
	store := New(t.TempDir())
	journal := ControllerActivationJournal{
		ControllerID: "11111111-1111-4111-8111-111111111111",
		StartedAt:    time.Date(2026, time.September, 23, 8, 0, 0, 0, time.UTC),
	}
	if err := store.SaveControllerActivationJournal(journal); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(store.ControllerActivationJournalPath())
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Fatalf("activation journal mode = %v, want regular 0600", info.Mode())
	}
	loaded, err := store.LoadControllerActivationJournal()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, journal) {
		t.Fatalf("loaded activation journal = %#v, want %#v", loaded, journal)
	}
	if err := store.DeleteControllerActivationJournal(); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteControllerActivationJournal(); err != nil {
		t.Fatalf("second activation journal deletion is not idempotent: %v", err)
	}
}

func TestLoadControllerActivationJournalRejectsUnsafeFiles(t *testing.T) {
	store := New(t.TempDir())
	path := store.ControllerActivationJournalPath()
	if err := os.WriteFile(path, []byte(`{"controller_id":"11111111-1111-4111-8111-111111111111","started_at":"2026-09-23T08:00:00Z"} {}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadControllerActivationJournal(); err == nil {
		t.Fatal("activation journal with trailing JSON was accepted")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadControllerActivationJournal(); err == nil {
		t.Fatal("world-readable activation journal was accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(filepath.Dir(path), "journal-target")
	if err := os.WriteFile(target, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadControllerActivationJournal(); err == nil {
		t.Fatal("symlink activation journal was accepted")
	}
}
