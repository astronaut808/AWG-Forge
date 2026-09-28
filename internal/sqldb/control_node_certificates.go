package sqldb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
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
