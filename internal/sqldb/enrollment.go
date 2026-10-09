package sqldb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlpki"
	"github.com/google/uuid"
)

const (
	nodePresenceTTL                = 60 * time.Second
	enrollmentInvitationTTL        = 15 * time.Minute
	maxActiveEnrollmentInvitations = 512
)

var (
	ErrEnrollmentDenied   = errors.New("enrollment is not authorized")
	ErrEnrollmentConflict = errors.New("enrollment conflicts with an existing request")
	ErrEnrollmentExpired  = errors.New("enrollment has expired")
)

type EnrollmentInvitation struct {
	ID, ControllerID, NodeID string
	SecretDigest             [32]byte
	CreatedAt, ExpiresAt     time.Time
}

type Enrollment struct {
	ID, InvitationID, ControllerID, NodeID, RequestedName, Status, IssuerGeneration, Serial string
	CSRDER                                                                                  []byte
	CSRHash, ClaimDigest                                                                    [32]byte
	CreatedAt, ExpiresAt                                                                    time.Time
	CertificateDER                                                                          []byte
}

func (db *DB) CreateEnrollmentInvitation(ctx context.Context, invitation EnrollmentInvitation) error {
	if db == nil || db.sql == nil {
		return ErrDisabled
	}
	if !validEnrollmentInvitation(invitation) {
		return errors.New("invalid enrollment invitation")
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if !controllerEnrollmentEnabled(ctx, tx) {
		return ErrEnrollmentDenied
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM control_enrollment_invitations
WHERE expires_at_unix_ms <= ? AND NOT EXISTS (SELECT 1 FROM control_enrollments WHERE invitation_id = control_enrollment_invitations.id)`, invitation.CreatedAt.UTC().UnixMilli()); err != nil {
		return err
	}
	var active int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM control_enrollment_invitations WHERE expires_at_unix_ms > ?", invitation.CreatedAt.UTC().UnixMilli()).Scan(&active); err != nil {
		return err
	}
	if active >= maxActiveEnrollmentInvitations {
		return ErrEnrollmentConflict
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO control_enrollment_invitations
(id, controller_id, node_id, secret_digest, created_at_unix_ms, expires_at_unix_ms)
VALUES (?, ?, ?, ?, ?, ?)`, invitation.ID, invitation.ControllerID, invitation.NodeID,
		invitation.SecretDigest[:], invitation.CreatedAt.UTC().UnixMilli(), invitation.ExpiresAt.UTC().UnixMilli())
	if err != nil {
		return fmt.Errorf("create enrollment invitation: %w", err)
	}
	return tx.Commit()
}

// ClaimEnrollment consumes an invitation at most once. The same request can be
// retried safely; a different CSR or proof is never allowed to replace it.
func (db *DB) ClaimEnrollment(ctx context.Context, invitationID string, secretDigest [32]byte, enrollment Enrollment, now time.Time) (Enrollment, error) {
	if db == nil || db.sql == nil {
		return Enrollment{}, ErrDisabled
	}
	if !validUUID(invitationID) || !validClaimEnrollment(enrollment) || now.IsZero() {
		return Enrollment{}, ErrEnrollmentDenied
	}
	csr, csrHash, err := controlpki.ParseNodeCSR(enrollment.CSRDER)
	if err != nil || csr == nil || csrHash != enrollment.CSRHash {
		return Enrollment{}, ErrEnrollmentDenied
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return Enrollment{}, err
	}
	defer func() { _ = tx.Rollback() }()
	invitation, err := enrollmentInvitationInTx(ctx, tx, invitationID)
	if errors.Is(err, sql.ErrNoRows) {
		return Enrollment{}, ErrEnrollmentDenied
	}
	if err != nil {
		return Enrollment{}, err
	}
	if subtle.ConstantTimeCompare(invitation.SecretDigest[:], secretDigest[:]) != 1 {
		return Enrollment{}, ErrEnrollmentDenied
	}
	if now.UTC().Before(invitation.CreatedAt) || !now.UTC().Before(invitation.ExpiresAt) {
		return Enrollment{}, ErrEnrollmentExpired
	}
	if invitation.ControllerID != enrollment.ControllerID || enrollment.NodeID != "" && invitation.NodeID != enrollment.NodeID || !controllerEnrollmentEnabled(ctx, tx) {
		return Enrollment{}, ErrEnrollmentDenied
	}
	existing, err := enrollmentByInvitationInTx(ctx, tx, invitationID)
	if err == nil {
		if subtle.ConstantTimeCompare(existing.CSRHash[:], enrollment.CSRHash[:]) != 1 ||
			subtle.ConstantTimeCompare(existing.ClaimDigest[:], enrollment.ClaimDigest[:]) != 1 {
			return Enrollment{}, ErrEnrollmentConflict
		}
		if existing.Status == "rejected" {
			return Enrollment{}, ErrEnrollmentDenied
		}
		if err := tx.Commit(); err != nil {
			return Enrollment{}, err
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Enrollment{}, err
	}
	enrollment.InvitationID = invitationID
	enrollment.NodeID = invitation.NodeID
	enrollment.CreatedAt, enrollment.ExpiresAt = invitation.CreatedAt, invitation.ExpiresAt
	enrollment.Status, enrollment.IssuerGeneration, enrollment.Serial, enrollment.CertificateDER = "pending", "", "", nil
	_, err = tx.ExecContext(ctx, `INSERT INTO control_enrollments
(id, invitation_id, controller_id, node_id, requested_name, status, issuer_generation, serial,
 csr_der, csr_sha256, claim_digest, created_at_unix_ms, expires_at_unix_ms, certificate_der)
VALUES (?, ?, ?, ?, ?, 'pending', '', '', ?, ?, ?, ?, ?, '')`, enrollment.ID, invitationID,
		enrollment.ControllerID, enrollment.NodeID, enrollment.RequestedName, enrollment.CSRDER,
		enrollment.CSRHash[:], enrollment.ClaimDigest[:], enrollment.CreatedAt.UTC().UnixMilli(), enrollment.ExpiresAt.UTC().UnixMilli())
	if err != nil {
		return Enrollment{}, fmt.Errorf("record enrollment claim: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Enrollment{}, err
	}
	return cloneEnrollment(enrollment), nil
}

// FindEnrollment is a trusted approval lookup. Callers must not expose it to a node.
func (db *DB) FindEnrollment(ctx context.Context, id string) (Enrollment, error) {
	if db == nil || db.sql == nil {
		return Enrollment{}, ErrDisabled
	}
	if !validUUID(id) {
		return Enrollment{}, ErrEnrollmentDenied
	}
	enrollment, err := enrollmentByID(ctx, db.sql, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Enrollment{}, ErrEnrollmentDenied
	}
	return enrollment, err
}

// FindEnrollmentByInvitation is a trusted controller approval lookup.
func (db *DB) FindEnrollmentByInvitation(ctx context.Context, invitationID string) (Enrollment, error) {
	if db == nil || db.sql == nil {
		return Enrollment{}, ErrDisabled
	}
	if !validUUID(invitationID) {
		return Enrollment{}, ErrEnrollmentDenied
	}
	enrollment, err := enrollmentByInvitationInTx(ctx, db.sql, invitationID)
	if errors.Is(err, sql.ErrNoRows) {
		return Enrollment{}, ErrEnrollmentDenied
	}
	return enrollment, err
}

func (db *DB) EnrollmentStatus(ctx context.Context, id string, claimDigest [32]byte, now time.Time) (Enrollment, error) {
	if db == nil || db.sql == nil {
		return Enrollment{}, ErrDisabled
	}
	if !validUUID(id) || now.IsZero() {
		return Enrollment{}, ErrEnrollmentDenied
	}
	tx, err := db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Enrollment{}, err
	}
	defer func() { _ = tx.Rollback() }()
	enrollment, err := enrollmentByID(ctx, tx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Enrollment{}, ErrEnrollmentDenied
	}
	if err != nil {
		return Enrollment{}, err
	}
	if subtle.ConstantTimeCompare(enrollment.ClaimDigest[:], claimDigest[:]) != 1 || !controllerEnrollmentEnabled(ctx, tx) {
		return Enrollment{}, ErrEnrollmentDenied
	}
	if now.UTC().Before(enrollment.CreatedAt) || !now.UTC().Before(enrollment.ExpiresAt) {
		return Enrollment{}, ErrEnrollmentExpired
	}
	if enrollment.Status == "rejected" {
		if err := tx.Commit(); err != nil {
			return Enrollment{}, err
		}
		return enrollment, nil
	}
	if enrollment.Status == "approved" && !activeEnrollmentCertificate(ctx, tx, enrollment, now.UTC()) {
		return Enrollment{}, ErrEnrollmentDenied
	}
	if err := tx.Commit(); err != nil {
		return Enrollment{}, err
	}
	return enrollment, nil
}

func (db *DB) ApproveEnrollment(ctx context.Context, id string, expectedCSR [32]byte, issuerGeneration string, certificate controlpki.NodeCertificate, now time.Time) (Enrollment, error) {
	if db == nil || db.sql == nil {
		return Enrollment{}, ErrDisabled
	}
	if !validUUID(id) || !validIssuerGeneration(issuerGeneration) || now.IsZero() || !validEnrollmentCertificate(certificate, expectedCSR, now.UTC()) {
		return Enrollment{}, ErrEnrollmentDenied
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return Enrollment{}, err
	}
	defer func() { _ = tx.Rollback() }()
	enrollment, err := enrollmentByID(ctx, tx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Enrollment{}, ErrEnrollmentDenied
	}
	if err != nil {
		return Enrollment{}, err
	}
	if enrollment.CSRHash != expectedCSR {
		return Enrollment{}, ErrEnrollmentConflict
	}
	if now.UTC().Before(enrollment.CreatedAt) || !now.UTC().Before(enrollment.ExpiresAt) || !controllerEnrollmentEnabled(ctx, tx) || !enrollmentCertificateMatchesCSR(enrollment, certificate) {
		return Enrollment{}, ErrEnrollmentDenied
	}
	if enrollment.Status == "approved" {
		if enrollment.IssuerGeneration != issuerGeneration || !activeEnrollmentCertificate(ctx, tx, enrollment, now.UTC()) {
			return Enrollment{}, ErrEnrollmentDenied
		}
		if err := tx.Commit(); err != nil {
			return Enrollment{}, err
		}
		return enrollment, nil
	}
	if enrollment.Status != "pending" {
		return Enrollment{}, ErrEnrollmentDenied
	}
	if certificate.CSRHash != enrollment.CSRHash {
		return Enrollment{}, ErrEnrollmentConflict
	}
	var existing int
	err = tx.QueryRowContext(ctx, "SELECT 1 FROM control_node_bindings WHERE node_id = ?", enrollment.NodeID).Scan(&existing)
	if err == nil {
		return Enrollment{}, ErrEnrollmentConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Enrollment{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO control_node_bindings
(node_id, controller_id, binding_epoch) VALUES (?, ?, 1)`, enrollment.NodeID, enrollment.ControllerID); err != nil {
		return Enrollment{}, ErrEnrollmentConflict
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO control_node_certificates
(issuer_generation, serial, node_id, controller_id, binding_epoch,
 certificate_sha256, public_key_sha256, csr_sha256, certificate_der, not_before_unix_ms, not_after_unix_ms)
VALUES (?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?)`, issuerGeneration, certificate.Serial,
		enrollment.NodeID, enrollment.ControllerID, certificate.CertificateHash[:], certificate.PublicKeyHash[:],
		certificate.CSRHash[:], certificate.DER, certificate.NotBefore.UnixMilli(), certificate.NotAfter.UnixMilli()); err != nil {
		return Enrollment{}, ErrEnrollmentConflict
	}
	if _, err := tx.ExecContext(ctx, `UPDATE control_enrollments
SET status = 'approved', issuer_generation = ?, serial = ?, certificate_der = ?
WHERE id = ? AND status = 'pending'`, issuerGeneration, certificate.Serial, certificate.DER, enrollment.ID); err != nil {
		return Enrollment{}, err
	}
	enrollment.Status, enrollment.IssuerGeneration, enrollment.Serial = "approved", issuerGeneration, certificate.Serial
	enrollment.CertificateDER = bytes.Clone(certificate.DER)
	if err := tx.Commit(); err != nil {
		return Enrollment{}, err
	}
	return enrollment, nil
}

func (db *DB) RejectEnrollment(ctx context.Context, id string, now time.Time) error {
	if db == nil || db.sql == nil {
		return ErrDisabled
	}
	if !validUUID(id) || now.IsZero() {
		return ErrEnrollmentDenied
	}
	result, err := db.sql.ExecContext(ctx, "UPDATE control_enrollments SET status = 'rejected' WHERE id = ? AND status = 'pending'", id)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrEnrollmentDenied
	}
	return nil
}

// AcceptNodePresence persists a monotonic boot fence for a currently active node.
func (db *DB) AcceptNodePresence(ctx context.Context, identity NodeIdentity, stateEpoch, bootID string, bootSequence uint64, now time.Time) (string, error) {
	return db.AcceptAuthenticatedNodePresence(ctx, identity, stateEpoch, bootID, bootSequence, "", "", now)
}

// AcceptAuthenticatedNodePresence binds the live session to the exact
// registry-authorized client certificate that made the presence request.
// Empty certificate fields preserve compatibility for callers that cannot yet
// attest transport identity; such legacy sessions never count as connected.
func (db *DB) AcceptAuthenticatedNodePresence(ctx context.Context, identity NodeIdentity, stateEpoch, bootID string, bootSequence uint64, issuerGeneration, serial string, now time.Time) (string, error) {
	if db == nil || db.sql == nil {
		return "", ErrDisabled
	}
	if !validNodeIdentity(identity) || !validUUID(stateEpoch) || !validUUID(bootID) || bootSequence == 0 || bootSequence > math.MaxInt64 || now.IsZero() || (issuerGeneration != "" && !validIssuerGeneration(issuerGeneration)) || (issuerGeneration == "") != (serial == "") {
		return "", ErrNodeCertificateDenied
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	var controllerID string
	var epoch int64
	var revoked sql.NullInt64
	err = tx.QueryRowContext(ctx, "SELECT controller_id, binding_epoch, revoked_at_unix_ms FROM control_node_bindings WHERE node_id = ?", identity.NodeID).Scan(&controllerID, &epoch, &revoked)
	if errors.Is(err, sql.ErrNoRows) || revoked.Valid || controllerID != identity.ControllerID || epoch != int64(identity.BindingEpoch) {
		return "", ErrNodeCertificateDenied
	}
	if err != nil {
		return "", err
	}
	var storedState, storedBoot, sessionID string
	var storedSequence, expires int64
	err = tx.QueryRowContext(ctx, `SELECT state_epoch, boot_id, boot_sequence, session_id, expires_at_unix_ms
FROM control_node_presence WHERE node_id = ?`, identity.NodeID).Scan(&storedState, &storedBoot, &storedSequence, &sessionID, &expires)
	newSession := uuid.NewString()
	if err == nil {
		if storedState != stateEpoch || bootSequence < uint64(storedSequence) || bootSequence == uint64(storedSequence) && storedBoot != bootID {
			return "", ErrNodeCertificateDenied
		}
		if bootSequence == uint64(storedSequence) && now.UTC().UnixMilli() < expires {
			newSession = sessionID
		}
		if _, err := tx.ExecContext(ctx, `UPDATE control_node_presence
SET controller_id = ?, binding_epoch = ?, state_epoch = ?, boot_id = ?, boot_sequence = ?, session_id = ?, expires_at_unix_ms = ?, certificate_issuer_generation = NULLIF(?, ''), certificate_serial = NULLIF(?, '')
WHERE node_id = ?`, identity.ControllerID, identity.BindingEpoch, stateEpoch, bootID, bootSequence, newSession, now.UTC().Add(nodePresenceTTL).UnixMilli(), issuerGeneration, serial, identity.NodeID); err != nil {
			return "", err
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO control_node_presence
(node_id, controller_id, binding_epoch, state_epoch, boot_id, boot_sequence, session_id, expires_at_unix_ms, certificate_issuer_generation, certificate_serial)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''))`, identity.NodeID, identity.ControllerID, identity.BindingEpoch, stateEpoch, bootID, bootSequence, newSession, now.UTC().Add(nodePresenceTTL).UnixMilli(), issuerGeneration, serial); err != nil {
			return "", err
		}
	} else {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return newSession, nil
}

type enrollmentExecutor interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func enrollmentInvitationInTx(ctx context.Context, tx *sql.Tx, id string) (EnrollmentInvitation, error) {
	var invitation EnrollmentInvitation
	var secret []byte
	var created, expires int64
	err := tx.QueryRowContext(ctx, `SELECT id, controller_id, node_id, secret_digest, created_at_unix_ms, expires_at_unix_ms
FROM control_enrollment_invitations WHERE id = ?`, id).Scan(&invitation.ID, &invitation.ControllerID, &invitation.NodeID, &secret, &created, &expires)
	if err != nil {
		return EnrollmentInvitation{}, err
	}
	if len(secret) != sha256.Size {
		return EnrollmentInvitation{}, ErrEnrollmentDenied
	}
	copy(invitation.SecretDigest[:], secret)
	invitation.CreatedAt, invitation.ExpiresAt = time.UnixMilli(created).UTC(), time.UnixMilli(expires).UTC()
	return invitation, nil
}

func enrollmentByInvitationInTx(ctx context.Context, executor enrollmentExecutor, invitationID string) (Enrollment, error) {
	return scanEnrollment(ctx, executor, `SELECT id, invitation_id, controller_id, node_id, requested_name, status, issuer_generation, serial,
csr_der, csr_sha256, claim_digest, created_at_unix_ms, expires_at_unix_ms, certificate_der
FROM control_enrollments WHERE invitation_id = ?`, invitationID)
}

func enrollmentByID(ctx context.Context, executor enrollmentExecutor, id string) (Enrollment, error) {
	return scanEnrollment(ctx, executor, `SELECT id, invitation_id, controller_id, node_id, requested_name, status, issuer_generation, serial,
csr_der, csr_sha256, claim_digest, created_at_unix_ms, expires_at_unix_ms, certificate_der
FROM control_enrollments WHERE id = ?`, id)
}

func scanEnrollment(ctx context.Context, executor enrollmentExecutor, query, value string) (Enrollment, error) {
	var enrollment Enrollment
	var csrHash, claimDigest []byte
	var created, expires int64
	err := executor.QueryRowContext(ctx, query, value).Scan(&enrollment.ID, &enrollment.InvitationID, &enrollment.ControllerID, &enrollment.NodeID, &enrollment.RequestedName, &enrollment.Status, &enrollment.IssuerGeneration, &enrollment.Serial, &enrollment.CSRDER, &csrHash, &claimDigest, &created, &expires, &enrollment.CertificateDER)
	if err != nil {
		return Enrollment{}, err
	}
	if len(csrHash) != sha256.Size || len(claimDigest) != sha256.Size {
		return Enrollment{}, ErrEnrollmentDenied
	}
	copy(enrollment.CSRHash[:], csrHash)
	copy(enrollment.ClaimDigest[:], claimDigest)
	enrollment.CreatedAt, enrollment.ExpiresAt = time.UnixMilli(created).UTC(), time.UnixMilli(expires).UTC()
	return cloneEnrollment(enrollment), nil
}

func controllerEnrollmentEnabled(ctx context.Context, executor enrollmentExecutor) bool {
	var count int
	return executor.QueryRowContext(ctx, "SELECT count(*) FROM controller_users WHERE singleton = 1 AND disabled_at = ''").Scan(&count) == nil && count == 1
}

func activeEnrollmentCertificate(ctx context.Context, tx *sql.Tx, enrollment Enrollment, now time.Time) bool {
	var der, csrHash []byte
	var epoch, bindingEpoch, notBefore, notAfter int64
	var revoked, superseded, bindingRevoked sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT c.certificate_der, c.csr_sha256, c.binding_epoch, c.not_before_unix_ms, c.not_after_unix_ms,
c.revoked_at_unix_ms, c.superseded_at_unix_ms, b.revoked_at_unix_ms, b.binding_epoch FROM control_node_certificates c
JOIN control_node_bindings b ON b.node_id = c.node_id
WHERE c.issuer_generation = ? AND c.serial = ? AND c.node_id = ? AND c.controller_id = ?
AND b.controller_id = ?`, enrollment.IssuerGeneration, enrollment.Serial, enrollment.NodeID, enrollment.ControllerID, enrollment.ControllerID).Scan(&der, &csrHash, &epoch, &notBefore, &notAfter, &revoked, &superseded, &bindingRevoked, &bindingEpoch)
	return err == nil && epoch == 1 && bindingEpoch == epoch && len(csrHash) == sha256.Size && subtle.ConstantTimeCompare(csrHash, enrollment.CSRHash[:]) == 1 && bytes.Equal(der, enrollment.CertificateDER) && !revoked.Valid && (!superseded.Valid || now.UnixMilli() < superseded.Int64) && !bindingRevoked.Valid && now.UnixMilli() >= notBefore && now.UnixMilli() < notAfter
}

func validEnrollmentInvitation(invitation EnrollmentInvitation) bool {
	return validUUID(invitation.ID) && validUUID(invitation.ControllerID) && validUUID(invitation.NodeID) && !zeroDigest(invitation.SecretDigest) && !invitation.CreatedAt.IsZero() && !invitation.ExpiresAt.IsZero() && invitation.CreatedAt.Before(invitation.ExpiresAt) && invitation.ExpiresAt.Sub(invitation.CreatedAt) <= enrollmentInvitationTTL
}

func validClaimEnrollment(enrollment Enrollment) bool {
	return validUUID(enrollment.ID) && validUUID(enrollment.ControllerID) && enrollment.InvitationID == "" && !zeroDigest(enrollment.ClaimDigest) && validRequestedName(enrollment.RequestedName) && len(enrollment.CSRDER) > 0 && len(enrollment.CSRDER) <= controlpki.MaxCSRBytes
}

func validRequestedName(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func validEnrollmentCertificate(certificate controlpki.NodeCertificate, expectedCSR [32]byte, now time.Time) bool {
	if len(certificate.DER) == 0 || len(certificate.DER) > 16384 || certificate.CSRHash != expectedCSR || certificate.Serial == "" || now.Before(certificate.NotBefore) || !now.Before(certificate.NotAfter) {
		return false
	}
	parsed, err := x509.ParseCertificate(certificate.DER)
	if err != nil || parsed.SerialNumber == nil || parsed.SerialNumber.Sign() <= 0 || parsed.SerialNumber.String() != certificate.Serial || sha256.Sum256(certificate.DER) != certificate.CertificateHash || sha256.Sum256(parsed.RawSubjectPublicKeyInfo) != certificate.PublicKeyHash || !parsed.NotBefore.Equal(certificate.NotBefore) || !parsed.NotAfter.Equal(certificate.NotAfter) || parsed.IsCA || parsed.KeyUsage != x509.KeyUsageDigitalSignature || len(parsed.ExtKeyUsage) != 1 || parsed.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth {
		return false
	}
	return true
}

func enrollmentCertificateMatchesCSR(enrollment Enrollment, certificate controlpki.NodeCertificate) bool {
	csr, csrHash, err := controlpki.ParseNodeCSR(enrollment.CSRDER)
	if err != nil || csrHash != enrollment.CSRHash || certificate.CSRHash != enrollment.CSRHash {
		return false
	}
	issued, err := x509.ParseCertificate(certificate.DER)
	return err == nil && bytes.Equal(csr.RawSubjectPublicKeyInfo, issued.RawSubjectPublicKeyInfo)
}

func zeroDigest(value [32]byte) bool {
	return subtle.ConstantTimeCompare(value[:], make([]byte, sha256.Size)) == 1
}

func cloneEnrollment(value Enrollment) Enrollment {
	value.CSRDER = bytes.Clone(value.CSRDER)
	value.CertificateDER = bytes.Clone(value.CertificateDER)
	return value
}
