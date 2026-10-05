package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/storage"
)

func TestPreparedControlIdentityBackupVerifyAndColdRestore(t *testing.T) {
	cfg, _ := controllerBackupFixture(t, false)
	svc := app.New(cfg)
	prepared, err := svc.PrepareControlIdentity(context.Background(), app.ControlIdentityRequest{BindIP: "127.0.0.1", Advertised: "control.example.com", Port: 8443})
	if err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(cfg.ConfigDir, "control", "ca", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err := os.Mkdir(orphan, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphan, "key.pem"), []byte("orphan-private-key"), 0600); err != nil {
		t.Fatal(err)
	}
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
	if state.Controller == nil || state.Controller.Control == nil || state.Controller.Control.Enabled || state.Controller.Control.CAPin != prepared.CAPin {
		t.Fatal("archive does not contain the disabled committed control identity")
	}
	paths, _ := storage.ControlIdentityRelativePaths(prepared.CAGeneration, prepared.ServerGeneration)
	count := 0
	for _, file := range files {
		if strings.HasPrefix(file.Path, "control/") {
			count++
		}
	}
	if count != len(paths) {
		t.Fatalf("control archive file count = %d, want %d", count, len(paths))
	}
	for _, file := range files {
		if strings.Contains(file.Path, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") {
			t.Fatal("unreferenced control generation entered backup")
		}
	}
	path := writeTempArchive(t, archive.Data)
	if _, err := Verify(context.Background(), cfg, testPassword, path); err != nil {
		t.Fatalf("verify prepared backup: %v", err)
	}
	if err := Restore(context.Background(), cfg, testPassword, path); err != nil {
		t.Fatalf("cold restore prepared backup: %v", err)
	}
	if err := storage.New(cfg.ConfigDir).CheckRestorePending(); err != nil {
		t.Fatal(err)
	}
	restored, err := storage.New(cfg.ConfigDir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if restored.Controller.Control == nil || *restored.Controller.Control != prepared {
		t.Fatal("cold restore changed prepared control identity")
	}
	if _, err := storage.New(cfg.ConfigDir).LoadControlIdentity(prepared.CAGeneration, prepared.ServerGeneration); err != nil {
		t.Fatal(err)
	}
}

func TestControllerBackupRejectsPendingControlIdentityJournal(t *testing.T) {
	cfg, _ := controllerBackupFixture(t, false)
	store := storage.New(cfg.ConfigDir)
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	journal := storage.ControlIdentityJournal{
		ControllerID: state.Controller.ControllerID, CAGeneration: strings.Repeat("a", 32),
		ServerGeneration: strings.Repeat("b", 32), StartedAt: time.Now().UTC(),
	}
	if err := store.SaveControlIdentityJournal(journal); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(context.Background(), cfg, app.New(cfg), testPassword, Options{}); err == nil || !strings.Contains(err.Error(), "preparation is pending") {
		t.Fatalf("backup with pending preparation error = %v", err)
	}
	if _, err := store.LoadControlIdentityJournal(); err != nil {
		t.Fatalf("backup changed preparation journal: %v", err)
	}
}

func TestPreparedControlIdentityBackupRejectsUnsafeSourceAndArchive(t *testing.T) {
	cfg, _ := controllerBackupFixture(t, false)
	svc := app.New(cfg)
	prepared, err := svc.PrepareControlIdentity(context.Background(), app.ControlIdentityRequest{BindIP: "127.0.0.1", Advertised: "control.example.com", Port: 8443})
	if err != nil {
		t.Fatal(err)
	}
	archive, err := Create(context.Background(), cfg, svc, testPassword, Options{})
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := decrypt(archive.Data, testPassword)
	files, metadata, _, err := readPlainZip(plain)
	if err != nil {
		t.Fatal(err)
	}
	paths, _ := storage.ControlIdentityRelativePaths(prepared.CAGeneration, prepared.ServerGeneration)
	for _, tt := range []struct {
		name string
		edit func([]restoreFile) []restoreFile
	}{
		{"missing", func(in []restoreFile) []restoreFile {
			out := make([]restoreFile, 0, len(in)-1)
			for _, file := range in {
				if file.Path != paths[0] {
					out = append(out, file)
				}
			}
			return out
		}},
		{"corrupt key", func(in []restoreFile) []restoreFile {
			for i := range in {
				if in[i].Path == paths[0] {
					in[i].Data = []byte("corrupt")
				}
			}
			return in
		}},
		{"extra control file", func(in []restoreFile) []restoreFile {
			return append(in, restoreFile{Path: "control/extra.pem", Data: []byte("extra")})
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mutated := tt.edit(append([]restoreFile(nil), files...))
			path := writeTempArchive(t, rebuildControlArchive(t, metadata, mutated))
			if _, err := Verify(context.Background(), cfg, testPassword, path); err == nil {
				t.Fatal("unsafe control archive verified")
			}
		})
	}
	keyPath := filepath.Join(cfg.ConfigDir, filepath.FromSlash(paths[0]))
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(cfg.ConfigDir, "state.json"), keyPath); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(context.Background(), cfg, svc, testPassword, Options{}); err == nil {
		t.Fatal("symlinked control key was included in backup")
	}
}

func TestExpiredPreparedControlIdentityRemainsRecoverable(t *testing.T) {
	cfg, _ := controllerBackupFixture(t, false)
	svc := app.New(cfg)
	if _, err := svc.PrepareControlIdentity(context.Background(), app.ControlIdentityRequest{BindIP: "127.0.0.1", Advertised: "control.example.com", Port: 8443, Now: time.Now().UTC().Add(-31 * 24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	archive, err := Create(context.Background(), cfg, svc, testPassword, Options{})
	if err != nil {
		t.Fatalf("expired but consistent identity blocked backup: %v", err)
	}
	path := writeTempArchive(t, archive.Data)
	if _, err := Verify(context.Background(), cfg, testPassword, path); err != nil {
		t.Fatal(err)
	}
	if err := Restore(context.Background(), cfg, testPassword, path); err != nil {
		t.Fatal(err)
	}
	if _, err := app.New(cfg).Init(); err != nil {
		t.Fatalf("expired optional identity blocked local runtime: %v", err)
	}
}

func TestControlArchiveRejectsNonPrivateEntryMode(t *testing.T) {
	var plain bytes.Buffer
	writer := zip.NewWriter(&plain)
	header := &zip.FileHeader{Name: "control/ca/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/key.pem", Method: zip.Deflate}
	header.SetMode(0644)
	file, err := writer.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("not-private")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := readPlainZip(plain.Bytes()); err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatalf("unsafe control file mode was accepted: %v", err)
	}
}

func TestControllerRestoreRejectsTargetWebPortConflictBeforeMarker(t *testing.T) {
	cfg, _ := controllerBackupFixture(t, false)
	svc := app.New(cfg)
	prepared, err := svc.PrepareControlIdentity(context.Background(), app.ControlIdentityRequest{BindIP: "127.0.0.1", Advertised: "control.example.com", Port: 8443})
	if err != nil {
		t.Fatal(err)
	}
	archive, err := Create(context.Background(), cfg, svc, testPassword, Options{})
	if err != nil {
		t.Fatal(err)
	}
	path := writeTempArchive(t, archive.Data)
	if _, err := Verify(context.Background(), cfg, testPassword, path); err != nil {
		t.Fatal(err)
	}
	target := cfg
	target.WebUIPort = prepared.Port
	if err := Restore(context.Background(), target, testPassword, path); err == nil {
		t.Fatal("target Web UI port conflict was accepted")
	}
	if err := storage.New(cfg.ConfigDir).CheckRestorePending(); err != nil {
		t.Fatalf("preflight conflict left restore marker: %v", err)
	}
	state, err := storage.New(cfg.ConfigDir).Load()
	if err != nil || state.Controller.Control == nil || state.Controller.Control.CAGeneration != prepared.CAGeneration {
		t.Fatal("preflight conflict changed controller identity")
	}
}

func TestControllerRestoreRejectsCorruptCurrentIdentityBeforeMarker(t *testing.T) {
	cfg, _ := controllerBackupFixture(t, false)
	svc := app.New(cfg)
	prepared, err := svc.PrepareControlIdentity(context.Background(), app.ControlIdentityRequest{BindIP: "127.0.0.1", Advertised: "control.example.com", Port: 8443})
	if err != nil {
		t.Fatal(err)
	}
	archive, err := Create(context.Background(), cfg, svc, testPassword, Options{})
	if err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(cfg.ConfigDir, "control", "server", prepared.ServerGeneration, "cert.pem")
	if err := os.WriteFile(certPath, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Restore(context.Background(), cfg, testPassword, writeTempArchive(t, archive.Data)); err == nil {
		t.Fatal("restore discarded corrupt current identity without a verified pre-restore backup")
	}
	if err := storage.New(cfg.ConfigDir).CheckRestorePending(); err != nil {
		t.Fatalf("preflight failure left restore marker: %v", err)
	}
	if _, err := app.New(cfg).Init(); err != nil {
		t.Fatalf("optional control corruption blocked local runtime: %v", err)
	}
}

func rebuildControlArchive(t *testing.T, metadata Metadata, files []restoreFile) []byte {
	t.Helper()
	var plain bytes.Buffer
	writer := zip.NewWriter(&plain)
	metadata.Files = nil
	for _, file := range files {
		if err := addBytes(writer, file.Path, file.Data); err != nil {
			t.Fatal(err)
		}
		metadata.Files = append(metadata.Files, testFileMeta(file.Path, file.Data))
	}
	if err := addJSON(writer, "metadata.json", metadata); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	encrypted, err := encrypt(plain.Bytes(), testPassword, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return encrypted
}
