package app

import (
	"context"
	"encoding/base32"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlauth"
	"github.com/astronaut808/awg-forge/internal/sqldb"
	"github.com/astronaut808/awg-forge/internal/storage"
	"github.com/pquerna/otp/totp"
)

const controllerTestPassword = "correct horse battery staple"

var controllerTestTOTPSecret = base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("controller-activation-test-secret"))

func TestControllerActivationCommitsModeLastAndSurvivesRestart(t *testing.T) {
	cfg := controllerTestConfig(t)
	svc := newFastControllerTestService(cfg)
	state, err := svc.Init()
	if err != nil {
		t.Fatal(err)
	}
	tunnelID := state.Tunnels[0].ID
	now := time.Unix(1_800_000_015, 0).UTC()
	result, err := svc.ActivateController(context.Background(), controllerTestActivationRequest(t, now))
	if err != nil {
		t.Fatal(err)
	}
	if result.ControllerID == "" || result.Username != "admin" || len(result.RecoveryCodes) != controlauth.DefaultRecoveryCodeCount {
		t.Fatalf("activation result = %#v", result)
	}
	state, err = storage.New(cfg.ConfigDir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if state.Mode != config.ModeController || state.Controller == nil || state.Controller.ControllerID != result.ControllerID {
		t.Fatalf("controller state = %#v", state)
	}
	if state.Tunnels[0].ID != tunnelID {
		t.Fatal("activation replaced standalone tunnel state")
	}
	if _, err := os.Stat(filepath.Join(cfg.ConfigDir, controlauth.KeyFileName)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(storage.New(cfg.ConfigDir).ControllerActivationJournalPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("activation journal remains after commit: %v", err)
	}

	restarted, err := newFastControllerTestService(cfg).Init()
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Controller == nil || restarted.Controller.ControllerID != result.ControllerID {
		t.Fatalf("restarted controller state = %#v", restarted.Controller)
	}
	if _, err := svc.ActivateController(context.Background(), controllerTestActivationRequest(t, now.Add(time.Minute))); !errors.Is(err, ErrControllerAlreadyActive) {
		t.Fatalf("second activation error = %v", err)
	}
}

func TestOfflineAdminRecoveryPreservesControllerIdentityAndRevokesInitialSession(t *testing.T) {
	cfg := controllerTestConfig(t)
	svc := newFastControllerTestService(cfg)
	if _, err := svc.Init(); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_015, 0).UTC()
	activated, err := svc.ActivateController(context.Background(), controllerTestActivationRequest(t, now))
	if err != nil {
		t.Fatal(err)
	}
	db, err := sqldb.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	keys, err := controlauth.LoadKeys(filepath.Join(cfg.ConfigDir, controlauth.KeyFileName))
	if err != nil {
		t.Fatal(err)
	}
	auth, err := controlauth.NewService(db, keys, fastControllerAuthOptions())
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := svc.RecoverControllerAdmin(context.Background(), auth, "restored-admin", "new correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if recovered.TOTPSecret == "" || len(recovered.RecoveryCodes) != controlauth.DefaultRecoveryCodeCount {
		t.Fatal("recovery material incomplete")
	}
	state, err := storage.New(cfg.ConfigDir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if state.Controller == nil || state.Controller.ControllerID != activated.ControllerID {
		t.Fatal("controller identity changed during admin recovery")
	}
	if _, err := auth.ValidateSession(context.Background(), activated.Authentication.Token, time.Now().UTC()); !errors.Is(err, controlauth.ErrSessionNotFound) {
		t.Fatalf("initial session after recovery: %v", err)
	}
}

func TestInitMigratesLegacyStandaloneStateToExplicitMode(t *testing.T) {
	cfg := controllerTestConfig(t)
	svc := newFastControllerTestService(cfg)
	state, err := svc.Init()
	if err != nil {
		t.Fatal(err)
	}
	state.Mode = ""
	state.SchemaVersion = config.CurrentStateSchemaVersion - 1
	if err := storage.New(cfg.ConfigDir).Save(state); err != nil {
		t.Fatal(err)
	}
	migrated, err := newFastControllerTestService(cfg).Init()
	if err != nil {
		t.Fatal(err)
	}
	if migrated.Mode != config.ModeStandalone || migrated.SchemaVersion != config.CurrentStateSchemaVersion {
		t.Fatalf("migrated state mode/schema = %q/%d", migrated.Mode, migrated.SchemaVersion)
	}
	persisted, err := storage.New(cfg.ConfigDir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Mode != config.ModeStandalone {
		t.Fatalf("persisted migrated mode = %q", persisted.Mode)
	}
}

func TestControllerActivationRollsBackWhenModeCommitFails(t *testing.T) {
	cfg := controllerTestConfig(t)
	svc := newFastControllerTestService(cfg)
	if _, err := svc.Init(); err != nil {
		t.Fatal(err)
	}
	store := storage.New(cfg.ConfigDir)
	ctx, cancel := context.WithCancel(context.Background())
	svc.saveState = func(state config.State) error {
		if state.EffectiveMode() == config.ModeController {
			cancel()
			return errors.New("injected mode commit failure")
		}
		return store.Save(state)
	}
	now := time.Unix(1_800_000_015, 0).UTC()
	if _, err := svc.ActivateController(ctx, controllerTestActivationRequest(t, now)); err == nil {
		t.Fatal("activation unexpectedly succeeded")
	}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if state.EffectiveMode() != config.ModeStandalone || state.Controller != nil {
		t.Fatalf("failed activation changed state: %#v", state)
	}
	assertControllerActivationArtifactsRemoved(t, cfg)
}

func TestInitRecoversInterruptedControllerActivation(t *testing.T) {
	cfg := controllerTestConfig(t)
	svc := newFastControllerTestService(cfg)
	if _, err := svc.Init(); err != nil {
		t.Fatal(err)
	}
	store := storage.New(cfg.ConfigDir)
	now := time.Unix(1_800_000_015, 0).UTC()
	if err := store.SaveControllerActivationJournal(storage.ControllerActivationJournal{
		ControllerID: "11111111-1111-4111-8111-111111111111",
		StartedAt:    now,
	}); err != nil {
		t.Fatal(err)
	}
	db, err := sqldb.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(context.Background()); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	keys, err := controlauth.LoadOrCreateKeys(filepath.Join(cfg.ConfigDir, controlauth.KeyFileName), nil)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	auth, err := controlauth.NewService(db, keys, fastControllerAuthOptions())
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err := auth.EnrollAdmin(context.Background(), "admin", controllerTestPassword, controllerTestTOTPSecret, controllerTestTOTPCode(t, now), now); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	state, err := newFastControllerTestService(cfg).Init()
	if err != nil {
		t.Fatal(err)
	}
	if state.EffectiveMode() != config.ModeStandalone {
		t.Fatalf("recovered mode = %q", state.EffectiveMode())
	}
	assertControllerActivationArtifactsRemoved(t, cfg)
}

func TestControllerActivationRollbackKeepsJournalUntilDatabaseResetSucceeds(t *testing.T) {
	cfg := controllerTestConfig(t)
	svc := newFastControllerTestService(cfg)
	if _, err := svc.Init(); err != nil {
		t.Fatal(err)
	}
	store := storage.New(cfg.ConfigDir)
	if err := store.SaveControllerActivationJournal(storage.ControllerActivationJournal{
		ControllerID: "11111111-1111-4111-8111-111111111111",
		StartedAt:    time.Unix(1_800_000_015, 0).UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(cfg.ConfigDir, controlauth.KeyFileName)
	if _, err := controlauth.LoadOrCreateKeys(keyPath, nil); err != nil {
		t.Fatal(err)
	}
	db, err := sqldb.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := svc.rollbackControllerActivation(canceled, db); err == nil {
		t.Fatal("rollback with canceled database context unexpectedly succeeded")
	}
	if _, err := os.Lstat(store.ControllerActivationJournalPath()); err != nil {
		t.Fatalf("recovery journal was removed before database reset: %v", err)
	}
	if _, err := os.Lstat(keyPath); err != nil {
		t.Fatalf("key was removed before database reset: %v", err)
	}
}

func TestControllerActivationRequiresSQLiteBeforeWritingArtifacts(t *testing.T) {
	cfg := controllerTestConfig(t)
	cfg.DatabaseMode = sqldb.ModeOff
	svc := newFastControllerTestService(cfg)
	if _, err := svc.Init(); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ActivateController(context.Background(), controllerTestActivationRequest(t, time.Now().UTC())); !errors.Is(err, ErrControllerActivationRequiresDB) {
		t.Fatalf("activation error = %v", err)
	}
	if _, err := os.Stat(storage.New(cfg.ConfigDir).ControllerActivationJournalPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal created without SQLite: %v", err)
	}
}

func assertControllerActivationArtifactsRemoved(t *testing.T, cfg config.Config) {
	t.Helper()
	for _, path := range []string{
		filepath.Join(cfg.ConfigDir, controlauth.KeyFileName),
		storage.New(cfg.ConfigDir).ControllerActivationJournalPath(),
	} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("activation artifact %s remains: %v", path, err)
		}
	}
	db, err := sqldb.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	initialized, err := db.ControllerAuthInitialized(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if initialized {
		t.Fatal("controller authentication remains initialized")
	}
}

func controllerTestActivationRequest(t *testing.T, now time.Time) ControllerActivationRequest {
	t.Helper()
	return ControllerActivationRequest{
		Username:         "Admin",
		Password:         controllerTestPassword,
		TOTPSecret:       controllerTestTOTPSecret,
		TOTPConfirmation: controllerTestTOTPCode(t, now),
		Now:              now,
	}
}

func controllerTestTOTPCode(t *testing.T, now time.Time) string {
	t.Helper()
	code, err := totp.GenerateCode(controllerTestTOTPSecret, now)
	if err != nil {
		t.Fatal(err)
	}
	return code
}

func newFastControllerTestService(cfg config.Config) *Service {
	svc := New(cfg)
	svc.controllerAuthOptions = fastControllerAuthOptions()
	return svc
}

func fastControllerAuthOptions() controlauth.Options {
	return controlauth.Options{
		PasswordParams: controlauth.PasswordParams{
			Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32,
		},
		MaxConcurrentHash: 2,
	}
}

func controllerTestConfig(t *testing.T) config.Config {
	t.Helper()
	dir := t.TempDir()
	return config.Config{
		ConfigDir:            dir,
		TunnelName:           "awg0",
		ServerHost:           "vpn.example.com",
		ListenPort:           51820,
		WebUIHost:            "127.0.0.1",
		WebUIPort:            51821,
		ExternalInterface:    "eth0",
		IPv4Subnet:           "10.8.0.0/24",
		DNS:                  "1.1.1.1",
		AllowedIPs:           "0.0.0.0/0",
		MTU:                  1420,
		ProtocolProfile:      "awg_legacy_1_0",
		DatabaseMode:         sqldb.ModeSQLite,
		DatabasePath:         filepath.Join(dir, "awg-forge.db"),
		DatabaseBusyTimeout:  5 * time.Second,
		DatabaseQueryTimeout: 2 * time.Second,
		DatabaseMaxOpenConns: 1,
		DatabaseMaxIdleConns: 1,
	}
}
