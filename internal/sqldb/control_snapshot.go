package sqldb

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"database/sql"
	"errors"
	"net/url"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlpki"
)

const controllerSnapshotQueryTimeout = 5 * time.Second

// VerifyControllerRegistrySnapshot verifies an archive read-only. It never migrates it.
func VerifyControllerRegistrySnapshot(ctx context.Context, path, controllerID, issuerGeneration string, ca *x509.Certificate) error {
	queryCtx, cancel := context.WithTimeout(ctx, controllerSnapshotQueryTimeout)
	defer cancel()
	if err := VerifyControllerSnapshot(queryCtx, path); err != nil {
		return err
	}
	if !validUUID(controllerID) {
		return errors.New("controller snapshot has an invalid controller identity")
	}
	uri := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return errors.New("open controller snapshot registry")
	}
	defer func() { _ = db.Close() }()
	version, migrations, err := verifySnapshotMigrations(queryCtx, db)
	if err != nil {
		return err
	}
	if err := verifySnapshotSchema(queryCtx, db, migrations[:version]); err != nil {
		return err
	}
	if err := verifySnapshotForeignKeys(queryCtx, db); err != nil {
		return err
	}
	if version == 5 {
		return nil
	}
	if err := verifySnapshotRegistry(queryCtx, db, version, controllerID, issuerGeneration, ca); err != nil {
		return err
	}
	if version >= 9 {
		return verifySnapshotEnrollment(queryCtx, db, controllerID, version)
	}
	return nil
}

func verifySnapshotMigrations(ctx context.Context, db *sql.DB) (int, []migration, error) {
	migrations, err := loadSQLiteMigrations()
	if err != nil {
		return 0, nil, errors.New("load controller snapshot migrations")
	}
	rows, err := db.QueryContext(ctx, "SELECT version, checksum FROM schema_migrations ORDER BY version")
	if err != nil {
		return 0, nil, errors.New("read controller snapshot migrations")
	}
	defer func() { _ = rows.Close() }()
	version := 0
	for rows.Next() {
		var got int
		var checksum string
		if err := rows.Scan(&got, &checksum); err != nil || got != version+1 || got > len(migrations) || checksum != migrations[got-1].checksum {
			return 0, nil, errors.New("controller snapshot migration provenance is invalid")
		}
		version = got
	}
	if err := rows.Err(); err != nil || version < 5 || version > CurrentSchemaVersion {
		return 0, nil, errors.New("controller snapshot schema is outside the supported range")
	}
	return version, migrations, nil
}

func verifySnapshotSchema(ctx context.Context, actual *sql.DB, migrations []migration) error {
	expected, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return errors.New("build controller snapshot schema")
	}
	defer func() { _ = expected.Close() }()
	expected.SetMaxOpenConns(1)
	if _, err := expected.ExecContext(ctx, `CREATE TABLE schema_migrations (
version INTEGER PRIMARY KEY, checksum TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		return errors.New("build controller snapshot schema")
	}
	for _, migration := range migrations {
		for _, statement := range splitStatements(migration.sql) {
			if _, err := expected.ExecContext(ctx, statement); err != nil {
				return errors.New("build controller snapshot schema")
			}
		}
	}
	want, err := snapshotSchemaObjects(ctx, expected)
	if err != nil {
		return err
	}
	got, err := snapshotSchemaObjects(ctx, actual)
	if err != nil || len(got) != len(want) {
		return errors.New("controller snapshot schema does not match its migration version")
	}
	for key, object := range want {
		if got[key] != object {
			return errors.New("controller snapshot schema does not match its migration version")
		}
	}
	return nil
}

type snapshotSchemaObject struct {
	table string
	sql   string
	null  bool
}

func snapshotSchemaObjects(ctx context.Context, db *sql.DB) (map[string]snapshotSchemaObject, error) {
	rows, err := db.QueryContext(ctx, `SELECT type, name, tbl_name, sql FROM sqlite_master ORDER BY type, name, tbl_name`)
	if err != nil {
		return nil, errors.New("read controller snapshot schema")
	}
	defer func() { _ = rows.Close() }()
	objects := make(map[string]snapshotSchemaObject)
	for rows.Next() {
		var kind, name, table string
		var sqlText sql.NullString
		if err := rows.Scan(&kind, &name, &table, &sqlText); err != nil {
			return nil, errors.New("read controller snapshot schema")
		}
		objects[kind+":"+name+":"+table] = snapshotSchemaObject{table: table, sql: normalizeSnapshotSQL(sqlText.String), null: !sqlText.Valid}
	}
	if rows.Err() != nil {
		return nil, errors.New("read controller snapshot schema")
	}
	return objects, nil
}

func verifySnapshotForeignKeys(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return errors.New("verify controller snapshot references")
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		return errors.New("controller snapshot has invalid references")
	}
	if rows.Err() != nil {
		return errors.New("verify controller snapshot references")
	}
	return nil
}

func verifySnapshotRegistry(ctx context.Context, db *sql.DB, version int, controllerID, issuerGeneration string, ca *x509.Certificate) error {
	var certificates, bindings int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM control_node_certificates").Scan(&certificates); err != nil {
		return errors.New("read controller snapshot registry")
	}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM control_node_bindings").Scan(&bindings); err != nil {
		return errors.New("read controller snapshot registry")
	}
	if certificates == 0 && bindings == 0 {
		return nil
	}
	if ca == nil || !validIssuerGeneration(issuerGeneration) {
		return errors.New("controller snapshot registry has no matching issuer")
	}
	if err := verifySnapshotBindings(ctx, db, controllerID); err != nil {
		return err
	}
	return verifySnapshotCertificates(ctx, db, version, controllerID, issuerGeneration, ca)
}

func verifySnapshotBindings(ctx context.Context, db *sql.DB, controllerID string) error {
	rows, err := db.QueryContext(ctx, "SELECT node_id, controller_id, binding_epoch, revoked_at_unix_ms FROM control_node_bindings")
	if err != nil {
		return errors.New("read controller snapshot bindings")
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var nodeID, stored string
		var epoch int64
		var revoked sql.NullInt64
		if err := rows.Scan(&nodeID, &stored, &epoch, &revoked); err != nil || !validUUID(nodeID) || stored != controllerID || epoch <= 0 {
			return errors.New("controller snapshot binding is invalid")
		}
	}
	if rows.Err() != nil {
		return errors.New("read controller snapshot bindings")
	}
	return nil
}

func verifySnapshotCertificates(ctx context.Context, db *sql.DB, version int, controllerID, issuerGeneration string, ca *x509.Certificate) error {
	query := `SELECT c.issuer_generation, c.serial, c.node_id, c.controller_id,
c.binding_epoch, c.certificate_sha256, c.public_key_sha256, c.csr_sha256,
c.certificate_der, c.not_before_unix_ms, c.not_after_unix_ms,
c.revoked_at_unix_ms, c.superseded_at_unix_ms,
b.controller_id, b.binding_epoch, b.revoked_at_unix_ms,
NULL, NULL, NULL, NULL, NULL, NULL, NULL
FROM control_node_certificates c
JOIN control_node_bindings b ON b.node_id = c.node_id`
	if version >= 7 {
		query = `SELECT c.issuer_generation, c.serial, c.node_id, c.controller_id,
c.binding_epoch, c.certificate_sha256, c.public_key_sha256, c.csr_sha256,
c.certificate_der, c.not_before_unix_ms, c.not_after_unix_ms,
c.revoked_at_unix_ms, c.superseded_at_unix_ms,
b.controller_id, b.binding_epoch, b.revoked_at_unix_ms,
c.predecessor_issuer_generation, c.predecessor_serial, p.node_id,
p.controller_id, p.binding_epoch, p.not_before_unix_ms, p.public_key_sha256
FROM control_node_certificates c
JOIN control_node_bindings b ON b.node_id = c.node_id
LEFT JOIN control_node_certificates p
ON p.issuer_generation = c.predecessor_issuer_generation AND p.serial = c.predecessor_serial`
	}
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return errors.New("read controller snapshot certificates")
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var r snapshotCertificate
		if err := rows.Scan(&r.issuer, &r.serial, &r.nodeID, &r.controllerID, &r.epoch, &r.certHash, &r.keyHash, &r.csrHash, &r.der, &r.notBefore, &r.notAfter, &r.revoked, &r.superseded, &r.bindingController, &r.bindingEpoch, &r.bindingRevoked, &r.predecessorIssuer, &r.predecessorSerial, &r.predecessorNode, &r.predecessorController, &r.predecessorEpoch, &r.predecessorNotBefore, &r.predecessorKeyHash); err != nil {
			return errors.New("read controller snapshot certificates")
		}
		if err := verifySnapshotCertificate(r, controllerID, issuerGeneration, ca); err != nil {
			return err
		}
	}
	if rows.Err() != nil {
		return errors.New("read controller snapshot certificates")
	}
	return nil
}

type snapshotCertificate struct {
	issuer, serial, nodeID, controllerID, bindingController                      string
	epoch, notBefore, notAfter, bindingEpoch                                     int64
	certHash, keyHash, csrHash, der                                              []byte
	revoked, superseded, bindingRevoked                                          sql.NullInt64
	predecessorIssuer, predecessorSerial, predecessorNode, predecessorController sql.NullString
	predecessorEpoch, predecessorNotBefore                                       sql.NullInt64
	predecessorKeyHash                                                           []byte
}

func verifySnapshotCertificate(r snapshotCertificate, controllerID, issuerGeneration string, ca *x509.Certificate) error {
	if r.issuer != issuerGeneration || r.controllerID != controllerID || r.bindingController != controllerID || !validUUID(r.nodeID) || r.epoch <= 0 || r.bindingEpoch < r.epoch || len(r.certHash) != sha256.Size || len(r.keyHash) != sha256.Size || len(r.csrHash) != sha256.Size || len(r.der) == 0 || len(r.der) > 16384 || r.notAfter <= r.notBefore {
		return errors.New("controller snapshot certificate record is invalid")
	}
	if err := verifySnapshotPredecessor(r, controllerID, issuerGeneration); err != nil {
		return err
	}
	certificate, err := x509.ParseCertificate(r.der)
	if err != nil {
		return errors.New("controller snapshot certificate is invalid")
	}
	certHash, keyHash := sha256.Sum256(r.der), sha256.Sum256(certificate.RawSubjectPublicKeyInfo)
	if certificate.SerialNumber.Sign() <= 0 || certificate.SerialNumber.String() != r.serial || !bytes.Equal(certHash[:], r.certHash) || !bytes.Equal(keyHash[:], r.keyHash) || certificate.NotBefore.UnixMilli() != r.notBefore || certificate.NotAfter.UnixMilli() != r.notAfter || certificate.CheckSignatureFrom(ca) != nil || !validSnapshotNodeCertificate(certificate, ca) {
		return errors.New("controller snapshot certificate is invalid")
	}
	return nil
}

func verifySnapshotPredecessor(r snapshotCertificate, controllerID, issuerGeneration string) error {
	if !r.predecessorIssuer.Valid && !r.predecessorSerial.Valid {
		return nil
	}
	if !r.predecessorIssuer.Valid || !r.predecessorSerial.Valid || r.predecessorIssuer.String != issuerGeneration || r.predecessorSerial.String == "" || !r.predecessorNode.Valid || !r.predecessorController.Valid || !r.predecessorEpoch.Valid || !r.predecessorNotBefore.Valid || len(r.predecessorKeyHash) != sha256.Size || r.predecessorNode.String != r.nodeID || r.predecessorController.String != controllerID || r.predecessorEpoch.Int64 != r.epoch || r.predecessorNotBefore.Int64 >= r.notBefore || bytes.Equal(r.predecessorKeyHash, r.keyHash) {
		return errors.New("controller snapshot renewal link is invalid")
	}
	return nil
}

func validSnapshotNodeCertificate(certificate, ca *x509.Certificate) bool {
	key, ok := certificate.PublicKey.(ed25519.PublicKey)
	return ok && len(key) == ed25519.PublicKeySize && certificate.PublicKeyAlgorithm == x509.Ed25519 && !certificate.IsCA && certificate.BasicConstraintsValid && certificate.KeyUsage == x509.KeyUsageDigitalSignature && len(certificate.ExtKeyUsage) == 1 && certificate.ExtKeyUsage[0] == x509.ExtKeyUsageClientAuth && len(certificate.UnknownExtKeyUsage) == 0 && len(certificate.UnhandledCriticalExtensions) == 0 && len(certificate.DNSNames) == 0 && len(certificate.IPAddresses) == 0 && len(certificate.EmailAddresses) == 0 && len(certificate.URIs) == 0 && !certificate.NotBefore.Before(ca.NotBefore) && !certificate.NotAfter.After(ca.NotAfter) && certificate.NotAfter.Sub(certificate.NotBefore) <= controlpki.NodeCertificateTTL+5*time.Minute
}

func verifySnapshotEnrollment(ctx context.Context, db *sql.DB, controllerID string, version int) error {
	invitationRows, err := db.QueryContext(ctx, `SELECT id, controller_id, node_id, secret_digest, created_at_unix_ms, expires_at_unix_ms
FROM control_enrollment_invitations`)
	if err != nil {
		return errors.New("read controller snapshot enrollment invitations")
	}
	defer func() { _ = invitationRows.Close() }()
	for invitationRows.Next() {
		var id, storedController, nodeID string
		var digest []byte
		var created, expires int64
		if err := invitationRows.Scan(&id, &storedController, &nodeID, &digest, &created, &expires); err != nil || !validUUID(id) || storedController != controllerID || !validUUID(nodeID) || len(digest) != sha256.Size || zeroBytes(digest) || created <= 0 || expires <= created || expires-created > enrollmentInvitationTTL.Milliseconds() {
			return errors.New("controller snapshot enrollment invitation is invalid")
		}
	}
	if invitationRows.Err() != nil {
		return errors.New("read controller snapshot enrollment invitations")
	}
	rows, err := db.QueryContext(ctx, `SELECT e.id, e.invitation_id, e.controller_id, e.node_id, e.requested_name, e.status,
e.issuer_generation, e.serial, e.csr_der, e.csr_sha256, e.claim_digest, e.created_at_unix_ms, e.expires_at_unix_ms,
e.certificate_der, i.controller_id, i.node_id, i.created_at_unix_ms, i.expires_at_unix_ms,
c.certificate_der, c.csr_sha256, c.binding_epoch, c.controller_id, b.controller_id, b.binding_epoch
FROM control_enrollments e JOIN control_enrollment_invitations i ON i.id = e.invitation_id
LEFT JOIN control_node_certificates c ON c.issuer_generation = e.issuer_generation AND c.serial = e.serial
LEFT JOIN control_node_bindings b ON b.node_id = e.node_id`)
	if err != nil {
		return errors.New("read controller snapshot enrollments")
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var enrollment Enrollment
		var csrHash, claimDigest []byte
		var created, expires, invitationCreated, invitationExpires int64
		var invitationController, invitationNode string
		var registryDER, registryCSR []byte
		var registryEpoch sql.NullInt64
		var registryController, bindingController sql.NullString
		var bindingEpoch sql.NullInt64
		if err := rows.Scan(&enrollment.ID, &enrollment.InvitationID, &enrollment.ControllerID, &enrollment.NodeID, &enrollment.RequestedName, &enrollment.Status, &enrollment.IssuerGeneration, &enrollment.Serial, &enrollment.CSRDER, &csrHash, &claimDigest, &created, &expires, &enrollment.CertificateDER, &invitationController, &invitationNode, &invitationCreated, &invitationExpires, &registryDER, &registryCSR, &registryEpoch, &registryController, &bindingController, &bindingEpoch); err != nil {
			return errors.New("read controller snapshot enrollments")
		}
		csr, parsedHash, csrErr := controlpki.ParseNodeCSR(enrollment.CSRDER)
		if csrErr != nil || !validUUID(enrollment.ID) || enrollment.ControllerID != controllerID || !validUUID(enrollment.NodeID) || !validRequestedName(enrollment.RequestedName) || len(csrHash) != sha256.Size || len(claimDigest) != sha256.Size || zeroBytes(claimDigest) || !bytes.Equal(parsedHash[:], csrHash) || invitationController != enrollment.ControllerID || invitationNode != enrollment.NodeID || created != invitationCreated || expires != invitationExpires || created <= 0 || expires <= created || expires-created > enrollmentInvitationTTL.Milliseconds() {
			return errors.New("controller snapshot enrollment is invalid")
		}
		if enrollment.Status == "approved" {
			certificate, certificateErr := x509.ParseCertificate(registryDER)
			if !validIssuerGeneration(enrollment.IssuerGeneration) || enrollment.Serial == "" || len(enrollment.CertificateDER) == 0 || certificateErr != nil || !bytes.Equal(certificate.RawSubjectPublicKeyInfo, csr.RawSubjectPublicKeyInfo) || !bytes.Equal(enrollment.CertificateDER, registryDER) || !bytes.Equal(csrHash, registryCSR) || !registryEpoch.Valid || registryEpoch.Int64 != 1 || !registryController.Valid || registryController.String != controllerID || !bindingController.Valid || bindingController.String != controllerID || !bindingEpoch.Valid || bindingEpoch.Int64 < 1 {
				return errors.New("controller snapshot approved enrollment link is invalid")
			}
		} else if (enrollment.Status != "pending" && enrollment.Status != "rejected") || enrollment.IssuerGeneration != "" || enrollment.Serial != "" || len(enrollment.CertificateDER) != 0 {
			return errors.New("controller snapshot enrollment status is invalid")
		}
	}
	if rows.Err() != nil {
		return errors.New("read controller snapshot enrollments")
	}
	presenceQuery := `SELECT p.node_id, p.controller_id, p.binding_epoch, p.state_epoch, p.boot_id, p.boot_sequence, p.session_id, p.expires_at_unix_ms,
b.controller_id, b.binding_epoch FROM control_node_presence p JOIN control_node_bindings b ON b.node_id = p.node_id`
	if version >= 10 {
		presenceQuery = `SELECT p.node_id, p.controller_id, p.binding_epoch, p.state_epoch, p.boot_id, p.boot_sequence, p.session_id, p.expires_at_unix_ms,
p.certificate_issuer_generation, p.certificate_serial, c.serial, b.controller_id, b.binding_epoch
FROM control_node_presence p JOIN control_node_bindings b ON b.node_id = p.node_id
LEFT JOIN control_node_certificates c ON c.issuer_generation = p.certificate_issuer_generation AND c.serial = p.certificate_serial
  AND c.node_id = p.node_id AND c.controller_id = p.controller_id AND c.binding_epoch = p.binding_epoch`
	}
	presence, err := db.QueryContext(ctx, presenceQuery)
	if err != nil {
		return errors.New("read controller snapshot node presence")
	}
	defer func() { _ = presence.Close() }()
	for presence.Next() {
		var nodeID, storedController, stateEpoch, bootID, sessionID, bindingController string
		var epoch, sequence, expires, bindingEpoch int64
		var issuer, serial, certificateSerial sql.NullString
		if version >= 10 {
			err = presence.Scan(&nodeID, &storedController, &epoch, &stateEpoch, &bootID, &sequence, &sessionID, &expires, &issuer, &serial, &certificateSerial, &bindingController, &bindingEpoch)
		} else {
			err = presence.Scan(&nodeID, &storedController, &epoch, &stateEpoch, &bootID, &sequence, &sessionID, &expires, &bindingController, &bindingEpoch)
		}
		if err != nil || !validUUID(nodeID) || storedController != controllerID || bindingController != controllerID || epoch <= 0 || epoch > bindingEpoch || !validUUID(stateEpoch) || !validUUID(bootID) || !validUUID(sessionID) || sequence <= 0 || expires <= 0 || version >= 10 && (issuer.Valid != serial.Valid || issuer.Valid && (!validIssuerGeneration(issuer.String) || serial.String == "" || !certificateSerial.Valid)) {
			return errors.New("controller snapshot node presence is invalid")
		}
	}
	if presence.Err() != nil {
		return errors.New("read controller snapshot node presence")
	}
	return nil
}

func zeroBytes(value []byte) bool {
	return subtle.ConstantTimeCompare(value, make([]byte, len(value))) == 1
}

func normalizeSnapshotSQL(value string) string {
	var out []byte
	space := false
	quote := byte(0)
	for i := 0; i < len(value); i++ {
		c := value[i]
		if quote != 0 {
			out = append(out, c)
			if c == quote {
				if i+1 < len(value) && value[i+1] == quote {
					out = append(out, value[i+1])
					i++
				} else {
					quote = 0
				}
			}
			continue
		}
		if c == '\'' || c == '"' {
			if space && len(out) > 0 && !snapshotSQLPunctuation(out[len(out)-1]) {
				out = append(out, ' ')
			}
			space, quote = false, c
			out = append(out, c)
			continue
		}
		if c == ' ' || c == '\n' || c == '\t' || c == '\r' {
			space = true
			continue
		}
		if snapshotSQLPunctuation(c) {
			if len(out) > 0 && out[len(out)-1] == ' ' {
				out = out[:len(out)-1]
			}
			out = append(out, c)
			space = false
			continue
		}
		if space && len(out) > 0 && !snapshotSQLPunctuation(out[len(out)-1]) {
			out = append(out, ' ')
		}
		space = false
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out = append(out, c)
	}
	return string(out)
}

func snapshotSQLPunctuation(c byte) bool { return c == '(' || c == ')' || c == ',' || c == ';' }
