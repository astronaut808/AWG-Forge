package backup

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlpki"
	"github.com/astronaut808/awg-forge/internal/storage"
)

func TestRotatedControlIdentityBackupVerifyAndColdRestore(t *testing.T) {
	cfg, svc, rotated, retired := rotatedControllerBackupFixture(t)
	archive, err := Create(context.Background(), cfg, svc, testPassword, Options{})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := decrypt(archive.Data, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	files, _, state, err := readPlainZip(plain)
	if err != nil {
		t.Fatal(err)
	}
	if state.Controller == nil || state.Controller.Control == nil || state.Controller.Control.ServerGeneration != rotated.ServerGeneration {
		t.Fatal("archive does not select the rotated server generation")
	}
	controlFiles := 0
	for _, file := range files {
		if strings.HasPrefix(file.Path, "control/") {
			controlFiles++
			if strings.Contains(file.Path, retired) {
				t.Fatal("archive contains retired server generation")
			}
		}
	}
	if controlFiles != 4 {
		t.Fatalf("control archive file count = %d, want 4", controlFiles)
	}
	path := writeTempArchive(t, archive.Data)
	if _, err := Verify(context.Background(), cfg, testPassword, path); err != nil {
		t.Fatalf("verify rotated backup: %v", err)
	}
	if err := Restore(context.Background(), cfg, testPassword, path); err != nil {
		t.Fatalf("cold restore rotated backup: %v", err)
	}
	restored, err := storage.New(cfg.ConfigDir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if restored.Controller == nil || restored.Controller.Control == nil || restored.Controller.Control.ServerGeneration != rotated.ServerGeneration {
		t.Fatal("cold restore changed active rotated generation")
	}
	if _, err := storage.New(cfg.ConfigDir).LoadControlIdentity(rotated.CAGeneration, rotated.ServerGeneration); err != nil {
		t.Fatal(err)
	}
}

func TestControlBackupAcceptsLegacyAndExpiredConsistentIdentity(t *testing.T) {
	cfg, _ := controllerBackupFixture(t, false)
	svc := app.New(cfg)
	if _, err := svc.PrepareControlIdentity(context.Background(), app.ControlIdentityRequest{BindIP: "127.0.0.1", Advertised: "control.example.com", Port: 8443}); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(context.Background(), cfg, svc, testPassword, Options{}); err != nil {
		t.Fatalf("legacy unrotated identity rejected: %v", err)
	}
	expiredCfg, _ := controllerBackupFixture(t, false)
	expiredSvc := app.New(expiredCfg)
	if _, err := expiredSvc.PrepareControlIdentity(context.Background(), app.ControlIdentityRequest{BindIP: "127.0.0.1", Advertised: "control.example.com", Port: 8443, Now: time.Now().UTC().Add(-31 * 24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	archive, err := Create(context.Background(), expiredCfg, expiredSvc, testPassword, Options{})
	if err != nil {
		t.Fatalf("expired consistent identity rejected: %v", err)
	}
	if err := Restore(context.Background(), expiredCfg, testPassword, writeTempArchive(t, archive.Data)); err != nil {
		t.Fatalf("expired consistent identity did not restore: %v", err)
	}
}

func TestControlRotationJournalRejectsBackupBeforeInit(t *testing.T) {
	for name, body := range map[string][]byte{
		"valid":     []byte(`{"version":1,"controller_id":"11111111-1111-4111-8111-111111111111","ca_generation":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","old_server_generation":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","new_server_generation":"cccccccccccccccccccccccccccccccc","started_at":"2026-09-30T12:00:00Z"}`),
		"malformed": []byte(`not json`),
	} {
		t.Run(name, func(t *testing.T) {
			cfg, _ := controllerBackupFixture(t, false)
			path := storage.New(cfg.ConfigDir).ControlServerRotationJournalPath()
			if err := os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Create(context.Background(), cfg, app.New(cfg), testPassword, Options{}); err == nil || !strings.Contains(err.Error(), "rotation requires recovery") {
				t.Fatalf("backup journal error = %v", err)
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatalf("backup changed rotation journal: %v", err)
			}
		})
	}
}

func TestControlRotationJournalDisallowedInArchiveAndColdRestore(t *testing.T) {
	cfg, svc, _, _ := rotatedControllerBackupFixture(t)
	archive, err := Create(context.Background(), cfg, svc, testPassword, Options{})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := decrypt(archive.Data, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	files, metadata, _, err := readPlainZip(plain)
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{
		"valid":     []byte(`{"version":1,"controller_id":"11111111-1111-4111-8111-111111111111","ca_generation":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","old_server_generation":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","new_server_generation":"cccccccccccccccccccccccccccccccc","started_at":"2026-09-30T12:00:00Z"}`),
		"malformed": []byte(`not json`),
	} {
		t.Run(name, func(t *testing.T) {
			mutated := append([]restoreFile(nil), files...)
			mutated = append(mutated, restoreFile{Path: storage.ControlServerRotationJournalFileName, Data: body})
			path := writeTempArchive(t, rebuildControlArchive(t, metadata, mutated))
			if _, err := Verify(context.Background(), cfg, testPassword, path); err == nil {
				t.Fatal("archive rotation journal verified")
			}
			if err := Restore(context.Background(), cfg, testPassword, path); err == nil {
				t.Fatal("cold restore accepted archive rotation journal")
			}
		})
	}
}

func rotatedControllerBackupFixture(t *testing.T) (config.Config, *app.Service, config.ControlIdentityState, string) {
	t.Helper()
	cfg, _ := controllerBackupFixture(t, false)
	svc := app.New(cfg)
	prepared, err := svc.PrepareControlIdentity(context.Background(), app.ControlIdentityRequest{BindIP: "127.0.0.1", Advertised: "control.example.com", Port: 8443})
	if err != nil {
		t.Fatal(err)
	}
	store := storage.New(cfg.ConfigDir)
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	old, err := store.LoadControlIdentity(prepared.CAGeneration, prepared.ServerGeneration)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := controlpki.NormalizeEndpoint(prepared.BindIP, prepared.Advertised, prepared.Port, cfg.WebUIPort)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := controlpki.RotateServerLeaf(old, endpoint, prepared.CAPin, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	journal := storage.ControlServerRotationJournal{
		Version: 1, ControllerID: state.Controller.ControllerID, CAGeneration: prepared.CAGeneration,
		OldServerGeneration: prepared.ServerGeneration, NewServerGeneration: strings.Repeat("c", 32), StartedAt: time.Now().UTC(),
	}
	if err := store.SaveControlServerRotationJournal(journal, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteControlServerGeneration(journal, candidate, nil); err != nil {
		t.Fatal(err)
	}
	state.Controller.Control.ServerGeneration = journal.NewServerGeneration
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	if err := store.SyncStateDirectory(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadControlIdentity(journal.CAGeneration, journal.NewServerGeneration); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveControlServerGeneration(journal, journal.OldServerGeneration, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteControlServerRotationJournal(nil); err != nil {
		t.Fatal(err)
	}
	return cfg, svc, *state.Controller.Control, journal.OldServerGeneration
}

func TestColdRestoreRejectsPendingTargetRotation(t *testing.T) {
	for _, body := range []string{"not json", `{"version":1,"controller_id":"11111111-1111-4111-8111-111111111111","ca_generation":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","old_server_generation":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","new_server_generation":"cccccccccccccccccccccccccccccccc","started_at":"2026-09-30T12:00:00Z"}`} {
		cfg, svc, _, _ := rotatedControllerBackupFixture(t)
		archive, err := Create(context.Background(), cfg, svc, testPassword, Options{})
		if err != nil {
			t.Fatal(err)
		}
		path := writeTempArchive(t, archive.Data)
		store := storage.New(cfg.ConfigDir)
		before, err := os.ReadFile(store.StatePath())
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(store.ControlServerRotationJournalPath(), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if err := Restore(context.Background(), cfg, testPassword, path); err == nil || !strings.Contains(err.Error(), "rotation requires recovery") {
			t.Fatalf("target rotation fence: %v", err)
		}
		after, err := os.ReadFile(store.StatePath())
		if err != nil || string(before) != string(after) {
			t.Fatal("rejected restore modified state", err)
		}
		retained, err := os.ReadFile(store.ControlServerRotationJournalPath())
		if err != nil || string(retained) != body {
			t.Fatal("restore removed or modified journal", err)
		}
		if err := store.CheckRestorePending(); err != nil {
			t.Fatal("rotation preflight created restore marker", err)
		}
	}
}
