package sqldb

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlauth"
)

func TestControllerAuthStoreLifecycle(t *testing.T) {
	db := openControllerAuthTestDB(t)
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	user := controllerAuthTestUser(now)
	recovery := controllerAuthTestDigest(1)
	if err := db.CreateControllerUser(context.Background(), user, []controlauth.Digest{recovery}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateControllerUser(context.Background(), controllerAuthTestUser(now), nil); !errors.Is(err, controlauth.ErrAlreadyInitialized) {
		t.Fatalf("second controller user error = %v", err)
	}
	stored, err := db.FindControllerUser(context.Background(), user.Username)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ID != user.ID || stored.TOTPLastStep != user.TOTPLastStep || !stored.CreatedAt.Equal(now) {
		t.Fatalf("stored controller user = %#v", stored)
	}

	first := controllerAuthTestSession(user.ID, controllerAuthTestDigest(2), now)
	if err := db.UseControllerTOTP(context.Background(), user.ID, 11, first, reserveControllerAuthTestAttempt(t, db, now)); err != nil {
		t.Fatal(err)
	}
	loaded, err := db.FindControllerSession(context.Background(), first.Digest, now)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.UserID != user.ID || loaded.Username != user.Username {
		t.Fatalf("loaded controller session = %#v", loaded)
	}
	if err := db.UseControllerTOTP(context.Background(), user.ID, 11, controllerAuthTestSession(user.ID, controllerAuthTestDigest(3), now), reserveControllerAuthTestAttempt(t, db, now)); !errors.Is(err, controlauth.ErrCredentialConsumed) {
		t.Fatalf("reused TOTP step error = %v", err)
	}
	if err := db.RevokeControllerSession(context.Background(), first.Digest, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FindControllerSession(context.Background(), first.Digest, now.Add(2*time.Minute)); !errors.Is(err, controlauth.ErrSessionNotFound) {
		t.Fatalf("revoked session error = %v", err)
	}

	recoverySession := controllerAuthTestSession(user.ID, controllerAuthTestDigest(4), now)
	if err := db.UseControllerRecoveryCode(context.Background(), user.ID, recovery, recoverySession, reserveControllerAuthTestAttempt(t, db, now)); err != nil {
		t.Fatal(err)
	}
	if err := db.UseControllerRecoveryCode(context.Background(), user.ID, recovery, controllerAuthTestSession(user.ID, controllerAuthTestDigest(5), now), reserveControllerAuthTestAttempt(t, db, now)); !errors.Is(err, controlauth.ErrCredentialConsumed) {
		t.Fatalf("reused recovery code error = %v", err)
	}
}

func TestControllerTOTPUpdateRollsBackWhenSessionInsertFails(t *testing.T) {
	db := openControllerAuthTestDB(t)
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	user := controllerAuthTestUser(now)
	if err := db.CreateControllerUser(context.Background(), user, nil); err != nil {
		t.Fatal(err)
	}
	colliding := controllerAuthTestSession(user.ID, controllerAuthTestDigest(9), now)
	if err := db.UseControllerTOTP(context.Background(), user.ID, 11, colliding, reserveControllerAuthTestAttempt(t, db, now)); err != nil {
		t.Fatal(err)
	}
	if err := db.UseControllerTOTP(context.Background(), user.ID, 12, colliding, reserveControllerAuthTestAttempt(t, db, now)); err == nil {
		t.Fatal("duplicate session digest unexpectedly succeeded")
	}
	retry := controllerAuthTestSession(user.ID, controllerAuthTestDigest(10), now)
	if err := db.UseControllerTOTP(context.Background(), user.ID, 12, retry, reserveControllerAuthTestAttempt(t, db, now)); err != nil {
		t.Fatalf("TOTP step was not rolled back after session failure: %v", err)
	}
}

func TestControllerSessionExpiresAtBoundary(t *testing.T) {
	db := openControllerAuthTestDB(t)
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	user := controllerAuthTestUser(now)
	if err := db.CreateControllerUser(context.Background(), user, nil); err != nil {
		t.Fatal(err)
	}
	session := controllerAuthTestSession(user.ID, controllerAuthTestDigest(7), now)
	if err := db.UseControllerTOTP(context.Background(), user.ID, 11, session, reserveControllerAuthTestAttempt(t, db, now)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FindControllerSession(context.Background(), session.Digest, session.ExpiresAt); !errors.Is(err, controlauth.ErrSessionNotFound) {
		t.Fatalf("session at expiry error = %v", err)
	}
	if _, err := db.ReserveControllerAuthAttempt(context.Background(), controllerAuthTestDigest(8), controllerAuthTestDigest(9), session.ExpiresAt, controlauth.DefaultRateLimitPolicy()); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, db, "SELECT count(*) FROM controller_sessions"); got != 0 {
		t.Fatalf("expired controller sessions = %d, want 0", got)
	}
}

func TestControllerAuthSuccessRollsBackWithoutPendingAttempt(t *testing.T) {
	db := openControllerAuthTestDB(t)
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	user := controllerAuthTestUser(now)
	if err := db.CreateControllerUser(context.Background(), user, nil); err != nil {
		t.Fatal(err)
	}
	rolledBack := controllerAuthTestSession(user.ID, controllerAuthTestDigest(11), now)
	if err := db.UseControllerTOTP(context.Background(), user.ID, 11, rolledBack, 999); err == nil {
		t.Fatal("authentication without a pending attempt unexpectedly succeeded")
	}
	if _, err := db.FindControllerSession(context.Background(), rolledBack.Digest, now); !errors.Is(err, controlauth.ErrSessionNotFound) {
		t.Fatalf("rolled-back session error = %v", err)
	}
	retry := controllerAuthTestSession(user.ID, controllerAuthTestDigest(12), now)
	if err := db.UseControllerTOTP(context.Background(), user.ID, 11, retry, reserveControllerAuthTestAttempt(t, db, now)); err != nil {
		t.Fatalf("TOTP step was not rolled back with attempt failure: %v", err)
	}
}

func TestControllerAuthAttemptIDsAreNotReusedAfterPruning(t *testing.T) {
	db := openControllerAuthTestDB(t)
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	policy := controlauth.DefaultRateLimitPolicy()
	first, err := db.ReserveControllerAuthAttempt(context.Background(), controllerAuthTestDigest(20), controllerAuthTestDigest(21), now, policy)
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.ReserveControllerAuthAttempt(context.Background(), controllerAuthTestDigest(22), controllerAuthTestDigest(23), now.Add(25*time.Hour), policy)
	if err != nil {
		t.Fatal(err)
	}
	if second <= first {
		t.Fatalf("attempt ID reused after pruning: first=%d second=%d", first, second)
	}
	if err := db.FinishControllerAuthAttempt(context.Background(), first, true, "", now.Add(25*time.Hour)); err == nil {
		t.Fatal("pruned attempt unexpectedly completed")
	}
	var outcome string
	if err := db.sql.QueryRowContext(context.Background(), "SELECT outcome FROM controller_auth_attempts WHERE id = ?", second).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if outcome != "pending" {
		t.Fatalf("new attempt outcome = %q, want pending", outcome)
	}
}

func TestControllerAuthAttemptRetentionDoesNotFollowCallerPolicy(t *testing.T) {
	db := openControllerAuthTestDB(t)
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	first, err := db.ReserveControllerAuthAttempt(context.Background(), controllerAuthTestDigest(24), controllerAuthTestDigest(25), now, controlauth.DefaultRateLimitPolicy())
	if err != nil {
		t.Fatal(err)
	}
	shortPolicy := controlauth.RateLimitPolicy{
		AccountFailures: 10,
		AccountWindow:   time.Minute,
		SourceFailures:  10,
		SourceWindow:    time.Minute,
		GlobalAttempts:  10,
		GlobalWindow:    time.Minute,
	}
	if _, err := db.ReserveControllerAuthAttempt(context.Background(), controllerAuthTestDigest(26), controllerAuthTestDigest(27), now.Add(2*time.Hour), shortPolicy); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.sql.QueryRowContext(context.Background(), "SELECT count(*) FROM controller_auth_attempts WHERE id = ?", first).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("short caller policy pruned history needed by a longer policy")
	}
}

func TestSQLiteConnectionSettingsApplyToEveryConnection(t *testing.T) {
	cfg := retentionTestConfig(t)
	cfg.DatabaseMaxOpenConns = 4
	cfg.DatabaseMaxIdleConns = 0
	db, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	connections := make([]*sql.Conn, 0, cfg.DatabaseMaxOpenConns)
	for range cfg.DatabaseMaxOpenConns {
		connection, err := db.sql.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, connection)
	}
	defer func() {
		for _, connection := range connections {
			_ = connection.Close()
		}
	}()
	for index, connection := range connections {
		assertConnectionPragma(t, connection, index, "foreign_keys", "1")
		assertConnectionPragma(t, connection, index, "synchronous", "2")
		assertConnectionPragma(t, connection, index, "busy_timeout", "5000")
		assertConnectionPragma(t, connection, index, "temp_store", "2")
	}
}

func TestConcurrentMigrationAndEnrollment(t *testing.T) {
	cfg := retentionTestConfig(t)
	cfg.DatabaseMaxIdleConns = 0
	first, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })

	errorsByWorker := runConcurrently(
		func() error { return first.Migrate(context.Background()) },
		func() error { return second.Migrate(context.Background()) },
	)
	for index, err := range errorsByWorker {
		if err != nil {
			t.Fatalf("migration worker %d: %v", index, err)
		}
	}

	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	otherUser := controllerAuthTestUser(now)
	otherUser.ID = "00000000-0000-4000-8000-000000000002"
	otherUser.Username = "other-admin"
	errorsByWorker = runConcurrently(
		func() error {
			return first.CreateControllerUser(context.Background(), controllerAuthTestUser(now), nil)
		},
		func() error { return second.CreateControllerUser(context.Background(), otherUser, nil) },
	)
	var created, alreadyInitialized int
	for _, err := range errorsByWorker {
		switch {
		case err == nil:
			created++
		case errors.Is(err, controlauth.ErrAlreadyInitialized):
			alreadyInitialized++
		default:
			t.Fatalf("concurrent enrollment error = %v", err)
		}
	}
	if created != 1 || alreadyInitialized != 1 {
		t.Fatalf("concurrent enrollment results: created=%d already_initialized=%d", created, alreadyInitialized)
	}
}

func TestMigrateRejectsNewerSchemaVersion(t *testing.T) {
	cfg := retentionTestConfig(t)
	db, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.sql.ExecContext(context.Background(), `
CREATE TABLE schema_migrations (
    version INTEGER PRIMARY KEY,
    checksum TEXT NOT NULL,
    applied_at TEXT NOT NULL
)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(context.Background(), "INSERT INTO schema_migrations VALUES (?, 'future', 'now')", CurrentSchemaVersion+1); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(context.Background()); err == nil {
		t.Fatal("newer database schema unexpectedly accepted")
	}
}

func TestControllerAuthMigrationFromVersionFourPreservesData(t *testing.T) {
	cfg := retentionTestConfig(t)
	db, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	migrations, err := loadSQLiteMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(context.Background(), `
CREATE TABLE schema_migrations (
    version INTEGER PRIMARY KEY,
    checksum TEXT NOT NULL,
    applied_at TEXT NOT NULL
)`); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:4] {
		if err := db.applyMigration(context.Background(), migration); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.sql.ExecContext(context.Background(), "INSERT INTO audit_events (time, level, event) VALUES (?, 'info', 'preserved')", formatTime(time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, db, "SELECT count(*) FROM audit_events WHERE event = 'preserved'"); got != 1 {
		t.Fatalf("preserved audit rows = %d, want 1", got)
	}
	if got := countRows(t, db, "SELECT count(*) FROM controller_auth_gate"); got != 1 {
		t.Fatalf("controller auth gate rows = %d, want 1", got)
	}
}

func TestControllerAuthAttemptRateLimits(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	t.Run("account", func(t *testing.T) {
		db := openControllerAuthTestDB(t)
		policy := controlauth.RateLimitPolicy{
			AccountFailures: 2,
			AccountWindow:   time.Minute,
			SourceFailures:  10,
			SourceWindow:    time.Minute,
			GlobalAttempts:  10,
			GlobalWindow:    time.Minute,
		}
		account := controllerAuthTestDigest(20)
		for index := range 2 {
			attemptID, err := db.ReserveControllerAuthAttempt(context.Background(), account, controllerAuthTestDigest(byte(30+index)), now, policy)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.FinishControllerAuthAttempt(context.Background(), attemptID, false, "invalid", now); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.ReserveControllerAuthAttempt(context.Background(), account, controllerAuthTestDigest(40), now, policy); !errors.Is(err, controlauth.ErrRateLimited) {
			t.Fatalf("account limit error = %v", err)
		}
		if _, err := db.ReserveControllerAuthAttempt(context.Background(), account, controllerAuthTestDigest(40), now.Add(time.Minute+time.Millisecond), policy); err != nil {
			t.Fatalf("account remained limited after window: %v", err)
		}
	})

	t.Run("source", func(t *testing.T) {
		db := openControllerAuthTestDB(t)
		policy := controlauth.RateLimitPolicy{
			AccountFailures: 10,
			AccountWindow:   time.Minute,
			SourceFailures:  2,
			SourceWindow:    time.Minute,
			GlobalAttempts:  10,
			GlobalWindow:    time.Minute,
		}
		source := controllerAuthTestDigest(50)
		for index := range 2 {
			attemptID, err := db.ReserveControllerAuthAttempt(context.Background(), controllerAuthTestDigest(byte(60+index)), source, now, policy)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.FinishControllerAuthAttempt(context.Background(), attemptID, false, "invalid", now); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.ReserveControllerAuthAttempt(context.Background(), controllerAuthTestDigest(70), source, now, policy); !errors.Is(err, controlauth.ErrRateLimited) {
			t.Fatalf("source limit error = %v", err)
		}
	})

	t.Run("global", func(t *testing.T) {
		db := openControllerAuthTestDB(t)
		policy := controlauth.RateLimitPolicy{
			AccountFailures: 10,
			AccountWindow:   time.Minute,
			SourceFailures:  10,
			SourceWindow:    time.Minute,
			GlobalAttempts:  2,
			GlobalWindow:    time.Minute,
		}
		for index := range 2 {
			attemptID, err := db.ReserveControllerAuthAttempt(context.Background(), controllerAuthTestDigest(byte(80+index)), controllerAuthTestDigest(byte(90+index)), now, policy)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.FinishControllerAuthAttempt(context.Background(), attemptID, true, "", now); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.ReserveControllerAuthAttempt(context.Background(), controllerAuthTestDigest(100), controllerAuthTestDigest(101), now, policy); !errors.Is(err, controlauth.ErrRateLimited) {
			t.Fatalf("global limit error = %v", err)
		}
	})
}

func openControllerAuthTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(context.Background(), retentionTestConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return db
}

func controllerAuthTestUser(now time.Time) controlauth.User {
	return controlauth.User{
		ID:                   "00000000-0000-4000-8000-000000000001",
		Username:             "admin",
		PasswordHash:         "password-hash",
		TOTPSecretCiphertext: "sealed-totp",
		TOTPLastStep:         10,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
}

func controllerAuthTestSession(userID string, digest controlauth.Digest, now time.Time) controlauth.Session {
	return controlauth.Session{
		Digest:          digest,
		UserID:          userID,
		CreatedAt:       now,
		AuthenticatedAt: now,
		ExpiresAt:       now.Add(30 * time.Minute),
	}
}

func reserveControllerAuthTestAttempt(t *testing.T, db *DB, now time.Time) int64 {
	t.Helper()
	policy := controlauth.RateLimitPolicy{
		AccountFailures: 1000,
		AccountWindow:   time.Hour,
		SourceFailures:  1000,
		SourceWindow:    time.Hour,
		GlobalAttempts:  1000,
		GlobalWindow:    time.Hour,
	}
	attemptID, err := db.ReserveControllerAuthAttempt(
		context.Background(),
		controllerAuthTestDigest(250),
		controllerAuthTestDigest(251),
		now,
		policy,
	)
	if err != nil {
		t.Fatal(err)
	}
	return attemptID
}

func assertConnectionPragma(t *testing.T, connection *sql.Conn, index int, name, want string) {
	t.Helper()
	var got string
	if err := connection.QueryRowContext(context.Background(), "PRAGMA "+name).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("connection %d PRAGMA %s = %q, want %q", index, name, got, want)
	}
}

func runConcurrently(functions ...func() error) []error {
	start := make(chan struct{})
	errorsByWorker := make([]error, len(functions))
	var wait sync.WaitGroup
	for index, function := range functions {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			errorsByWorker[index] = function()
		}()
	}
	close(start)
	wait.Wait()
	return errorsByWorker
}

func controllerAuthTestDigest(seed byte) controlauth.Digest {
	var digest controlauth.Digest
	for index := range digest {
		digest[index] = seed
	}
	return digest
}
