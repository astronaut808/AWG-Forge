package sqldb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlpki"
	"github.com/google/uuid"
)

var (
	ErrNodeCertificateConflict = errors.New("node certificate issuance conflicts with existing binding")
	ErrNodeCertificateDenied   = errors.New("node certificate is not authorized")
)

type NodeIdentity struct {
	ControllerID string
	NodeID       string
	BindingEpoch uint64
}

// RegisterInitialNodeCertificate commits an approved node's public certificate
// before the caller may return it. An exact CSR retry returns the stored DER;
// a different CSR cannot replace a node's initial binding.
func (db *DB) RegisterInitialNodeCertificate(ctx context.Context, identity NodeIdentity, issuerGeneration string, certificate controlpki.NodeCertificate, now time.Time) ([]byte, error) {
	if db == nil || db.sql == nil {
		return nil, ErrDisabled
	}
	if !validNodeIdentity(identity) || identity.BindingEpoch != 1 || !validIssuerGeneration(issuerGeneration) ||
		now.IsZero() || !now.Before(certificate.NotAfter) || now.Before(certificate.NotBefore) ||
		len(certificate.DER) == 0 || len(certificate.DER) > 16384 {
		return nil, errors.New("invalid node certificate registration")
	}
	parsed, err := x509.ParseCertificate(certificate.DER)
	if err != nil || parsed.SerialNumber.String() != certificate.Serial ||
		sha256.Sum256(certificate.DER) != certificate.CertificateHash ||
		sha256.Sum256(parsed.RawSubjectPublicKeyInfo) != certificate.PublicKeyHash ||
		!parsed.NotBefore.Equal(certificate.NotBefore) || !parsed.NotAfter.Equal(certificate.NotAfter) ||
		parsed.IsCA || parsed.KeyUsage != x509.KeyUsageDigitalSignature ||
		len(parsed.ExtKeyUsage) != 1 || parsed.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth {
		return nil, errors.New("invalid issued node certificate")
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var controllerID string
	var epoch int64
	var revoked sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT controller_id, binding_epoch, revoked_at_unix_ms
FROM control_node_bindings WHERE node_id = ?`, identity.NodeID).Scan(&controllerID, &epoch, &revoked)
	if err == nil {
		if revoked.Valid || controllerID != identity.ControllerID || epoch != int64(identity.BindingEpoch) {
			return nil, ErrNodeCertificateConflict
		}
		var generation, serial, certificateControllerID string
		var certificateEpoch int64
		var existingDER, existingHash, existingKeyHash []byte
		var certRevoked, superseded sql.NullInt64
		var notBefore, notAfter int64
		err := tx.QueryRowContext(ctx, `SELECT issuer_generation, serial, controller_id, binding_epoch, certificate_der,
certificate_sha256, public_key_sha256, revoked_at_unix_ms, superseded_at_unix_ms,
not_before_unix_ms, not_after_unix_ms FROM control_node_certificates WHERE node_id = ? AND csr_sha256 = ?`,
			identity.NodeID, certificate.CSRHash[:]).Scan(&generation, &serial,
			&certificateControllerID, &certificateEpoch, &existingDER,
			&existingHash, &existingKeyHash, &certRevoked, &superseded, &notBefore, &notAfter)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNodeCertificateConflict
		}
		if err != nil {
			return nil, err
		}
		if generation != issuerGeneration || certificateControllerID != controllerID ||
			certificateEpoch != epoch || !bytes.Equal(existingKeyHash, certificate.PublicKeyHash[:]) {
			return nil, ErrNodeCertificateConflict
		}
		existingCertificate, err := x509.ParseCertificate(existingDER)
		fullHash := sha256.Sum256(existingDER)
		if err != nil || !bytes.Equal(existingHash, fullHash[:]) ||
			existingCertificate.SerialNumber.String() != serial ||
			sha256.Sum256(existingCertificate.RawSubjectPublicKeyInfo) != certificate.PublicKeyHash ||
			existingCertificate.NotBefore.UnixMilli() != notBefore ||
			existingCertificate.NotAfter.UnixMilli() != notAfter {
			return nil, ErrNodeCertificateDenied
		}
		if certRevoked.Valid || superseded.Valid && now.UnixMilli() >= superseded.Int64 ||
			now.UnixMilli() < notBefore || now.UnixMilli() >= notAfter {
			return nil, ErrNodeCertificateDenied
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return existingDER, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO control_node_bindings
(node_id, controller_id, binding_epoch) VALUES (?, ?, ?)`, identity.NodeID, identity.ControllerID, identity.BindingEpoch); err != nil {
		return nil, fmt.Errorf("register node binding: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO control_node_certificates
(issuer_generation, serial, node_id, controller_id, binding_epoch,
 certificate_sha256, public_key_sha256, csr_sha256, certificate_der,
 not_before_unix_ms, not_after_unix_ms)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		issuerGeneration, certificate.Serial, identity.NodeID, identity.ControllerID,
		identity.BindingEpoch, certificate.CertificateHash[:], certificate.PublicKeyHash[:],
		certificate.CSRHash[:], certificate.DER,
		certificate.NotBefore.UnixMilli(), certificate.NotAfter.UnixMilli()); err != nil {
		return nil, fmt.Errorf("record node certificate: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return bytes.Clone(certificate.DER), nil
}

// RenewNodeCertificate atomically publishes one successor for the exact active
// predecessor. The caller verifies its chain before signing; this transaction
// independently rechecks the registry, binding, and renewal window.
func (db *DB) RenewNodeCertificate(ctx context.Context, controllerID, issuerGeneration string, predecessor *x509.Certificate, successor controlpki.NodeCertificate, now time.Time) ([]byte, error) {
	der, _, err := db.renewNodeCertificate(ctx, controllerID, issuerGeneration, predecessor, &successor, successor.CSRHash, successor.PublicKeyHash, now)
	return der, err
}

// FindRenewedNodeCertificate recovers a previously committed exact CSR retry
// without asking the CA to sign again. An absent successor is not an error;
// predecessor authority is still checked inside this transaction.
func (db *DB) FindRenewedNodeCertificate(ctx context.Context, controllerID, issuerGeneration string, predecessor *x509.Certificate, csrHash, publicKeyHash [32]byte, now time.Time) ([]byte, bool, error) {
	return db.renewNodeCertificate(ctx, controllerID, issuerGeneration, predecessor, nil, csrHash, publicKeyHash, now)
}

func (db *DB) renewNodeCertificate(ctx context.Context, controllerID, issuerGeneration string, predecessor *x509.Certificate, successor *controlpki.NodeCertificate, csrHash, publicKeyHash [32]byte, now time.Time) ([]byte, bool, error) {
	if db == nil || db.sql == nil {
		return nil, false, ErrDisabled
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if !validUUID(controllerID) || !validIssuerGeneration(issuerGeneration) || predecessor == nil ||
		predecessor.SerialNumber == nil || predecessor.SerialNumber.Sign() <= 0 || now.IsZero() ||
		now.Before(predecessor.NotBefore) || !now.Before(predecessor.NotAfter) {
		return nil, false, ErrNodeCertificateDenied
	}
	var issued *x509.Certificate
	if successor != nil {
		var err error
		issued, err = x509.ParseCertificate(successor.DER)
		if err != nil || len(successor.DER) == 0 || len(successor.DER) > 16384 ||
			issued.SerialNumber.String() != successor.Serial ||
			sha256.Sum256(successor.DER) != successor.CertificateHash ||
			sha256.Sum256(issued.RawSubjectPublicKeyInfo) != successor.PublicKeyHash ||
			!issued.NotBefore.Equal(successor.NotBefore) || !issued.NotAfter.Equal(successor.NotAfter) ||
			issued.IsCA || issued.KeyUsage != x509.KeyUsageDigitalSignature ||
			len(issued.ExtKeyUsage) != 1 || issued.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth ||
			bytes.Equal(issued.RawSubjectPublicKeyInfo, predecessor.RawSubjectPublicKeyInfo) ||
			!issued.NotBefore.Before(now) || !now.Before(issued.NotAfter) {
			return nil, false, errors.New("invalid renewed node certificate")
		}
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	var nodeID, rowControllerID string
	var epoch, notBefore, notAfter int64
	var certHash, keyHash []byte
	err = tx.QueryRowContext(ctx, `SELECT c.node_id, c.controller_id, c.binding_epoch,
 c.certificate_sha256, c.public_key_sha256, c.not_before_unix_ms, c.not_after_unix_ms
 FROM control_node_certificates c JOIN control_node_bindings b ON b.node_id = c.node_id
 WHERE c.issuer_generation = ? AND c.serial = ? AND c.controller_id = ?
 AND b.controller_id = c.controller_id AND b.binding_epoch = c.binding_epoch
 AND c.revoked_at_unix_ms IS NULL AND b.revoked_at_unix_ms IS NULL
 AND (c.superseded_at_unix_ms IS NULL OR c.superseded_at_unix_ms > ?)`,
		issuerGeneration, predecessor.SerialNumber.String(), controllerID, now.UnixMilli()).
		Scan(&nodeID, &rowControllerID, &epoch, &certHash, &keyHash, &notBefore, &notAfter)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, ErrNodeCertificateDenied
	}
	if err != nil {
		return nil, false, err
	}
	predecessorHash := sha256.Sum256(predecessor.Raw)
	predecessorKeyHash := sha256.Sum256(predecessor.RawSubjectPublicKeyInfo)
	if !validUUID(nodeID) || rowControllerID != controllerID || epoch <= 0 ||
		!bytes.Equal(certHash, predecessorHash[:]) || !bytes.Equal(keyHash, predecessorKeyHash[:]) ||
		now.UnixMilli() < notBefore || now.UnixMilli() >= notAfter ||
		predecessor.NotBefore.UnixMilli() != notBefore || predecessor.NotAfter.UnixMilli() != notAfter {
		return nil, false, ErrNodeCertificateDenied
	}
	var existingDER, existingCSRHash, existingHash, existingKeyHash []byte
	var existingNodeID, existingControllerID string
	var existingEpoch int64
	err = tx.QueryRowContext(ctx, `SELECT certificate_der, csr_sha256, certificate_sha256,
 public_key_sha256, node_id, controller_id, binding_epoch
 FROM control_node_certificates
 WHERE predecessor_issuer_generation = ? AND predecessor_serial = ?`,
		issuerGeneration, predecessor.SerialNumber.String()).
		Scan(&existingDER, &existingCSRHash, &existingHash, &existingKeyHash,
			&existingNodeID, &existingControllerID, &existingEpoch)
	if err == nil {
		if !bytes.Equal(existingCSRHash, csrHash[:]) || existingNodeID != nodeID ||
			existingControllerID != controllerID || existingEpoch != epoch ||
			!bytes.Equal(existingKeyHash, publicKeyHash[:]) {
			return nil, false, ErrNodeCertificateConflict
		}
		stored, parseErr := x509.ParseCertificate(existingDER)
		storedHash := sha256.Sum256(existingDER)
		if parseErr != nil || !bytes.Equal(existingHash, storedHash[:]) ||
			sha256.Sum256(stored.RawSubjectPublicKeyInfo) != publicKeyHash {
			return nil, false, ErrNodeCertificateDenied
		}
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		return bytes.Clone(existingDER), true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	if successor == nil {
		return nil, false, nil
	}
	// The first renewal may begin at exactly two thirds of the predecessor's
	// actual validity interval. Retries above are allowed throughout overlap.
	window := predecessor.NotBefore.Add(predecessor.NotAfter.Sub(predecessor.NotBefore) * 2 / 3)
	if now.Before(window) {
		return nil, false, ErrNodeCertificateDenied
	}
	var used int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM control_node_certificates WHERE csr_sha256 = ?`, csrHash[:]).Scan(&used)
	if err == nil {
		return nil, false, ErrNodeCertificateConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	cutoff := now.Add(24 * time.Hour)
	if predecessor.NotAfter.Before(cutoff) {
		cutoff = predecessor.NotAfter
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO control_node_certificates
 (issuer_generation, serial, node_id, controller_id, binding_epoch,
 certificate_sha256, public_key_sha256, csr_sha256, certificate_der,
 not_before_unix_ms, not_after_unix_ms, predecessor_issuer_generation, predecessor_serial)
 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, issuerGeneration,
		successor.Serial, nodeID, controllerID, epoch, successor.CertificateHash[:],
		publicKeyHash[:], csrHash[:], successor.DER,
		successor.NotBefore.UnixMilli(), successor.NotAfter.UnixMilli(),
		issuerGeneration, predecessor.SerialNumber.String()); err != nil {
		return nil, false, fmt.Errorf("record renewed node certificate: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE control_node_certificates
 SET superseded_at_unix_ms = ? WHERE issuer_generation = ? AND serial = ?
 AND revoked_at_unix_ms IS NULL AND superseded_at_unix_ms IS NULL`,
		cutoff.UnixMilli(), issuerGeneration, predecessor.SerialNumber.String())
	if err != nil {
		return nil, false, err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return nil, false, ErrNodeCertificateDenied
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return bytes.Clone(successor.DER), true, nil
}

// FindActiveNodeCertificate resolves only the current binding and exact public
// certificate. It is called for every request, including keep-alive requests.
func (db *DB) FindActiveNodeCertificate(ctx context.Context, controllerID, issuerGeneration string, certificate *x509.Certificate, now time.Time) (NodeIdentity, error) {
	if db == nil || db.sql == nil || certificate == nil || now.IsZero() ||
		!validIssuerGeneration(issuerGeneration) || !validUUID(controllerID) ||
		certificate.SerialNumber == nil || certificate.SerialNumber.Sign() <= 0 ||
		now.Before(certificate.NotBefore) || !now.Before(certificate.NotAfter) {
		return NodeIdentity{}, ErrNodeCertificateDenied
	}
	var identity NodeIdentity
	var epoch int64
	var certHash, keyHash []byte
	var notBefore, notAfter int64
	err := db.sql.QueryRowContext(ctx, `
SELECT c.node_id, c.controller_id, c.binding_epoch, c.certificate_sha256,
       c.public_key_sha256, c.not_before_unix_ms, c.not_after_unix_ms
FROM control_node_certificates c
JOIN control_node_bindings b ON b.node_id = c.node_id
WHERE c.issuer_generation = ? AND c.serial = ?
  AND c.controller_id = ? AND b.controller_id = c.controller_id
  AND b.binding_epoch = c.binding_epoch
  AND c.revoked_at_unix_ms IS NULL AND b.revoked_at_unix_ms IS NULL
  AND (c.superseded_at_unix_ms IS NULL OR c.superseded_at_unix_ms > ?)`,
		issuerGeneration, certificate.SerialNumber.String(), controllerID, now.UnixMilli()).
		Scan(&identity.NodeID, &identity.ControllerID, &epoch, &certHash, &keyHash, &notBefore, &notAfter)
	if errors.Is(err, sql.ErrNoRows) {
		return NodeIdentity{}, ErrNodeCertificateDenied
	}
	if err != nil {
		return NodeIdentity{}, err
	}
	fullHash := sha256.Sum256(certificate.Raw)
	publicHash := sha256.Sum256(certificate.RawSubjectPublicKeyInfo)
	if !bytes.Equal(certHash, fullHash[:]) || !bytes.Equal(keyHash, publicHash[:]) ||
		epoch <= 0 || !validUUID(identity.NodeID) ||
		now.UnixMilli() < notBefore || now.UnixMilli() >= notAfter {
		return NodeIdentity{}, ErrNodeCertificateDenied
	}
	identity.BindingEpoch = uint64(epoch)
	return identity, nil
}

func (db *DB) RevokeNodeCertificate(ctx context.Context, issuerGeneration, serial string, now time.Time) error {
	if db == nil || db.sql == nil {
		return ErrDisabled
	}
	if !validIssuerGeneration(issuerGeneration) || serial == "" || now.IsZero() {
		return errors.New("invalid node certificate revocation")
	}
	result, err := db.sql.ExecContext(ctx, `UPDATE control_node_certificates
SET revoked_at_unix_ms = ? WHERE issuer_generation = ? AND serial = ? AND revoked_at_unix_ms IS NULL`,
		now.UnixMilli(), issuerGeneration, serial)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return ErrNodeCertificateDenied
	}
	return nil
}

// RevokeNodeBinding fences every certificate for the current binding at once.
func (db *DB) RevokeNodeBinding(ctx context.Context, identity NodeIdentity, now time.Time) error {
	if db == nil || db.sql == nil {
		return ErrDisabled
	}
	if !validNodeIdentity(identity) || now.IsZero() {
		return errors.New("invalid node binding revocation")
	}
	result, err := db.sql.ExecContext(ctx, `UPDATE control_node_bindings
SET revoked_at_unix_ms = ? WHERE node_id = ? AND controller_id = ?
AND binding_epoch = ? AND revoked_at_unix_ms IS NULL`,
		now.UnixMilli(), identity.NodeID, identity.ControllerID, identity.BindingEpoch)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return ErrNodeCertificateDenied
	}
	return nil
}

// RebindRevokedNodeCertificate atomically replaces a revoked binding on the
// same controller with the next epoch and one new initial certificate. The
// caller must separately establish local recovery authority; no network route
// calls this primitive. An exact retry recovers the committed public DER.
func (db *DB) RebindRevokedNodeCertificate(ctx context.Context, previous NodeIdentity, issuerGeneration string, certificate controlpki.NodeCertificate, now time.Time) ([]byte, error) {
	if db == nil || db.sql == nil {
		return nil, ErrDisabled
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validNodeIdentity(previous) || previous.BindingEpoch >= math.MaxInt64 ||
		!validIssuerGeneration(issuerGeneration) || now.IsZero() ||
		len(certificate.DER) == 0 || len(certificate.DER) > 16384 {
		return nil, ErrNodeCertificateDenied
	}
	issued, err := x509.ParseCertificate(certificate.DER)
	if err != nil || issued.SerialNumber == nil || issued.SerialNumber.String() != certificate.Serial ||
		sha256.Sum256(certificate.DER) != certificate.CertificateHash ||
		sha256.Sum256(issued.RawSubjectPublicKeyInfo) != certificate.PublicKeyHash ||
		!issued.NotBefore.Equal(certificate.NotBefore) || !issued.NotAfter.Equal(certificate.NotAfter) ||
		issued.IsCA || issued.KeyUsage != x509.KeyUsageDigitalSignature ||
		len(issued.ExtKeyUsage) != 1 || issued.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth ||
		now.Before(issued.NotBefore) || !now.Before(issued.NotAfter) {
		return nil, ErrNodeCertificateDenied
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var controllerID string
	var epoch int64
	var revoked sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT controller_id, binding_epoch, revoked_at_unix_ms
FROM control_node_bindings WHERE node_id = ?`, previous.NodeID).Scan(&controllerID, &epoch, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNodeCertificateDenied
	}
	if err != nil {
		return nil, err
	}
	if controllerID != previous.ControllerID {
		return nil, ErrNodeCertificateConflict
	}
	nextEpoch := int64(previous.BindingEpoch) + 1
	if epoch == nextEpoch && !revoked.Valid {
		storedDER, err := findReboundNodeCertificateInTx(ctx, tx, previous,
			issuerGeneration, nextEpoch, certificate.CSRHash, certificate.PublicKeyHash, now)
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return bytes.Clone(storedDER), nil
	}
	if epoch != int64(previous.BindingEpoch) {
		return nil, ErrNodeCertificateConflict
	}
	if !revoked.Valid || revoked.Int64 > now.UnixMilli() {
		return nil, ErrNodeCertificateDenied
	}
	var existing int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM control_node_certificates
 WHERE node_id = ? AND controller_id = ? AND binding_epoch = ? LIMIT 1`,
		previous.NodeID, previous.ControllerID, previous.BindingEpoch).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNodeCertificateDenied
	}
	if err != nil {
		return nil, err
	}
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM control_node_certificates
 WHERE node_id = ? AND public_key_sha256 = ? LIMIT 1`, previous.NodeID,
		certificate.PublicKeyHash[:]).Scan(&existing)
	if err == nil {
		return nil, ErrNodeCertificateDenied
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM control_node_certificates
 WHERE csr_sha256 = ? LIMIT 1`, certificate.CSRHash[:]).Scan(&existing)
	if err == nil {
		return nil, ErrNodeCertificateConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE control_node_bindings
 SET binding_epoch = ?, revoked_at_unix_ms = NULL
 WHERE node_id = ? AND controller_id = ? AND binding_epoch = ?
 AND revoked_at_unix_ms IS NOT NULL`, nextEpoch, previous.NodeID,
		previous.ControllerID, previous.BindingEpoch)
	if err != nil {
		return nil, err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return nil, ErrNodeCertificateDenied
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO control_node_certificates
 (issuer_generation, serial, node_id, controller_id, binding_epoch,
 certificate_sha256, public_key_sha256, csr_sha256, certificate_der,
 not_before_unix_ms, not_after_unix_ms)
 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, issuerGeneration,
		certificate.Serial, previous.NodeID, previous.ControllerID, nextEpoch,
		certificate.CertificateHash[:], certificate.PublicKeyHash[:], certificate.CSRHash[:],
		certificate.DER, certificate.NotBefore.UnixMilli(), certificate.NotAfter.UnixMilli()); err != nil {
		return nil, fmt.Errorf("record rebound node certificate: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return bytes.Clone(certificate.DER), nil
}

// FindReboundNodeCertificate recovers an exact committed retry before the CA
// is asked to sign again. It never authorizes a future rebind by itself.
func (db *DB) FindReboundNodeCertificate(ctx context.Context, previous NodeIdentity, issuerGeneration string, csrHash, publicKeyHash [32]byte, now time.Time) ([]byte, bool, error) {
	if db == nil || db.sql == nil {
		return nil, false, ErrDisabled
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if !validNodeIdentity(previous) || previous.BindingEpoch >= math.MaxInt64 ||
		!validIssuerGeneration(issuerGeneration) || now.IsZero() {
		return nil, false, ErrNodeCertificateDenied
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	var controllerID string
	var epoch int64
	var revoked sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT controller_id, binding_epoch, revoked_at_unix_ms
 FROM control_node_bindings WHERE node_id = ?`, previous.NodeID).Scan(&controllerID, &epoch, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, ErrNodeCertificateDenied
	}
	if err != nil {
		return nil, false, err
	}
	if controllerID != previous.ControllerID {
		return nil, false, ErrNodeCertificateConflict
	}
	if epoch == int64(previous.BindingEpoch) {
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		return nil, false, nil
	}
	if epoch != int64(previous.BindingEpoch)+1 || revoked.Valid {
		return nil, false, ErrNodeCertificateConflict
	}
	der, err := findReboundNodeCertificateInTx(ctx, tx, previous,
		issuerGeneration, epoch, csrHash, publicKeyHash, now)
	if err != nil {
		return nil, false, err
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return der, true, nil
}

func findReboundNodeCertificateInTx(ctx context.Context, tx *sql.Tx, previous NodeIdentity, issuerGeneration string, epoch int64, csrHash, publicKeyHash [32]byte, now time.Time) ([]byte, error) {
	var storedDER, storedCSRHash, storedHash, storedKeyHash []byte
	var serial string
	var certRevoked, superseded sql.NullInt64
	var notBefore, notAfter int64
	err := tx.QueryRowContext(ctx, `SELECT certificate_der, csr_sha256, certificate_sha256,
 public_key_sha256, serial, revoked_at_unix_ms, superseded_at_unix_ms,
 not_before_unix_ms, not_after_unix_ms
 FROM control_node_certificates WHERE node_id = ? AND controller_id = ?
 AND binding_epoch = ? AND issuer_generation = ?
 AND predecessor_issuer_generation IS NULL AND predecessor_serial IS NULL`,
		previous.NodeID, previous.ControllerID, epoch, issuerGeneration).
		Scan(&storedDER, &storedCSRHash, &storedHash, &storedKeyHash, &serial,
			&certRevoked, &superseded, &notBefore, &notAfter)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNodeCertificateConflict
	}
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(storedCSRHash, csrHash[:]) || !bytes.Equal(storedKeyHash, publicKeyHash[:]) {
		return nil, ErrNodeCertificateConflict
	}
	stored, parseErr := x509.ParseCertificate(storedDER)
	storedDigest := sha256.Sum256(storedDER)
	if parseErr != nil || certRevoked.Valid ||
		superseded.Valid && now.UnixMilli() >= superseded.Int64 ||
		!bytes.Equal(storedHash, storedDigest[:]) ||
		stored.SerialNumber == nil || stored.SerialNumber.String() != serial ||
		sha256.Sum256(stored.RawSubjectPublicKeyInfo) != publicKeyHash ||
		stored.NotBefore.UnixMilli() != notBefore || stored.NotAfter.UnixMilli() != notAfter ||
		now.UnixMilli() < notBefore || now.UnixMilli() >= notAfter {
		return nil, ErrNodeCertificateDenied
	}
	return bytes.Clone(storedDER), nil
}

func validNodeIdentity(identity NodeIdentity) bool {
	return validUUID(identity.ControllerID) && validUUID(identity.NodeID) && identity.BindingEpoch > 0
}

func validUUID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id.String() == value
}

func validIssuerGeneration(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return false
		}
	}
	return true
}
