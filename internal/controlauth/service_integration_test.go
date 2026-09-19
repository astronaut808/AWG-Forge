package controlauth_test

import (
	"context"
	"encoding/base32"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlauth"
	"github.com/astronaut808/awg-forge/internal/sqldb"
	"github.com/pquerna/otp/totp"
)

const (
	testUsername = "admin"
	testPassword = "correct horse battery staple"
)

var testTOTPSecret = base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("test-only-totp-key-material"))

func TestControllerAuthLifecycle(t *testing.T) {
	service, closeDB := newControllerAuthService(t)
	defer closeDB()
	now := time.Unix(1_800_000_015, 0).UTC()
	confirmationCode := mustTOTPCode(t, now)
	enrollment, err := service.EnrollAdmin(context.Background(), " Admin ", testPassword, testTOTPSecret, confirmationCode, now)
	if err != nil {
		t.Fatal(err)
	}
	if enrollment.Username != testUsername || enrollment.UserID == "" || len(enrollment.RecoveryCodes) != 10 {
		t.Fatalf("enrollment = %#v", enrollment)
	}
	if _, err := service.Authenticate(context.Background(), testUsername, testPassword, confirmationCode, "192.0.2.10", now); !errors.Is(err, controlauth.ErrInvalidCredentials) {
		t.Fatalf("replayed enrollment TOTP error = %v", err)
	}

	loginAt := now.Add(30 * time.Second)
	authentication, err := service.Authenticate(context.Background(), testUsername, testPassword, mustTOTPCode(t, loginAt), "192.0.2.10", loginAt)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := service.ValidateSession(context.Background(), authentication.Token, loginAt)
	if err != nil {
		t.Fatal(err)
	}
	if principal.UserID != enrollment.UserID || principal.Username != testUsername || !principal.RecentAuth {
		t.Fatalf("principal = %#v", principal)
	}
	principal, err = service.ValidateSession(context.Background(), authentication.Token, loginAt.Add(6*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if principal.RecentAuth {
		t.Fatal("session remained recently authenticated past the recent-auth window")
	}
	if err := service.RevokeSession(context.Background(), authentication.Token, loginAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ValidateSession(context.Background(), authentication.Token, loginAt.Add(2*time.Minute)); !errors.Is(err, controlauth.ErrSessionNotFound) {
		t.Fatalf("revoked session error = %v", err)
	}

	recoveryAuth, err := service.AuthenticateRecovery(context.Background(), testUsername, testPassword, enrollment.RecoveryCodes[0], "192.0.2.10", loginAt)
	if err != nil {
		t.Fatal(err)
	}
	if recoveryAuth.Token == "" {
		t.Fatal("recovery authentication returned an empty token")
	}
	if _, err := service.AuthenticateRecovery(context.Background(), testUsername, testPassword, enrollment.RecoveryCodes[0], "192.0.2.10", loginAt); !errors.Is(err, controlauth.ErrInvalidCredentials) {
		t.Fatalf("reused recovery code error = %v", err)
	}

	if _, err := service.Authenticate(context.Background(), "missing", testPassword, "000000", "192.0.2.10", loginAt); !errors.Is(err, controlauth.ErrInvalidCredentials) {
		t.Fatalf("missing user error = %v", err)
	}
	if _, err := service.Authenticate(context.Background(), testUsername, "wrong password", "000000", "192.0.2.10", loginAt); !errors.Is(err, controlauth.ErrInvalidCredentials) {
		t.Fatalf("wrong password error = %v", err)
	}
	if _, err := service.EnrollAdmin(context.Background(), "second", testPassword, testTOTPSecret, mustTOTPCode(t, loginAt), loginAt); !errors.Is(err, controlauth.ErrAlreadyInitialized) {
		t.Fatalf("second enrollment error = %v", err)
	}
}

func TestConcurrentTOTPUseCreatesOneSession(t *testing.T) {
	service, closeDB := newControllerAuthService(t)
	defer closeDB()
	now := time.Unix(1_800_000_015, 0).UTC()
	if _, err := service.EnrollAdmin(context.Background(), testUsername, testPassword, testTOTPSecret, mustTOTPCode(t, now), now); err != nil {
		t.Fatal(err)
	}
	loginAt := now.Add(30 * time.Second)
	code := mustTOTPCode(t, loginAt)
	const attempts = 8
	var wg sync.WaitGroup
	results := make(chan error, attempts)
	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := service.Authenticate(context.Background(), testUsername, testPassword, code, "192.0.2.20", loginAt)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	var succeeded int
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, controlauth.ErrInvalidCredentials):
		case errors.Is(err, controlauth.ErrRateLimited):
		default:
			t.Fatalf("concurrent authentication error = %v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful concurrent authentications = %d, want 1", succeeded)
	}
}

func newControllerAuthService(t *testing.T) (*controlauth.Service, func()) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Config{
		ConfigDir:            dir,
		DatabaseMode:         sqldb.ModeSQLite,
		DatabasePath:         filepath.Join(dir, "awg-forge.db"),
		DatabaseBusyTimeout:  5 * time.Second,
		DatabaseQueryTimeout: 2 * time.Second,
		DatabaseMaxOpenConns: 1,
		DatabaseMaxIdleConns: 1,
	}
	db, err := sqldb.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(context.Background()); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	keys, err := controlauth.LoadOrCreateKeys(filepath.Join(dir, controlauth.KeyFileName), nil)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	service, err := controlauth.NewService(db, keys, controlauth.Options{
		PasswordParams: controlauth.PasswordParams{
			Memory:      64,
			Iterations:  1,
			Parallelism: 1,
			SaltLength:  16,
			KeyLength:   32,
		},
		MaxConcurrentHash: 4,
	})
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	return service, func() { _ = db.Close() }
}

func mustTOTPCode(t *testing.T, at time.Time) string {
	t.Helper()
	code, err := totp.GenerateCode(testTOTPSecret, at)
	if err != nil {
		t.Fatal(err)
	}
	return code
}
