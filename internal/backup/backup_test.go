package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlauth"
	"github.com/astronaut808/awg-forge/internal/sqldb"
	"github.com/astronaut808/awg-forge/internal/storage"
	"github.com/astronaut808/awg-forge/internal/webtls"
	"github.com/pquerna/otp/totp"
)

const testPassword = "correct horse battery staple"

func TestBackupRestoreRoundTripEncrypted(t *testing.T) {
	cfg := testConfig(t)
	svc := app.New(cfg)
	client, err := svc.AddClient("phone")
	if err != nil {
		t.Fatal(err)
	}
	archive, err := Create(context.Background(), cfg, svc, testPassword, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(archive.Data), client.PrivateKey) || strings.Contains(string(archive.Data), client.PresharedKey) {
		t.Fatal("encrypted archive contains plaintext client secrets")
	}
	restoreCfg := cfg
	restoreCfg.ConfigDir = t.TempDir()
	if err := Restore(context.Background(), restoreCfg, testPassword, writeTempArchive(t, archive.Data)); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(filepath.Join(restoreCfg.ConfigDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(restored), client.PrivateKey) {
		t.Fatal("restore did not preserve client private key")
	}
	assertMode(t, restoreCfg.ConfigDir, 0700)
	assertMode(t, filepath.Join(restoreCfg.ConfigDir, "state.json"), 0600)
	assertMode(t, filepath.Join(restoreCfg.ConfigDir, storage.StateLockFileName), 0600)
	assertMode(t, filepath.Join(restoreCfg.ConfigDir, storage.StateMutationLockFileName), 0600)
}

func TestBackupWaitsForStateMutationTransaction(t *testing.T) {
	cfg := testConfig(t)
	svc := app.New(cfg)
	if _, err := svc.Init(); err != nil {
		t.Fatal(err)
	}
	lock, err := storage.AcquireStateMutationLock(cfg.ConfigDir)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := Create(context.Background(), cfg, svc, testPassword, Options{})
		done <- err
	}()
	select {
	case err := <-done:
		_ = lock.Close()
		t.Fatalf("backup completed during a state mutation transaction: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("backup did not resume after the state mutation transaction")
	}
}

func TestBackupIncludesTLSSettings(t *testing.T) {
	cfg := testConfig(t)
	cfg.Password = "secret"
	cfg.WebUITrustProxyHeaders = true
	cfg.WebUITrustedProxyCIDRs = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
	svc := app.New(cfg)
	if _, err := svc.Init(); err != nil {
		t.Fatal(err)
	}
	if err := webtls.Save(cfg, webtls.Settings{Mode: webtls.ModeReverseProxy}); err != nil {
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
	reader, err := zip.NewReader(bytes.NewReader(plain), int64(len(plain)))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range reader.File {
		if file.Name == webtls.SettingsRelativePath {
			restoreCfg := cfg
			restoreCfg.ConfigDir = t.TempDir()
			restoreCfg.Password = "secret"
			restoreCfg.WebUITrustProxyHeaders = true
			restoreCfg.WebUITrustedProxyCIDRs = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
			if err := Restore(context.Background(), restoreCfg, testPassword, writeTempArchive(t, archive.Data)); err != nil {
				t.Fatal(err)
			}
			runtime, err := webtls.Load(restoreCfg)
			if err != nil {
				t.Fatal(err)
			}
			status := runtime.Status
			if status.Mode != webtls.ModeReverseProxy {
				t.Fatalf("restored TLS mode = %q, want %q", status.Mode, webtls.ModeReverseProxy)
			}
			return
		}
	}
	t.Fatalf("backup does not contain %s", webtls.SettingsRelativePath)
}

func TestBackupRejectsTLSSymlink(t *testing.T) {
	cfg := testConfig(t)
	svc := app.New(cfg)
	if _, err := svc.Init(); err != nil {
		t.Fatal(err)
	}
	secretPath := filepath.Join(t.TempDir(), "unrelated-secret")
	if err := os.WriteFile(secretPath, []byte("must not be backed up"), 0600); err != nil {
		t.Fatal(err)
	}
	tlsDir := filepath.Join(cfg.ConfigDir, "tls")
	if err := os.MkdirAll(tlsDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secretPath, filepath.Join(tlsDir, "config.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(context.Background(), cfg, svc, testPassword, Options{}); err == nil {
		t.Fatal("expected backup to reject TLS settings symlink")
	}
}

func TestBackupIncludesACMECacheOnlyInEncryptedArchive(t *testing.T) {
	cfg := testConfig(t)
	svc := app.New(cfg)
	if _, err := svc.Init(); err != nil {
		t.Fatal(err)
	}
	cacheFile := filepath.Join(cfg.ConfigDir, filepath.FromSlash(webtls.ACMECacheRelativePath), "panel.example.com")
	if err := os.MkdirAll(filepath.Dir(cacheFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cacheFile, []byte("private ACME account material"), 0o600); err != nil {
		t.Fatal(err)
	}
	archive, err := Create(context.Background(), cfg, svc, testPassword, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(archive.Data, []byte("private ACME account material")) {
		t.Fatal("encrypted backup exposes ACME cache plaintext")
	}
	plain, err := decrypt(archive.Data, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(plain), int64(len(plain)))
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.ToSlash(filepath.Join(webtls.ACMECacheRelativePath, "panel.example.com"))
	for _, file := range reader.File {
		if file.Name == want {
			return
		}
	}
	t.Fatalf("backup does not contain %s", want)
}

func TestRestoreReplacesMountedDirectoryContents(t *testing.T) {
	cfg := testConfig(t)
	svc := app.New(cfg)
	if _, err := svc.AddClient("phone"); err != nil {
		t.Fatal(err)
	}
	archive, err := Create(context.Background(), cfg, svc, testPassword, Options{})
	if err != nil {
		t.Fatal(err)
	}

	restoreCfg := cfg
	restoreCfg.ConfigDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(restoreCfg.ConfigDir, "stale.txt"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(restoreCfg.ConfigDir, "stale-dir"), 0700); err != nil {
		t.Fatal(err)
	}

	if err := Restore(context.Background(), restoreCfg, testPassword, writeTempArchive(t, archive.Data)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(restoreCfg.ConfigDir, "state.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(restoreCfg.ConfigDir, "stale.txt")); !os.IsNotExist(err) {
		t.Fatalf("stale file should be removed, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(restoreCfg.ConfigDir, "stale-dir")); !os.IsNotExist(err) {
		t.Fatalf("stale dir should be removed, stat err = %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(restoreCfg.ConfigDir, ".restore-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("restore scratch dirs left behind: %v", matches)
	}
}

func TestRestoreRejectsWrongPassword(t *testing.T) {
	cfg := testConfig(t)
	svc := app.New(cfg)
	if _, err := svc.AddClient("phone"); err != nil {
		t.Fatal(err)
	}
	archive, err := Create(context.Background(), cfg, svc, testPassword, Options{})
	if err != nil {
		t.Fatal(err)
	}
	restoreCfg := cfg
	restoreCfg.ConfigDir = t.TempDir()
	err = Restore(context.Background(), restoreCfg, "wrong password", writeTempArchive(t, archive.Data))
	if err == nil || !strings.Contains(err.Error(), "decrypt failed") {
		t.Fatalf("restore error = %v, want decrypt failed", err)
	}
}

func TestVerifyReturnsReportWithoutWritingFiles(t *testing.T) {
	cfg := testConfig(t)
	svc := app.New(cfg)
	if _, err := svc.AddClient("phone"); err != nil {
		t.Fatal(err)
	}
	archive, err := Create(context.Background(), cfg, svc, testPassword, Options{})
	if err != nil {
		t.Fatal(err)
	}
	verifyCfg := cfg
	verifyCfg.ConfigDir = t.TempDir()
	report, err := Verify(context.Background(), verifyCfg, testPassword, writeTempArchive(t, archive.Data))
	if err != nil {
		t.Fatal(err)
	}
	if report.Format != formatVersion {
		t.Fatalf("format = %q, want %q", report.Format, formatVersion)
	}
	if report.SchemaVersion != config.CurrentStateSchemaVersion {
		t.Fatalf("schema = %d, want %d", report.SchemaVersion, config.CurrentStateSchemaVersion)
	}
	if report.ClientCount != 1 {
		t.Fatalf("clients = %d, want 1", report.ClientCount)
	}
	if len(report.Tunnels) != 1 {
		t.Fatalf("tunnels = %d, want 1", len(report.Tunnels))
	}
	if report.Tunnels[0].Name != "awg0" || report.Tunnels[0].Clients != 1 {
		t.Fatalf("tunnel report = %+v", report.Tunnels[0])
	}
	if _, err := os.Stat(filepath.Join(verifyCfg.ConfigDir, "state.json")); !os.IsNotExist(err) {
		t.Fatalf("verify wrote state.json or stat failed unexpectedly: %v", err)
	}
}

func TestVerifyRejectsWrongPassword(t *testing.T) {
	cfg := testConfig(t)
	svc := app.New(cfg)
	if _, err := svc.AddClient("phone"); err != nil {
		t.Fatal(err)
	}
	archive, err := Create(context.Background(), cfg, svc, testPassword, Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = Verify(context.Background(), cfg, "wrong password", writeTempArchive(t, archive.Data))
	if err == nil || !strings.Contains(err.Error(), "decrypt failed") {
		t.Fatalf("verify error = %v, want decrypt failed", err)
	}
}

func TestVerifyRejectsDuplicatedTunnelPorts(t *testing.T) {
	cfg := testConfig(t)
	svc := app.New(cfg)
	if _, err := svc.AddClient("phone"); err != nil {
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
	var mutatedStateJSON []byte
	mutated := mutatePlainZip(t, plain, func(name string, b []byte) []byte {
		switch name {
		case "state.json":
			var state config.State
			if err := json.Unmarshal(b, &state); err != nil {
				t.Fatal(err)
			}
			clone := state.Tunnels[0]
			clone.ID = "duplicate-port"
			clone.Name = "awg-copy"
			clone.InterfaceName = "awg-copy"
			clone.IPv4Subnet = "10.9.0.0/24"
			state.Tunnels = append(state.Tunnels, clone)
			out, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			mutatedStateJSON = out
			return out
		case "metadata.json":
			var metadata Metadata
			if err := json.Unmarshal(b, &metadata); err != nil {
				t.Fatal(err)
			}
			for i := range metadata.Files {
				if metadata.Files[i].Path == "state.json" {
					metadata.Files[i] = testFileMeta("state.json", mutatedStateJSON)
				}
			}
			return mustJSON(t, metadata)
		default:
			return b
		}
	})
	encrypted, err := encrypt(mutated, testPassword, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	_, err = Verify(context.Background(), cfg, testPassword, writeTempArchive(t, encrypted))
	if err == nil || !strings.Contains(err.Error(), "listen port 51820 is duplicated") {
		t.Fatalf("verify error = %v, want duplicate listen port", err)
	}
}

func TestRestoreKeepsEncryptedPreRestoreBackup(t *testing.T) {
	cfg := testConfig(t)
	svc := app.New(cfg)
	if _, err := svc.AddClient("old"); err != nil {
		t.Fatal(err)
	}
	sourceCfg := testConfig(t)
	sourceSvc := app.New(sourceCfg)
	if _, err := sourceSvc.AddClient("new"); err != nil {
		t.Fatal(err)
	}
	archive, err := Create(context.Background(), sourceCfg, sourceSvc, testPassword, Options{})
	if err != nil {
		t.Fatal(err)
	}
	backupDir := filepath.Join(cfg.ConfigDir, "backups")
	if err := os.Mkdir(backupDir, 0700); err != nil {
		t.Fatal(err)
	}
	previous := filepath.Join(backupDir, "previous.afbackup")
	if err := os.WriteFile(previous, []byte("earlier encrypted backup"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Restore(context.Background(), cfg, testPassword, writeTempArchive(t, archive.Data)); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(previous); err != nil || string(body) != "earlier encrypted backup" {
		t.Fatalf("previous backup was not preserved: %q, %v", body, err)
	}
	matches, err := filepath.Glob(filepath.Join(cfg.ConfigDir, "backups", "pre-restore-*.afbackup"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("pre-restore backups = %d, want 1", len(matches))
	}
	assertMode(t, matches[0], 0600)
	if _, err := Verify(context.Background(), cfg, testPassword, matches[0]); err != nil {
		t.Fatalf("saved pre-restore backup is unusable: %v", err)
	}
}

func TestCreateRejectsControllerStateWithoutAuthenticationData(t *testing.T) {
	cfg := testConfig(t)
	svc := app.New(cfg)
	state, err := svc.Init()
	if err != nil {
		t.Fatal(err)
	}
	state.Mode = config.ModeController
	state.Controller = &config.ControllerState{
		ControllerID: "11111111-1111-4111-8111-111111111111",
		ActivatedAt:  time.Date(2026, 9, 23, 8, 0, 0, 0, time.UTC),
	}
	if err := storage.New(cfg.ConfigDir).Save(state); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(context.Background(), cfg, svc, testPassword, Options{}); err == nil || !strings.Contains(err.Error(), "requires an absolute SQLite database path") {
		t.Fatalf("controller backup error = %v", err)
	}
}

func TestControllerBackupIncludesVerifiedKeysAndSQLiteSnapshot(t *testing.T) {
	cfg := testConfig(t)
	cfg.DatabaseMode = sqldb.ModeSQLite
	cfg.DatabasePath = filepath.Join(t.TempDir(), "controller.sqlite")
	cfg.DatabaseQueryTimeout = 5 * time.Second
	cfg.DatabaseBusyTimeout = 5 * time.Second
	cfg.DatabaseMaxOpenConns = 1
	cfg.DatabaseMaxIdleConns = 1
	svc := app.New(cfg)
	standaloneArchive, err := Create(context.Background(), cfg, svc, testPassword, Options{})
	if err != nil {
		t.Fatal(err)
	}
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("controller-backup-test-secret"))
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	code, err := totp.GenerateCode(secret, now)
	if err != nil {
		t.Fatal(err)
	}
	activated, err := svc.ActivateController(context.Background(), app.ControllerActivationRequest{
		Username: "admin", Password: testPassword, TOTPSecret: secret, TOTPConfirmation: code, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := Restore(context.Background(), cfg, testPassword, writeTempArchive(t, standaloneArchive.Data)); err == nil || !strings.Contains(err.Error(), "existing controller") {
		t.Fatalf("standalone restore into controller error = %v", err)
	}
	if _, err := svc.AddClient("controller-client"); err != nil {
		t.Fatal(err)
	}
	archive, err := Create(context.Background(), cfg, svc, testPassword, Options{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	archivePath := writeTempArchive(t, archive.Data)
	if _, err := Verify(context.Background(), cfg, testPassword, archivePath); err != nil {
		t.Fatalf("verify controller backup: %v", err)
	}
	if err := Restore(context.Background(), cfg, testPassword, archivePath); err != nil {
		t.Fatalf("restore controller backup: %v", err)
	}
	if err := storage.New(cfg.ConfigDir).CheckRestorePending(); err != nil {
		t.Fatalf("restore gate remains after verified restore: %v", err)
	}
	conn, err := sql.Open("sqlite", cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	var disabledAt string
	if err := conn.QueryRow("SELECT disabled_at FROM controller_users WHERE singleton = 1").Scan(&disabledAt); err != nil || disabledAt == "" {
		t.Fatalf("restored administrator disabled_at = %q, error = %v", disabledAt, err)
	}
	for _, table := range []struct {
		name string
		row  *sql.Row
	}{
		{"controller_sessions", conn.QueryRow("SELECT count(*) FROM controller_sessions")},
		{"controller_recovery_codes", conn.QueryRow("SELECT count(*) FROM controller_recovery_codes")},
		{"controller_auth_attempts", conn.QueryRow("SELECT count(*) FROM controller_auth_attempts")},
	} {
		var count int
		if err := table.row.Scan(&count); err != nil || count != 0 {
			t.Fatalf("restored %s count = %d, error = %v", table.name, count, err)
		}
	}
	if len(archive.Data) == 0 || strings.Contains(string(archive.Data), secret) {
		t.Fatal("controller backup is empty or exposes authentication material")
	}
	plain, err := decrypt(archive.Data, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	files, _, state, err := readPlainZip(plain)
	if err != nil {
		t.Fatal(err)
	}
	if state.Controller == nil || state.Controller.ControllerID != activated.ControllerID {
		t.Fatal("controller identity is missing from backup")
	}
	paths := map[string]bool{}
	for _, file := range files {
		paths[file.Path] = true
	}
	if !paths[controlauth.KeyFileName] || !paths[controllerSnapshotArchivePath] {
		t.Fatalf("controller backup files = %v", paths)
	}
	staged, err := filepath.Glob(filepath.Join(cfg.ConfigDir, ".backup-snapshot-*"))
	if err != nil || len(staged) != 0 {
		t.Fatalf("snapshot staging paths = %v, error = %v", staged, err)
	}
}

func TestControllerRestoreWithDatabaseInsideConfigDirectory(t *testing.T) {
	cfg := testConfig(t)
	cfg.DatabaseMode = sqldb.ModeSQLite
	cfg.DatabasePath = filepath.Join(cfg.ConfigDir, "awg-forge.db")
	cfg.DatabaseQueryTimeout = 5 * time.Second
	cfg.DatabaseBusyTimeout = 5 * time.Second
	cfg.DatabaseMaxOpenConns = 1
	cfg.DatabaseMaxIdleConns = 1
	svc := app.New(cfg)
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("controller-internal-backup-secret"))
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	code, err := totp.GenerateCode(secret, now)
	if err != nil {
		t.Fatal(err)
	}
	activated, err := svc.ActivateController(context.Background(), app.ControllerActivationRequest{
		Username: "admin", Password: testPassword, TOTPSecret: secret, TOTPConfirmation: code, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	archive, err := Create(context.Background(), cfg, svc, testPassword, Options{})
	if err != nil {
		t.Fatal(err)
	}
	insideArchive := filepath.Join(cfg.ConfigDir, "controller.afbackup")
	if err := os.WriteFile(insideArchive, archive.Data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Restore(context.Background(), cfg, testPassword, insideArchive); err == nil || !strings.Contains(err.Error(), "outside the configuration directory") {
		t.Fatalf("controller archive inside config directory error = %v", err)
	}
	if err := os.Remove(insideArchive); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"audit.log", "audit.log.1"} {
		if err := os.WriteFile(filepath.Join(cfg.ConfigDir, name), []byte("preserved audit"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := Restore(context.Background(), cfg, testPassword, writeTempArchive(t, archive.Data)); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"audit.log", "audit.log.1"} {
		if body, err := os.ReadFile(filepath.Join(cfg.ConfigDir, name)); err != nil || string(body) != "preserved audit" {
			t.Fatalf("%s was not preserved: %q, %v", name, body, err)
		}
	}
	state, err := storage.New(cfg.ConfigDir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if state.Controller == nil || state.Controller.ControllerID != activated.ControllerID {
		t.Fatalf("restored controller identity = %#v", state.Controller)
	}
	if err := sqldb.VerifyControllerSnapshot(context.Background(), cfg.DatabasePath); err != nil {
		t.Fatal(err)
	}
	assertMode(t, cfg.DatabasePath, 0600)
	assertMode(t, filepath.Join(cfg.ConfigDir, controlauth.KeyFileName), 0600)
}

func TestControllerRestoreRejectsDifferentControllerIdentity(t *testing.T) {
	makeController := func(t *testing.T, name string) (config.Config, *app.Service, string) {
		t.Helper()
		cfg := testConfig(t)
		cfg.DatabaseMode = sqldb.ModeSQLite
		cfg.DatabasePath = filepath.Join(cfg.ConfigDir, "awg-forge.db")
		cfg.DatabaseQueryTimeout = 5 * time.Second
		cfg.DatabaseBusyTimeout = 5 * time.Second
		cfg.DatabaseMaxOpenConns = 1
		cfg.DatabaseMaxIdleConns = 1
		svc := app.New(cfg)
		secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte(name + "-identity-secret"))
		now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
		code, err := totp.GenerateCode(secret, now)
		if err != nil {
			t.Fatal(err)
		}
		result, err := svc.ActivateController(context.Background(), app.ControllerActivationRequest{
			Username: "admin", Password: testPassword, TOTPSecret: secret, TOTPConfirmation: code, Now: now,
		})
		if err != nil {
			t.Fatal(err)
		}
		return cfg, svc, result.ControllerID
	}
	sourceCfg, sourceSvc, sourceID := makeController(t, "source")
	archive, err := Create(context.Background(), sourceCfg, sourceSvc, testPassword, Options{})
	if err != nil {
		t.Fatal(err)
	}
	targetCfg, _, targetID := makeController(t, "target")
	if sourceID == targetID {
		t.Fatal("controller test identities unexpectedly match")
	}
	if err := Restore(context.Background(), targetCfg, testPassword, writeTempArchive(t, archive.Data)); err == nil || !strings.Contains(err.Error(), "same controller identity") {
		t.Fatalf("cross-controller restore error = %v", err)
	}
	if err := storage.New(targetCfg.ConfigDir).CheckRestorePending(); err != nil {
		t.Fatalf("cross-controller restore changed recovery gate: %v", err)
	}
	state, err := storage.New(targetCfg.ConfigDir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if state.Controller == nil || state.Controller.ControllerID != targetID {
		t.Fatal("cross-controller restore changed target identity")
	}
}

func TestControllerRestoreInterruptedBeforeStateCommitStaysFailClosed(t *testing.T) {
	cfg := testConfig(t)
	cfg.DatabaseMode = sqldb.ModeSQLite
	cfg.DatabasePath = filepath.Join(cfg.ConfigDir, "awg-forge.db")
	cfg.DatabaseQueryTimeout = 5 * time.Second
	cfg.DatabaseBusyTimeout = 5 * time.Second
	cfg.DatabaseMaxOpenConns = 1
	cfg.DatabaseMaxIdleConns = 1
	svc := app.New(cfg)
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("controller-crash-restore-secret"))
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	code, err := totp.GenerateCode(secret, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ActivateController(context.Background(), app.ControllerActivationRequest{
		Username: "admin", Password: testPassword, TOTPSecret: secret, TOTPConfirmation: code, Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	archive, err := Create(context.Background(), cfg, svc, testPassword, Options{})
	if err != nil {
		t.Fatal(err)
	}
	archivePath := writeTempArchive(t, archive.Data)
	validated, err := loadAndValidate(context.Background(), testPassword, archivePath)
	if err != nil {
		t.Fatal(err)
	}
	current, err := storage.New(cfg.ConfigDir).Load()
	if err != nil {
		t.Fatal(err)
	}
	stateLock, err := storage.AcquireStateLock(cfg.ConfigDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stateLock.Close() }()
	mutationLock, err := storage.AcquireStateMutationLock(cfg.ConfigDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mutationLock.Close() }()
	injected := errors.New("injected state commit failure")
	err = restoreControllerWithFiles(context.Background(), cfg, testPassword, current, validated, func(root string, files []restoreFile) error {
		return restoreFilesWithRename(root, files, func(src, dst string) error {
			if strings.Contains(src, ".restore-tmp-") && filepath.Base(src) == "state.json" {
				return injected
			}
			return os.Rename(src, dst)
		})
	})
	if !errors.Is(err, injected) {
		t.Fatalf("interrupted restore error = %v", err)
	}
	if !errors.Is(storage.New(cfg.ConfigDir).CheckRestorePending(), storage.ErrRestorePending) {
		t.Fatal("interrupted controller restore did not block startup")
	}
	if err := mutationLock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := stateLock.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(context.Background(), cfg, svc, testPassword, Options{}); !errors.Is(err, storage.ErrRestorePending) {
		t.Fatalf("backup after interrupted restore error = %v", err)
	}
}

func TestControllerRestoreAfterExternalDatabaseSwitchStaysClosedOnVerificationFailure(t *testing.T) {
	cfg, archive := controllerBackupFixture(t, true)
	validated, err := loadAndValidate(context.Background(), testPassword, writeTempArchive(t, archive.Data))
	if err != nil {
		t.Fatal(err)
	}
	current, err := storage.New(cfg.ConfigDir).Load()
	if err != nil {
		t.Fatal(err)
	}
	stateLock, err := storage.AcquireStateLock(cfg.ConfigDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stateLock.Close() }()
	mutationLock, err := storage.AcquireStateMutationLock(cfg.ConfigDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mutationLock.Close() }()
	err = restoreControllerWithFiles(context.Background(), cfg, testPassword, current, validated, func(root string, files []restoreFile) error {
		if err := restoreFiles(root, files); err != nil {
			return err
		}
		return os.Chmod(filepath.Join(root, controlauth.KeyFileName), 0644)
	})
	if err == nil || !strings.Contains(err.Error(), "verify restored controller") {
		t.Fatalf("post-switch verification error = %v", err)
	}
	if !errors.Is(storage.New(cfg.ConfigDir).CheckRestorePending(), storage.ErrRestorePending) {
		t.Fatal("post-switch failure did not block startup")
	}
	if err := sqldb.VerifyControllerSnapshot(context.Background(), cfg.DatabasePath); err != nil {
		t.Fatalf("external database was not installed before the injected failure: %v", err)
	}
}

func TestControllerRestoreMarkerClearFailureKeepsStartupBlocked(t *testing.T) {
	cfg, archive := controllerBackupFixture(t, false)
	validated, err := loadAndValidate(context.Background(), testPassword, writeTempArchive(t, archive.Data))
	if err != nil {
		t.Fatal(err)
	}
	current, err := storage.New(cfg.ConfigDir).Load()
	if err != nil {
		t.Fatal(err)
	}
	stateLock, err := storage.AcquireStateLock(cfg.ConfigDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stateLock.Close() }()
	mutationLock, err := storage.AcquireStateMutationLock(cfg.ConfigDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mutationLock.Close() }()
	err = restoreControllerWithFiles(context.Background(), cfg, testPassword, current, validated, func(root string, files []restoreFile) error {
		if err := restoreFiles(root, files); err != nil {
			return err
		}
		return os.Chmod(storage.New(root).RestorePendingPath(), 0644)
	})
	if err == nil || !strings.Contains(err.Error(), "clear restored controller gate") {
		t.Fatalf("marker-clear error = %v", err)
	}
	if !errors.Is(storage.New(cfg.ConfigDir).CheckRestorePending(), storage.ErrRestorePending) {
		t.Fatal("invalid marker did not block startup")
	}
}

func controllerBackupFixture(t *testing.T, externalDatabase bool) (config.Config, Archive) {
	t.Helper()
	cfg := testConfig(t)
	cfg.DatabaseMode = sqldb.ModeSQLite
	if externalDatabase {
		cfg.DatabasePath = filepath.Join(t.TempDir(), "controller.sqlite")
	} else {
		cfg.DatabasePath = filepath.Join(cfg.ConfigDir, "awg-forge.db")
	}
	cfg.DatabaseQueryTimeout = 5 * time.Second
	cfg.DatabaseBusyTimeout = 5 * time.Second
	cfg.DatabaseMaxOpenConns = 1
	cfg.DatabaseMaxIdleConns = 1
	svc := app.New(cfg)
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("controller-fault-fixture-secret"))
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	code, err := totp.GenerateCode(secret, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ActivateController(context.Background(), app.ControllerActivationRequest{
		Username: "admin", Password: testPassword, TOTPSecret: secret, TOTPConfirmation: code, Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	archive, err := Create(context.Background(), cfg, svc, testPassword, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return cfg, archive
}

func TestRestoreManagedBackupRequiresExplicitDetachOnNewInstallation(t *testing.T) {
	sourceCfg := testConfig(t)
	sourceSvc, sourceState := managedBackupService(t, sourceCfg, testManagedBackupState())
	archive, err := Create(context.Background(), sourceCfg, sourceSvc, testPassword, Options{})
	if err != nil {
		t.Fatal(err)
	}

	restoreCfg := sourceCfg
	restoreCfg.ConfigDir = t.TempDir()
	archivePath := writeTempArchive(t, archive.Data)
	if err := Restore(context.Background(), restoreCfg, testPassword, archivePath); !errors.Is(err, app.ErrManagedNodeRestoreIdentityConflict) {
		t.Fatalf("restore error = %v, want %v", err, app.ErrManagedNodeRestoreIdentityConflict)
	}
	if _, err := os.Stat(filepath.Join(restoreCfg.ConfigDir, "state.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restore wrote state before identity authorization: %v", err)
	}

	now := time.Date(2026, 9, 8, 15, 0, 0, 0, time.UTC)
	result, err := RestoreWithOptions(context.Background(), restoreCfg, testPassword, archivePath, RestoreOptions{
		DetachManagedNode: true,
		Now:               now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.ManagedNodeDetached {
		t.Fatal("restore result did not report managed-node detach")
	}
	restored, err := storage.New(restoreCfg.ConfigDir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if restored.ManagedNode != nil {
		t.Fatalf("managed identity remains after detached restore: %#v", restored.ManagedNode)
	}
	if len(restored.Tunnels) != len(sourceState.Tunnels) || restored.Tunnels[0].ID != sourceState.Tunnels[0].ID {
		t.Fatalf("detached restore did not preserve tunnels: %#v", restored.Tunnels)
	}
	if !restored.UpdatedAt.Equal(now) {
		t.Fatalf("updated at = %v, want %v", restored.UpdatedAt, now)
	}
	assertMode(t, restoreCfg.ConfigDir, 0700)
	assertMode(t, filepath.Join(restoreCfg.ConfigDir, "state.json"), 0600)
}

func TestRestoreManagedBackupPreservesMatchingInstallationIdentity(t *testing.T) {
	cfg := testConfig(t)
	svc, sourceState := managedBackupService(t, cfg, testManagedBackupState())
	archive, err := Create(context.Background(), cfg, svc, testPassword, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := Restore(context.Background(), cfg, testPassword, writeTempArchive(t, archive.Data)); err != nil {
		t.Fatal(err)
	}
	restored, err := storage.New(cfg.ConfigDir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.ManagedNode, sourceState.ManagedNode) {
		t.Fatalf("managed identity changed during in-place restore: %#v", restored.ManagedNode)
	}
}

func TestRestoreRejectsManagedIdentityMismatchWithoutChangingTarget(t *testing.T) {
	sourceCfg := testConfig(t)
	sourceSvc, _ := managedBackupService(t, sourceCfg, testManagedBackupState())
	archive, err := Create(context.Background(), sourceCfg, sourceSvc, testPassword, Options{})
	if err != nil {
		t.Fatal(err)
	}

	targetCfg := testConfig(t)
	targetManaged := testManagedBackupState()
	targetManaged.NodeID = "55555555-5555-4555-8555-555555555555"
	_, targetState := managedBackupService(t, targetCfg, targetManaged)
	err = Restore(context.Background(), targetCfg, testPassword, writeTempArchive(t, archive.Data))
	if !errors.Is(err, app.ErrManagedNodeRestoreIdentityConflict) {
		t.Fatalf("restore error = %v, want %v", err, app.ErrManagedNodeRestoreIdentityConflict)
	}
	persisted, err := storage.New(targetCfg.ConfigDir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ManagedNode == nil || persisted.ManagedNode.NodeID != targetState.ManagedNode.NodeID {
		t.Fatalf("target identity changed after rejected restore: %#v", persisted.ManagedNode)
	}
	if matches, err := filepath.Glob(filepath.Join(targetCfg.ConfigDir, "backups", "pre-restore-*.afbackup")); err != nil || len(matches) != 0 {
		t.Fatalf("rejected restore created pre-restore backup: matches=%v err=%v", matches, err)
	}
}

func TestRestoreStandaloneBackupRequiresDetachOnManagedInstallation(t *testing.T) {
	sourceCfg := testConfig(t)
	sourceSvc := app.New(sourceCfg)
	if _, err := sourceSvc.Init(); err != nil {
		t.Fatal(err)
	}
	archive, err := Create(context.Background(), sourceCfg, sourceSvc, testPassword, Options{})
	if err != nil {
		t.Fatal(err)
	}

	targetCfg := testConfig(t)
	_, _ = managedBackupService(t, targetCfg, testManagedBackupState())
	archivePath := writeTempArchive(t, archive.Data)
	if err := Restore(context.Background(), targetCfg, testPassword, archivePath); !errors.Is(err, app.ErrManagedNodeRestoreIdentityConflict) {
		t.Fatalf("restore error = %v, want %v", err, app.ErrManagedNodeRestoreIdentityConflict)
	}
	result, err := RestoreWithOptions(context.Background(), targetCfg, testPassword, archivePath, RestoreOptions{DetachManagedNode: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.ManagedNodeDetached {
		t.Fatal("restore result did not report managed-node detach")
	}
	restored, err := storage.New(targetCfg.ConfigDir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if restored.ManagedNode != nil {
		t.Fatalf("managed identity remains after standalone restore: %#v", restored.ManagedNode)
	}
}

func TestRestoreReportsNoDetachForStandaloneState(t *testing.T) {
	sourceCfg := testConfig(t)
	sourceSvc := app.New(sourceCfg)
	if _, err := sourceSvc.Init(); err != nil {
		t.Fatal(err)
	}
	archive, err := Create(context.Background(), sourceCfg, sourceSvc, testPassword, Options{})
	if err != nil {
		t.Fatal(err)
	}

	targetCfg := testConfig(t)
	result, err := RestoreWithOptions(context.Background(), targetCfg, testPassword, writeTempArchive(t, archive.Data), RestoreOptions{
		DetachManagedNode: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ManagedNodeDetached {
		t.Fatal("standalone restore incorrectly reported a managed-node detach")
	}
}

func TestRestoreRejectsStateDirectoryInUseBeforeReadingTarget(t *testing.T) {
	sourceCfg := testConfig(t)
	sourceSvc := app.New(sourceCfg)
	if _, err := sourceSvc.Init(); err != nil {
		t.Fatal(err)
	}
	archive, err := Create(context.Background(), sourceCfg, sourceSvc, testPassword, Options{})
	if err != nil {
		t.Fatal(err)
	}

	targetCfg := testConfig(t)
	stateLock, err := storage.AcquireStateLock(targetCfg.ConfigDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := stateLock.Close(); err != nil {
			t.Error(err)
		}
	})

	_, err = RestoreWithOptions(context.Background(), targetCfg, testPassword, writeTempArchive(t, archive.Data), RestoreOptions{})
	if !errors.Is(err, storage.ErrStateDirectoryInUse) {
		t.Fatalf("restore error = %v, want %v", err, storage.ErrStateDirectoryInUse)
	}
	if _, err := os.Stat(filepath.Join(targetCfg.ConfigDir, "state.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("locked restore changed target state: %v", err)
	}
}

func TestVerifyRejectsInvalidManagedNodeMetadata(t *testing.T) {
	cfg := testConfig(t)
	svc, _ := managedBackupService(t, cfg, testManagedBackupState())
	archive, err := Create(context.Background(), cfg, svc, testPassword, Options{})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := decrypt(archive.Data, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	var mutatedStateJSON []byte
	mutated := mutatePlainZip(t, plain, func(name string, b []byte) []byte {
		switch name {
		case "state.json":
			var state config.State
			if err := json.Unmarshal(b, &state); err != nil {
				t.Fatal(err)
			}
			state.ManagedNode.StateEpoch = "not-a-uuid"
			mutatedStateJSON = mustJSON(t, state)
			return mutatedStateJSON
		case "metadata.json":
			var metadata Metadata
			if err := json.Unmarshal(b, &metadata); err != nil {
				t.Fatal(err)
			}
			for i := range metadata.Files {
				if metadata.Files[i].Path == "state.json" {
					metadata.Files[i] = testFileMeta("state.json", mutatedStateJSON)
				}
			}
			return mustJSON(t, metadata)
		default:
			return b
		}
	})
	encrypted, err := encrypt(mutated, testPassword, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	_, err = Verify(context.Background(), cfg, testPassword, writeTempArchive(t, encrypted))
	if !errors.Is(err, app.ErrInvalidManagedNodeState) {
		t.Fatalf("verify error = %v, want %v", err, app.ErrInvalidManagedNodeState)
	}
}

func TestRestoreRejectsNewerSchema(t *testing.T) {
	cfg := testConfig(t)
	svc := app.New(cfg)
	if _, err := svc.AddClient("phone"); err != nil {
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
	mutated := mutatePlainZip(t, plain, func(name string, b []byte) []byte {
		if name != "metadata.json" {
			return b
		}
		var raw map[string]any
		if err := json.Unmarshal(b, &raw); err != nil {
			t.Fatal(err)
		}
		raw["schema_version"] = float64(config.CurrentStateSchemaVersion + 1)
		out, err := json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		return out
	})
	encrypted, err := encrypt(mutated, testPassword, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	restoreCfg := cfg
	restoreCfg.ConfigDir = t.TempDir()
	err = Restore(context.Background(), restoreCfg, testPassword, writeTempArchive(t, encrypted))
	if err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("restore error = %v, want newer schema rejection", err)
	}
}

func TestRestoreRejectsMissingMetadata(t *testing.T) {
	var plain bytes.Buffer
	zw := zip.NewWriter(&plain)
	w, err := zw.Create("state.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(`{"schema_version":2}`)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	encrypted, err := encrypt(plain.Bytes(), testPassword, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t)
	err = Restore(context.Background(), cfg, testPassword, writeTempArchive(t, encrypted))
	if err == nil || !strings.Contains(err.Error(), "metadata.json is missing") {
		t.Fatalf("restore error = %v, want missing metadata", err)
	}
}

func TestDecryptRejectsUnexpectedKDFParameters(t *testing.T) {
	archive := encryptedTestZip(t,
		testZipEntry{name: "metadata.json", data: mustJSON(t, testMetadata(nil))},
		testZipEntry{name: "state.json", data: []byte(`{"schema_version":2}`)},
	)
	var env encryptedArchive
	if err := json.Unmarshal(archive, &env); err != nil {
		t.Fatal(err)
	}
	env.KDF.MemoryKiB = 1024 * 1024
	mutated, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decrypt(mutated, testPassword); err == nil || !strings.Contains(err.Error(), "unsupported backup kdf") {
		t.Fatalf("decrypt error = %v, want unsupported backup kdf", err)
	}
}

func TestVerifyRejectsOversizedBackupFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "too-large.afbackup")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxEncryptedBackupBytes + 1); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t)
	_, err = Verify(context.Background(), cfg, testPassword, path)
	if err == nil || !strings.Contains(err.Error(), "backup file is too large") {
		t.Fatalf("verify error = %v, want backup file is too large", err)
	}
}

func TestRestoreRejectsFilesNotListedInMetadata(t *testing.T) {
	stateJSON := `{"schema_version":2}`
	metadata := testMetadata([]FileMeta{testFileMeta("state.json", []byte(stateJSON))})
	archive := encryptedTestZip(t,
		testZipEntry{name: "metadata.json", data: mustJSON(t, metadata)},
		testZipEntry{name: "state.json", data: []byte(stateJSON)},
		testZipEntry{name: "tunnels/awg0/server.conf", data: []byte("unexpected")},
	)
	cfg := testConfig(t)
	err := Restore(context.Background(), cfg, testPassword, writeTempArchive(t, archive))
	if err == nil || !strings.Contains(err.Error(), "not listed in metadata") {
		t.Fatalf("restore error = %v, want unlisted file rejection", err)
	}
}

func TestRestoreRejectsDuplicateArchiveFiles(t *testing.T) {
	stateJSON := `{"schema_version":2}`
	metadata := testMetadata([]FileMeta{testFileMeta("state.json", []byte(stateJSON))})
	archive := encryptedTestZip(t,
		testZipEntry{name: "metadata.json", data: mustJSON(t, metadata)},
		testZipEntry{name: "state.json", data: []byte(stateJSON)},
		testZipEntry{name: "state.json", data: []byte(stateJSON)},
	)
	cfg := testConfig(t)
	err := Restore(context.Background(), cfg, testPassword, writeTempArchive(t, archive))
	if err == nil || !strings.Contains(err.Error(), "state.json is duplicated") {
		t.Fatalf("restore error = %v, want duplicate file rejection", err)
	}
}

func TestRestoreRejectsDuplicateMetadataFiles(t *testing.T) {
	stateJSON := `{"schema_version":2}`
	meta := testFileMeta("state.json", []byte(stateJSON))
	metadata := testMetadata([]FileMeta{meta, meta})
	archive := encryptedTestZip(t,
		testZipEntry{name: "metadata.json", data: mustJSON(t, metadata)},
		testZipEntry{name: "state.json", data: []byte(stateJSON)},
	)
	cfg := testConfig(t)
	err := Restore(context.Background(), cfg, testPassword, writeTempArchive(t, archive))
	if err == nil || !strings.Contains(err.Error(), "metadata file state.json is duplicated") {
		t.Fatalf("restore error = %v, want duplicate metadata rejection", err)
	}
}

func TestRestoreRejectsZipSlipPaths(t *testing.T) {
	for _, name := range []string{"../escape", "tunnels/../../escape", `tunnels\..\escape`} {
		t.Run(name, func(t *testing.T) {
			var plain bytes.Buffer
			zw := zip.NewWriter(&plain)
			for path, content := range map[string]string{
				"metadata.json": `{"format":"awg-forge-backup-v1","schema_version":2,"created_at":"2026-01-01T00:00:00Z","files":[]}`,
				"state.json":    `{"schema_version":2}`,
				name:            "bad",
			} {
				w, err := zw.Create(path)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := w.Write([]byte(content)); err != nil {
					t.Fatal(err)
				}
			}
			if err := zw.Close(); err != nil {
				t.Fatal(err)
			}
			encrypted, err := encrypt(plain.Bytes(), testPassword, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			cfg := testConfig(t)
			err = Restore(context.Background(), cfg, testPassword, writeTempArchive(t, encrypted))
			if err == nil || !strings.Contains(err.Error(), "invalid archive path") {
				t.Fatalf("restore error = %v, want invalid archive path", err)
			}
		})
	}
}

func TestSafeRestorePathStaysUnderRoot(t *testing.T) {
	root := t.TempDir()
	got, err := safeRestorePath(root, "tunnels/awg0/server.conf")
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(root, got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		t.Fatalf("restore path escaped root: %s", got)
	}
	if _, err := safeRestorePath(root, "../escape"); err == nil {
		t.Fatal("expected traversal path to be rejected")
	}
	if _, err := safeRestorePath(root, storage.StateLockFileName); err == nil {
		t.Fatal("expected reserved state lock path to be rejected")
	}
	if _, err := safeRestorePath(root, storage.StateMutationLockFileName); err == nil {
		t.Fatal("expected reserved state mutation lock path to be rejected")
	}
}

func TestRestoreFilesRollsBackBeforeStateCommit(t *testing.T) {
	for _, phase := range []string{"move-current", "install-candidate", "commit-state"} {
		t.Run(phase, func(t *testing.T) {
			root := t.TempDir()
			currentState := []byte(`{"schema_version":1,"server_host":"current.example"}`)
			for name, data := range map[string][]byte{
				"state.json":    currentState,
				"current-a.txt": []byte("current-a"),
				"current-b.txt": []byte("current-b"),
			} {
				if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
					t.Fatal(err)
				}
			}

			injected := errors.New("injected " + phase + " failure")
			failed := false
			rename := func(src, dst string) error {
				base := filepath.Base(src)
				shouldFail := false
				switch phase {
				case "move-current":
					shouldFail = filepath.Dir(src) == root && base == "current-b.txt"
				case "install-candidate":
					shouldFail = strings.Contains(src, ".restore-tmp-") && base == "next-b.txt"
				case "commit-state":
					shouldFail = strings.Contains(src, ".restore-tmp-") && base == "state.json"
				}
				if !failed && shouldFail {
					failed = true
					return injected
				}
				return os.Rename(src, dst)
			}
			err := restoreFilesWithRename(root, []restoreFile{
				{Path: "state.json", Data: []byte(`{"schema_version":1,"server_host":"next.example"}`)},
				{Path: "next-a.txt", Data: []byte("next-a")},
				{Path: "next-b.txt", Data: []byte("next-b")},
			}, rename)
			if !errors.Is(err, injected) {
				t.Fatalf("restore error = %v, want %v", err, injected)
			}
			assertRestoredFile(t, filepath.Join(root, "state.json"), currentState)
			assertRestoredFile(t, filepath.Join(root, "current-a.txt"), []byte("current-a"))
			assertRestoredFile(t, filepath.Join(root, "current-b.txt"), []byte("current-b"))
			for _, name := range []string{"next-a.txt", "next-b.txt"} {
				if _, err := os.Stat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("candidate file %s remains after rollback: %v", name, err)
				}
			}
		})
	}
}

func assertRestoredFile(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

func TestWriteFileUsesPrivatePermissions(t *testing.T) {
	cfg := testConfig(t)
	svc := app.New(cfg)
	if _, err := svc.AddClient("phone"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "backup.afbackup")
	if _, err := WriteFile(context.Background(), cfg, svc, testPassword, path); err != nil {
		t.Fatal(err)
	}
	assertMode(t, path, 0600)
}

func writeTempArchive(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "backup.afbackup")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

type testZipEntry struct {
	name string
	data []byte
}

func encryptedTestZip(t *testing.T, entries ...testZipEntry) []byte {
	t.Helper()
	var plain bytes.Buffer
	zw := zip.NewWriter(&plain)
	for _, entry := range entries {
		w, err := zw.Create(entry.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	encrypted, err := encrypt(plain.Bytes(), testPassword, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return encrypted
}

func testMetadata(files []FileMeta) Metadata {
	return Metadata{
		Format:        formatVersion,
		SchemaVersion: config.CurrentStateSchemaVersion,
		CreatedAt:     "2026-01-01T00:00:00Z",
		Files:         files,
	}
}

func testFileMeta(path string, data []byte) FileMeta {
	sum := sha256.Sum256(data)
	return FileMeta{Path: path, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mutatePlainZip(t *testing.T, data []byte, mutate func(string, []byte) []byte) []byte {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, file := range reader.File {
		rc, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		w, err := zw.Create(file.Name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(mutate(file.Name, b)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}

func managedBackupService(t *testing.T, cfg config.Config, managed *config.ManagedNodeState) (*app.Service, config.State) {
	t.Helper()
	svc := app.New(cfg)
	state, err := svc.Init()
	if err != nil {
		t.Fatal(err)
	}
	state.ManagedNode = managed
	state.Mode = config.ModeNode
	if err := storage.New(cfg.ConfigDir).Save(state); err != nil {
		t.Fatal(err)
	}
	return svc, state
}

func testManagedBackupState() *config.ManagedNodeState {
	return &config.ManagedNodeState{
		NodeID:       "11111111-1111-4111-8111-111111111111",
		ControllerID: "22222222-2222-4222-8222-222222222222",
		StateEpoch:   "33333333-3333-4333-8333-333333333333",
		BindingEpoch: 1,
	}
}

func testConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{
		ConfigDir:           t.TempDir(),
		TunnelName:          "awg0",
		ServerHost:          "vpn.example.com",
		ListenPort:          51820,
		WebUIHost:           "127.0.0.1",
		WebUIPort:           51821,
		ExternalInterface:   "eth0",
		IPv4Subnet:          "10.8.0.0/24",
		DNS:                 "1.1.1.1",
		AllowedIPs:          "0.0.0.0/0",
		PersistentKeepalive: 0,
		MTU:                 1280,
		ProtocolProfile:     "awg_legacy_1_0",
	}
}
