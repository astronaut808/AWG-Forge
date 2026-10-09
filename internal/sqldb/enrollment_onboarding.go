package sqldb

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// EnrollmentOnboarding is the controller-only, redacted enrollment view used
// while a browser waits for a newly invited node to join.
type EnrollmentOnboarding struct {
	InvitationID, EnrollmentID, ControllerID, NodeID, Status string
	ExpiresAt                                                time.Time
	BindingEpoch                                             uint64
	Connected                                                bool
}

// EnrollmentOnboardingStatus reads the invitation and its live presence in one
// registry transaction. The caller supplies the current controller identity and
// CA generation so restored, revoked, expired, superseded, or old-CA registry
// entries cannot make a node appear connected.
func (db *DB) EnrollmentOnboardingStatus(ctx context.Context, invitationID, controllerID, issuerGeneration string, now time.Time) (EnrollmentOnboarding, error) {
	if db == nil || db.sql == nil {
		return EnrollmentOnboarding{}, ErrDisabled
	}
	if !validUUID(invitationID) || !validUUID(controllerID) || !validIssuerGeneration(issuerGeneration) || now.IsZero() {
		return EnrollmentOnboarding{}, ErrEnrollmentDenied
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return EnrollmentOnboarding{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if !controllerEnrollmentEnabled(ctx, tx) {
		return EnrollmentOnboarding{}, ErrEnrollmentDenied
	}
	var result EnrollmentOnboarding
	var expires int64
	err = tx.QueryRowContext(ctx, `SELECT id, controller_id, node_id, expires_at_unix_ms
FROM control_enrollment_invitations WHERE id = ?`, invitationID).
		Scan(&result.InvitationID, &result.ControllerID, &result.NodeID, &expires)
	if errors.Is(err, sql.ErrNoRows) || result.ControllerID != controllerID {
		return EnrollmentOnboarding{}, ErrEnrollmentDenied
	}
	if err != nil {
		return EnrollmentOnboarding{}, err
	}
	result.ExpiresAt = time.UnixMilli(expires).UTC()
	result.Status = "waiting"
	if !now.UTC().Before(result.ExpiresAt) {
		result.Status = "expired"
		if err := tx.Commit(); err != nil {
			return EnrollmentOnboarding{}, err
		}
		return result, nil
	}
	var enrollmentStatus, enrollmentController, enrollmentNode string
	err = tx.QueryRowContext(ctx, `SELECT id, status, controller_id, node_id FROM control_enrollments WHERE invitation_id = ?`, invitationID).
		Scan(&result.EnrollmentID, &enrollmentStatus, &enrollmentController, &enrollmentNode)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return EnrollmentOnboarding{}, err
		}
		return result, nil
	}
	if err != nil {
		return EnrollmentOnboarding{}, err
	}
	if enrollmentController != controllerID || enrollmentNode != result.NodeID {
		return EnrollmentOnboarding{}, ErrEnrollmentDenied
	}
	if enrollmentStatus != "pending" && enrollmentStatus != "approved" && enrollmentStatus != "rejected" {
		return EnrollmentOnboarding{}, ErrEnrollmentDenied
	}
	result.Status = enrollmentStatus
	if enrollmentStatus == "approved" {
		var epoch int64
		err = tx.QueryRowContext(ctx, `SELECT b.binding_epoch
FROM control_node_presence p
JOIN control_node_bindings b ON b.node_id = p.node_id
WHERE p.node_id = ? AND p.controller_id = ? AND p.binding_epoch = 1 AND p.binding_epoch = b.binding_epoch
  AND b.controller_id = ? AND b.revoked_at_unix_ms IS NULL
  AND p.expires_at_unix_ms > ?
  AND EXISTS (
    SELECT 1 FROM control_node_certificates c
    WHERE c.node_id = p.node_id AND c.controller_id = p.controller_id
      AND c.binding_epoch = p.binding_epoch AND c.issuer_generation = p.certificate_issuer_generation
      AND c.serial = p.certificate_serial AND c.issuer_generation = ?
      AND c.revoked_at_unix_ms IS NULL
      AND (c.superseded_at_unix_ms IS NULL OR c.superseded_at_unix_ms > ?)
      AND c.not_before_unix_ms <= ? AND c.not_after_unix_ms > ?
  )`, result.NodeID, controllerID, controllerID, now.UTC().UnixMilli(), issuerGeneration,
			now.UTC().UnixMilli(), now.UTC().UnixMilli(), now.UTC().UnixMilli()).Scan(&epoch)
		if err == nil && epoch > 0 {
			result.BindingEpoch = uint64(epoch)
			result.Connected = true
		} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return EnrollmentOnboarding{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return EnrollmentOnboarding{}, err
	}
	return result, nil
}
