package sqldb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlauth"
)

func (db *DB) ControllerAuthInitialized(ctx context.Context) (bool, error) {
	var count int
	if err := db.sql.QueryRowContext(ctx, "SELECT count(*) FROM controller_users").Scan(&count); err != nil {
		return false, err
	}
	return count != 0, nil
}

// ResetControllerAuth removes only controller authentication state. It keeps
// operational history and every non-authentication table intact so an
// interrupted activation can roll back to standalone safely.
func (db *DB) ResetControllerAuth(ctx context.Context) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range []string{
		"DELETE FROM controller_sessions",
		"DELETE FROM controller_recovery_codes",
		"DELETE FROM controller_users",
		"DELETE FROM controller_auth_attempts",
		"UPDATE controller_auth_gate SET sequence = 0 WHERE singleton = 1",
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (db *DB) CreateControllerUser(ctx context.Context, user controlauth.User, recoveryDigests []controlauth.Digest) error {
	return db.createControllerUser(ctx, user, recoveryDigests, nil)
}

func (db *DB) CreateControllerUserWithSession(ctx context.Context, user controlauth.User, recoveryDigests []controlauth.Digest, session controlauth.Session) error {
	return db.createControllerUser(ctx, user, recoveryDigests, &session)
}

func (db *DB) createControllerUser(ctx context.Context, user controlauth.User, recoveryDigests []controlauth.Digest, session *controlauth.Session) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM controller_users").Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return controlauth.ErrAlreadyInitialized
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO controller_users (
    id, singleton, username, password_hash, totp_secret_ciphertext, totp_last_step,
    created_at, updated_at, disabled_at
) VALUES (?, 1, ?, ?, ?, ?, ?, ?, '')`,
		user.ID,
		user.Username,
		user.PasswordHash,
		user.TOTPSecretCiphertext,
		user.TOTPLastStep,
		formatTime(user.CreatedAt),
		formatTime(user.UpdatedAt),
	); err != nil {
		return fmt.Errorf("insert controller user: %w", err)
	}
	for _, digest := range recoveryDigests {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO controller_recovery_codes (user_id, code_digest, created_at, used_at)
VALUES (?, ?, ?, '')`, user.ID, digest[:], formatTime(user.CreatedAt)); err != nil {
			return fmt.Errorf("insert controller recovery code: %w", err)
		}
	}
	if session != nil {
		if session.UserID != user.ID {
			return errors.New("initial session user mismatch")
		}
		if err := insertControllerSession(ctx, tx, *session); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (db *DB) FindControllerAdmin(ctx context.Context) (controlauth.User, error) {
	var username string
	err := db.sql.QueryRowContext(ctx, "SELECT username FROM controller_users WHERE singleton = 1").Scan(&username)
	if errors.Is(err, sql.ErrNoRows) {
		return controlauth.User{}, controlauth.ErrInvalidCredentials
	}
	if err != nil {
		return controlauth.User{}, err
	}
	return db.FindControllerUser(ctx, username)
}

func (db *DB) FindControllerUser(ctx context.Context, username string) (controlauth.User, error) {
	var user controlauth.User
	var createdAt, updatedAt, disabledAt string
	err := db.sql.QueryRowContext(ctx, `
SELECT id, username, password_hash, totp_secret_ciphertext, totp_last_step,
       created_at, updated_at, disabled_at
FROM controller_users
WHERE username = ?`, username).Scan(
		&user.ID,
		&user.Username,
		&user.PasswordHash,
		&user.TOTPSecretCiphertext,
		&user.TOTPLastStep,
		&createdAt,
		&updatedAt,
		&disabledAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return controlauth.User{}, controlauth.ErrInvalidCredentials
	}
	if err != nil {
		return controlauth.User{}, err
	}
	if user.CreatedAt, err = parseStoredTime(createdAt); err != nil {
		return controlauth.User{}, err
	}
	if user.UpdatedAt, err = parseStoredTime(updatedAt); err != nil {
		return controlauth.User{}, err
	}
	if disabledAt != "" {
		if user.DisabledAt, err = parseStoredTime(disabledAt); err != nil {
			return controlauth.User{}, err
		}
	}
	return user, nil
}

func (db *DB) UseControllerTOTP(ctx context.Context, userID string, step int64, session controlauth.Session, attemptID int64) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
UPDATE controller_users
SET totp_last_step = ?, updated_at = ?
WHERE id = ? AND disabled_at = '' AND totp_last_step < ?`,
		step, formatTime(session.AuthenticatedAt), userID, step)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return controlauth.ErrCredentialConsumed
	}
	if err := insertControllerSession(ctx, tx, session); err != nil {
		return err
	}
	if err := finishControllerAuthAttempt(ctx, tx, attemptID, true, "", session.AuthenticatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

func (db *DB) UseControllerRecoveryCode(ctx context.Context, userID string, recoveryDigest controlauth.Digest, session controlauth.Session, attemptID int64) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
UPDATE controller_recovery_codes
SET used_at = ?
WHERE user_id = ? AND code_digest = ? AND used_at = ''
  AND EXISTS (
      SELECT 1 FROM controller_users
      WHERE controller_users.id = controller_recovery_codes.user_id
        AND controller_users.disabled_at = ''
  )`,
		formatTime(session.AuthenticatedAt), userID, recoveryDigest[:])
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return controlauth.ErrCredentialConsumed
	}
	if err := insertControllerSession(ctx, tx, session); err != nil {
		return err
	}
	if err := finishControllerAuthAttempt(ctx, tx, attemptID, true, "", session.AuthenticatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

func (db *DB) RotateControllerTOTP(ctx context.Context, oldDigest controlauth.Digest, userID string, step int64, session controlauth.Session, attemptID int64, now time.Time) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := consumeControllerSession(ctx, tx, oldDigest, userID, now); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE controller_users SET totp_last_step = ?, updated_at = ? WHERE id = ? AND disabled_at = '' AND totp_last_step < ?`, step, formatTime(now), userID, step)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return controlauth.ErrCredentialConsumed
	}
	if err := insertControllerSession(ctx, tx, session); err != nil {
		return err
	}
	if err := finishControllerAuthAttempt(ctx, tx, attemptID, true, "", now); err != nil {
		return err
	}
	return tx.Commit()
}

func (db *DB) RotateControllerRecoveryCode(ctx context.Context, oldDigest controlauth.Digest, userID string, recoveryDigest controlauth.Digest, session controlauth.Session, attemptID int64, now time.Time) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := consumeControllerSession(ctx, tx, oldDigest, userID, now); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE controller_recovery_codes SET used_at = ? WHERE user_id = ? AND code_digest = ? AND used_at = ''`, formatTime(now), userID, recoveryDigest[:])
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return controlauth.ErrCredentialConsumed
	}
	if err := insertControllerSession(ctx, tx, session); err != nil {
		return err
	}
	if err := finishControllerAuthAttempt(ctx, tx, attemptID, true, "", now); err != nil {
		return err
	}
	return tx.Commit()
}

func consumeControllerSession(ctx context.Context, tx *sql.Tx, digest controlauth.Digest, userID string, now time.Time) error {
	result, err := tx.ExecContext(ctx, `DELETE FROM controller_sessions WHERE token_digest = ? AND user_id = ? AND expires_at_unix_ms > ? AND revoked_at = ''`, digest[:], userID, now.UnixMilli())
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return controlauth.ErrSessionNotFound
	}
	return nil
}

func (db *DB) ReplaceControllerRecoveryCodes(ctx context.Context, sessionDigest controlauth.Digest, userID string, digests []controlauth.Digest, now time.Time, recentTTL time.Duration) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "UPDATE controller_auth_gate SET sequence = sequence + 1 WHERE singleton = 1"); err != nil {
		return err
	}
	var authenticatedAt, revokedAt string
	err = tx.QueryRowContext(ctx, `SELECT authenticated_at, revoked_at FROM controller_sessions WHERE token_digest = ? AND user_id = ? AND expires_at_unix_ms > ?`, sessionDigest[:], userID, now.UnixMilli()).Scan(&authenticatedAt, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return controlauth.ErrSessionNotFound
	}
	if err != nil {
		return err
	}
	authTime, err := parseStoredTime(authenticatedAt)
	if err != nil {
		return err
	}
	if revokedAt != "" || now.Before(authTime) || now.Sub(authTime) > recentTTL {
		return controlauth.ErrRecentAuthRequired
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM controller_recovery_codes WHERE user_id = ?", userID); err != nil {
		return err
	}
	for _, digest := range digests {
		if _, err := tx.ExecContext(ctx, "INSERT INTO controller_recovery_codes (user_id, code_digest, created_at, used_at) VALUES (?, ?, ?, '')", userID, digest[:], formatTime(now)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (db *DB) RecoverControllerAdmin(ctx context.Context, user controlauth.User, digests []controlauth.Digest) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE controller_users SET username = ?, password_hash = ?, totp_secret_ciphertext = ?, totp_last_step = -1, updated_at = ?, disabled_at = '' WHERE id = ? AND singleton = 1`, user.Username, user.PasswordHash, user.TOTPSecretCiphertext, formatTime(user.UpdatedAt), user.ID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return controlauth.ErrInvalidCredentials
	}
	for _, statement := range []string{"DELETE FROM controller_sessions", "DELETE FROM controller_recovery_codes", "DELETE FROM controller_auth_attempts", "UPDATE controller_auth_gate SET sequence = 0 WHERE singleton = 1"} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	for _, digest := range digests {
		if _, err := tx.ExecContext(ctx, "INSERT INTO controller_recovery_codes (user_id, code_digest, created_at, used_at) VALUES (?, ?, ?, '')", user.ID, digest[:], formatTime(user.UpdatedAt)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (db *DB) FindControllerSession(ctx context.Context, digest controlauth.Digest, now time.Time) (controlauth.Session, error) {
	var session controlauth.Session
	var storedDigest []byte
	var createdAt, expiresAt, authenticatedAt, revokedAt string
	err := db.sql.QueryRowContext(ctx, `
SELECT sessions.token_digest, sessions.user_id, users.username,
       sessions.created_at, sessions.expires_at, sessions.authenticated_at,
       sessions.revoked_at
FROM controller_sessions AS sessions
JOIN controller_users AS users ON users.id = sessions.user_id
	WHERE sessions.token_digest = ? AND sessions.expires_at_unix_ms > ?
	  AND users.disabled_at = ''`, digest[:], now.UTC().UnixMilli()).Scan(
		&storedDigest,
		&session.UserID,
		&session.Username,
		&createdAt,
		&expiresAt,
		&authenticatedAt,
		&revokedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return controlauth.Session{}, controlauth.ErrSessionNotFound
	}
	if err != nil {
		return controlauth.Session{}, err
	}
	if len(storedDigest) != len(session.Digest) {
		return controlauth.Session{}, errors.New("invalid controller session digest")
	}
	copy(session.Digest[:], storedDigest)
	if session.CreatedAt, err = parseStoredTime(createdAt); err != nil {
		return controlauth.Session{}, err
	}
	if session.ExpiresAt, err = parseStoredTime(expiresAt); err != nil {
		return controlauth.Session{}, err
	}
	if session.AuthenticatedAt, err = parseStoredTime(authenticatedAt); err != nil {
		return controlauth.Session{}, err
	}
	if revokedAt != "" {
		if session.RevokedAt, err = parseStoredTime(revokedAt); err != nil {
			return controlauth.Session{}, err
		}
	}
	if !session.RevokedAt.IsZero() || !now.UTC().Before(session.ExpiresAt) {
		return controlauth.Session{}, controlauth.ErrSessionNotFound
	}
	return session, nil
}

func (db *DB) RevokeControllerSession(ctx context.Context, digest controlauth.Digest, _ time.Time) error {
	_, err := db.sql.ExecContext(ctx, "DELETE FROM controller_sessions WHERE token_digest = ?", digest[:])
	return err
}

func (db *DB) ReserveControllerAuthAttempt(ctx context.Context, accountDigest, sourceDigest controlauth.Digest, now time.Time, policy controlauth.RateLimitPolicy) (int64, error) {
	if err := controlauth.ValidateRateLimitPolicy(policy); err != nil {
		return 0, err
	}
	if now.Unix() < 0 {
		return 0, errors.New("invalid controller auth attempt time")
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	// The first write serializes concurrent reservations before any count is read.
	if _, err := tx.ExecContext(ctx, `
UPDATE controller_auth_gate
SET sequence = sequence + 1
WHERE singleton = 1`); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM controller_auth_attempts WHERE attempted_at_unix_ms < ?", now.Add(-24*time.Hour).UnixMilli()); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM controller_sessions WHERE expires_at_unix_ms <= ? OR revoked_at != ''", now.UnixMilli()); err != nil {
		return 0, err
	}
	limited, err := controllerAuthRateLimited(ctx, tx, accountDigest, sourceDigest, now, policy)
	if err != nil {
		return 0, err
	}
	if limited {
		return 0, controlauth.ErrRateLimited
	}
	result, err := tx.ExecContext(ctx, `
INSERT INTO controller_auth_attempts (
    attempted_at_unix_ms, account_digest, source_digest, outcome, reason
) VALUES (?, ?, ?, 'pending', '')`, now.UnixMilli(), accountDigest[:], sourceDigest[:])
	if err != nil {
		return 0, err
	}
	attemptID, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return attemptID, nil
}

func (db *DB) FinishControllerAuthAttempt(ctx context.Context, attemptID int64, success bool, reason string, now time.Time) error {
	return finishControllerAuthAttempt(ctx, db.sql, attemptID, success, reason, now)
}

type authAttemptExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func finishControllerAuthAttempt(ctx context.Context, executor authAttemptExecutor, attemptID int64, success bool, reason string, now time.Time) error {
	if attemptID <= 0 || now.Unix() < 0 {
		return errors.New("invalid controller auth attempt completion")
	}
	outcome := "failed"
	if success {
		outcome = "success"
		reason = ""
	} else if reason != "invalid" && reason != "error" {
		return errors.New("invalid controller auth attempt reason")
	}
	result, err := executor.ExecContext(ctx, `
UPDATE controller_auth_attempts
SET outcome = ?, reason = ?, finished_at_unix_ms = ?
WHERE id = ? AND outcome = 'pending'`, outcome, reason, now.UnixMilli(), attemptID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("controller auth attempt is not pending")
	}
	return nil
}

func controllerAuthRateLimited(ctx context.Context, tx *sql.Tx, accountDigest, sourceDigest controlauth.Digest, now time.Time, policy controlauth.RateLimitPolicy) (bool, error) {
	checks := []struct {
		query string
		args  []any
		limit int
	}{
		{
			query: "SELECT count(*) FROM controller_auth_attempts WHERE attempted_at_unix_ms >= ? AND account_digest = ? AND outcome != 'success'",
			args:  []any{now.Add(-policy.AccountWindow).UnixMilli(), accountDigest[:]},
			limit: policy.AccountFailures,
		},
		{
			query: "SELECT count(*) FROM controller_auth_attempts WHERE attempted_at_unix_ms >= ? AND source_digest = ? AND outcome != 'success'",
			args:  []any{now.Add(-policy.SourceWindow).UnixMilli(), sourceDigest[:]},
			limit: policy.SourceFailures,
		},
		{
			query: "SELECT count(*) FROM controller_auth_attempts WHERE attempted_at_unix_ms >= ?",
			args:  []any{now.Add(-policy.GlobalWindow).UnixMilli()},
			limit: policy.GlobalAttempts,
		},
	}
	for _, check := range checks {
		var count int
		if err := tx.QueryRowContext(ctx, check.query, check.args...).Scan(&count); err != nil {
			return false, err
		}
		if count >= check.limit {
			return true, nil
		}
	}
	return false, nil
}

func insertControllerSession(ctx context.Context, tx *sql.Tx, session controlauth.Session) error {
	_, err := tx.ExecContext(ctx, `
	INSERT INTO controller_sessions (
	    token_digest, user_id, created_at, expires_at, expires_at_unix_ms,
	    authenticated_at, revoked_at
	) VALUES (?, ?, ?, ?, ?, ?, '')`,
		session.Digest[:],
		session.UserID,
		formatTime(session.CreatedAt),
		formatTime(session.ExpiresAt),
		session.ExpiresAt.UnixMilli(),
		formatTime(session.AuthenticatedAt),
	)
	if err != nil {
		return fmt.Errorf("insert controller session: %w", err)
	}
	return nil
}

func parseStoredTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse stored time: %w", err)
	}
	return parsed.UTC(), nil
}
