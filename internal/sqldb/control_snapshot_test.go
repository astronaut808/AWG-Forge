package sqldb

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlpki"
)

const snapshotControllerID = "22222222-2222-4222-8222-222222222222"

func TestControllerRegistrySnapshotSupportedSchemas(t *testing.T) {
	ctx := context.Background()
	for _, version := range []int{5, 6, 7, 8} {
		t.Run("schema", func(t *testing.T) {
			db := openSnapshotSchema(t, version)
			path := snapshotPath(t, db)
			if err := VerifyControllerRegistrySnapshot(ctx, path, snapshotControllerID, "", nil); err != nil {
				t.Fatalf("schema %d: %v", version, err)
			}
		})
	}
}

func TestControllerRegistrySnapshotAcceptsExpiredRevokedHistory(t *testing.T) {
	ctx := context.Background()
	db := openSnapshotSchema(t, 8)
	now := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	material, _, err := controlpki.Generate(controlpki.Endpoint{BindIP: "127.0.0.1", Advertised: "127.0.0.1", Port: 8443}, now)
	if err != nil {
		t.Fatal(err)
	}
	issued, err := controlpki.IssueNodeCertificate(material, testNodeCSR(t), now)
	if err != nil {
		t.Fatal(err)
	}
	identity := NodeIdentity{ControllerID: snapshotControllerID, NodeID: "11111111-1111-4111-8111-111111111111", BindingEpoch: 1}
	generation := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := db.RegisterInitialNodeCertificate(ctx, identity, generation, issued, now); err != nil {
		t.Fatal(err)
	}
	if err := db.RevokeNodeCertificate(ctx, generation, issued.Serial, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.RevokeNodeBinding(ctx, identity, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(material.CACert)
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyControllerRegistrySnapshot(ctx, snapshotPath(t, db), snapshotControllerID, generation, ca); err != nil {
		t.Fatalf("expired revoked history: %v", err)
	}
}

func TestControllerRegistrySnapshotRejectsCorruption(t *testing.T) {
	ctx := context.Background()
	db := openSnapshotSchema(t, 8)
	if _, err := db.sql.ExecContext(ctx, "DROP INDEX control_node_certificates_one_initial_per_binding_idx"); err != nil {
		t.Fatal(err)
	}
	if err := VerifyControllerRegistrySnapshot(ctx, snapshotPath(t, db), snapshotControllerID, "", nil); err == nil {
		t.Fatal("schema constraint corruption accepted")
	}

	db = openSnapshotSchema(t, 8)
	now := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	material, _, err := controlpki.Generate(controlpki.Endpoint{BindIP: "127.0.0.1", Advertised: "127.0.0.1", Port: 8443}, now)
	if err != nil {
		t.Fatal(err)
	}
	issued, err := controlpki.IssueNodeCertificate(material, testNodeCSR(t), now)
	if err != nil {
		t.Fatal(err)
	}
	identity := NodeIdentity{ControllerID: snapshotControllerID, NodeID: "11111111-1111-4111-8111-111111111111", BindingEpoch: 1}
	generation := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := db.RegisterInitialNodeCertificate(ctx, identity, generation, issued, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, "UPDATE control_node_certificates SET certificate_sha256 = zeroblob(32)"); err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(material.CACert)
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyControllerRegistrySnapshot(ctx, snapshotPath(t, db), snapshotControllerID, generation, ca); err == nil {
		t.Fatal("certificate corruption accepted")
	}
}

func TestControllerRegistrySnapshotRejectsUnexpectedSchemaAndProvenance(t *testing.T) {
	ctx := context.Background()
	for _, change := range []struct {
		name    string
		version int
		sql     string
	}{
		{"unexpected node table in v5", 5, "CREATE TABLE control_node_bindings (node_id TEXT PRIMARY KEY)"},
		{"trigger", 8, "CREATE TRIGGER snapshot_test AFTER INSERT ON audit_events BEGIN SELECT 1; END"},
		{"sqlite lookalike trigger", 8, "CREATE TRIGGER sqlitex_trigger AFTER INSERT ON audit_events BEGIN SELECT 1; END"},
		{"wrong migration checksum", 8, "UPDATE schema_migrations SET checksum = 'wrong' WHERE version = 8"},
	} {
		t.Run(change.name, func(t *testing.T) {
			db := openSnapshotSchema(t, change.version)
			if _, err := db.sql.ExecContext(ctx, change.sql); err != nil {
				t.Fatal(err)
			}
			if err := VerifyControllerRegistrySnapshot(ctx, snapshotPath(t, db), snapshotControllerID, "", nil); err == nil {
				t.Fatal("invalid schema accepted")
			}
		})
	}
	db := openSnapshotSchema(t, 8)
	path := snapshotPath(t, db)
	archive, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = archive.Close() }()
	if _, err := archive.ExecContext(ctx, `CREATE TRIGGER restore_backdoor AFTER UPDATE OF disabled_at ON controller_users
WHEN NEW.disabled_at = '' BEGIN
UPDATE control_node_certificates SET revoked_at_unix_ms = NULL;
UPDATE control_node_bindings SET revoked_at_unix_ms = NULL;
END`); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.ExecContext(ctx, "PRAGMA writable_schema = ON"); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.ExecContext(ctx, "UPDATE sqlite_master SET name = 'sqlite_restore_backdoor', sql = replace(sql, 'restore_backdoor', 'sqlite_restore_backdoor') WHERE type = 'trigger' AND name = 'restore_backdoor'"); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.ExecContext(ctx, "PRAGMA writable_schema = OFF"); err != nil {
		t.Fatal(err)
	}
	if err := VerifyControllerRegistrySnapshot(ctx, path, snapshotControllerID, "", nil); err == nil {
		t.Fatal("forged sqlite trigger accepted")
	}
}

func TestControllerRegistrySnapshotPopulatedSchemasAndPreservesArchive(t *testing.T) {
	ctx := context.Background()
	for _, version := range []int{6, 7, 8} {
		t.Run("populated", func(t *testing.T) {
			db, material, generation, identity := populatedSnapshotSchema(t, version, version >= 7)
			path := snapshotPath(t, db)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := VerifyControllerRegistrySnapshot(ctx, path, identity.ControllerID, generation, snapshotCA(t, material)); err != nil {
				t.Fatalf("version %d: %v", version, err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if sha256.Sum256(before) != sha256.Sum256(after) {
				t.Fatal("verification changed archive")
			}
		})
	}
}

func TestControllerRegistrySnapshotAcceptsRebindHistory(t *testing.T) {
	ctx := context.Background()
	db, material, generation, identity := populatedSnapshotSchema(t, 8, false)
	now := time.Now().UTC()
	if err := db.RevokeNodeBinding(ctx, identity, now); err != nil {
		t.Fatal(err)
	}
	rebound, err := controlpki.IssueNodeCertificate(material, testNodeCSR(t), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.RebindRevokedNodeCertificate(ctx, identity, generation, rebound, now); err != nil {
		t.Fatal(err)
	}
	if err := VerifyControllerRegistrySnapshot(ctx, snapshotPath(t, db), identity.ControllerID, generation, snapshotCA(t, material)); err != nil {
		t.Fatalf("historical rebind: %v", err)
	}
}

func TestControllerRegistrySnapshotRejectsRegistryMismatch(t *testing.T) {
	ctx := context.Background()
	db, material, generation, identity := populatedSnapshotSchema(t, 8, true)
	path := snapshotPath(t, db)
	if err := VerifyControllerRegistrySnapshot(ctx, path, identity.ControllerID, generation, nil); err == nil {
		t.Fatal("nil CA accepted")
	}
	other, _, err := controlpki.Generate(controlpki.Endpoint{BindIP: "127.0.0.1", Advertised: "127.0.0.1", Port: 8443}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyControllerRegistrySnapshot(ctx, path, identity.ControllerID, generation, snapshotCA(t, other)); err == nil {
		t.Fatal("wrong CA accepted")
	}
	if err := VerifyControllerRegistrySnapshot(ctx, path, identity.ControllerID, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", snapshotCA(t, material)); err == nil {
		t.Fatal("wrong generation accepted")
	}
	if err := VerifyControllerRegistrySnapshot(ctx, path, "33333333-3333-4333-8333-333333333333", generation, snapshotCA(t, material)); err == nil {
		t.Fatal("wrong controller accepted")
	}
	if _, err := db.sql.ExecContext(ctx, "UPDATE control_node_certificates SET binding_epoch = 3"); err != nil {
		t.Fatal(err)
	}
	if err := VerifyControllerRegistrySnapshot(ctx, snapshotPath(t, db), identity.ControllerID, generation, snapshotCA(t, material)); err == nil {
		t.Fatal("binding epoch mismatch accepted")
	}
}

func TestControllerRegistrySnapshotRejectsCertificateAndRenewalCorruption(t *testing.T) {
	ctx := context.Background()
	for _, change := range []string{
		"UPDATE control_node_certificates SET certificate_der = zeroblob(1)",
		"UPDATE control_node_certificates SET serial = '0' WHERE predecessor_serial IS NULL",
		"UPDATE control_node_certificates SET public_key_sha256 = zeroblob(32)",
		"UPDATE control_node_certificates SET not_before_unix_ms = not_before_unix_ms + 1",
		"UPDATE control_node_certificates SET predecessor_issuer_generation = NULL WHERE predecessor_serial IS NOT NULL",
	} {
		t.Run("corrupt", func(t *testing.T) {
			db, material, generation, identity := populatedSnapshotSchema(t, 8, true)
			if _, err := db.sql.ExecContext(ctx, change); err != nil {
				t.Fatal(err)
			}
			if err := VerifyControllerRegistrySnapshot(ctx, snapshotPath(t, db), identity.ControllerID, generation, snapshotCA(t, material)); err == nil {
				t.Fatal("corrupt registry accepted")
			}
		})
	}
	// A self-cycle has non-increasing validity and is rejected.
	db, material, generation, identity := populatedSnapshotSchema(t, 8, true)
	if _, err := db.sql.ExecContext(ctx, `UPDATE control_node_certificates
	SET predecessor_serial = serial WHERE predecessor_serial IS NOT NULL`); err != nil {
		t.Fatal(err)
	}
	if err := VerifyControllerRegistrySnapshot(ctx, snapshotPath(t, db), identity.ControllerID, generation, snapshotCA(t, material)); err == nil {
		t.Fatal("cyclic renewal accepted")
	}
}

func TestControllerRegistrySnapshotRejectsSignedWrongCertificateProfile(t *testing.T) {
	ctx := context.Background()
	db, material, generation, identity := populatedSnapshotSchema(t, 8, false)
	if err := VerifyControllerRegistrySnapshot(ctx, snapshotPath(t, db), identity.ControllerID, generation, snapshotCA(t, material)); err != nil {
		t.Fatalf("happy control: %v", err)
	}
	der, certificate := signedWrongProfileCertificate(t, material)
	certificateHash := sha256.Sum256(der)
	publicKeyHash := sha256.Sum256(certificate.RawSubjectPublicKeyInfo)
	if _, err := db.sql.ExecContext(ctx, `UPDATE control_node_certificates
SET serial = ?, certificate_der = ?, certificate_sha256 = ?, public_key_sha256 = ?,
not_before_unix_ms = ?, not_after_unix_ms = ?`, certificate.SerialNumber.String(), der,
		certificateHash[:], publicKeyHash[:], certificate.NotBefore.UnixMilli(), certificate.NotAfter.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err := VerifyControllerRegistrySnapshot(ctx, snapshotPath(t, db), identity.ControllerID, generation, snapshotCA(t, material)); err == nil {
		t.Fatal("CA-signed serverAuth/SAN certificate accepted")
	}
}

func TestControllerRegistrySnapshotRejectsOrphanCertificate(t *testing.T) {
	ctx := context.Background()
	db, material, generation, identity := populatedSnapshotSchema(t, 8, false)
	path := snapshotPath(t, db)
	archive, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = archive.Close() }()
	if _, err := archive.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.ExecContext(ctx, "UPDATE control_node_certificates SET node_id = '33333333-3333-4333-8333-333333333333'"); err != nil {
		t.Fatal(err)
	}
	if err := VerifyControllerRegistrySnapshot(ctx, path, identity.ControllerID, generation, snapshotCA(t, material)); err == nil {
		t.Fatal("orphan certificate accepted")
	}
}

func TestControllerRegistrySnapshotRejectsMigrationAndAuthDDLManipulation(t *testing.T) {
	ctx := context.Background()
	for _, change := range []string{
		"DELETE FROM schema_migrations WHERE version = 4",
		"INSERT INTO schema_migrations(version, checksum, applied_at) VALUES (9, 'future', 'now')",
	} {
		t.Run("provenance", func(t *testing.T) {
			db := openSnapshotSchema(t, 8)
			if _, err := db.sql.ExecContext(ctx, change); err != nil {
				t.Fatal(err)
			}
			if err := VerifyControllerRegistrySnapshot(ctx, snapshotPath(t, db), snapshotControllerID, "", nil); err == nil {
				t.Fatal("bad provenance accepted")
			}
		})
	}
	db := openSnapshotSchema(t, 5)
	path := snapshotPath(t, db)
	archive, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = archive.Close() }()
	if _, err := archive.ExecContext(ctx, "PRAGMA writable_schema = ON"); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.ExecContext(ctx, `UPDATE sqlite_master SET sql = 'CREATE TABLE controller_users (id TEXT PRIMARY KEY /* singleton integer not null default 1 unique check (singleton = 1) */)' WHERE type = 'table' AND name = 'controller_users'`); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.ExecContext(ctx, "PRAGMA writable_schema = OFF"); err != nil {
		t.Fatal(err)
	}
	if err := VerifyControllerRegistrySnapshot(ctx, path, snapshotControllerID, "", nil); err == nil {
		t.Fatal("comment-spoofed auth DDL accepted")
	}
}

func populatedSnapshotSchema(t *testing.T, version int, renew bool) (*DB, controlpki.Material, string, NodeIdentity) {
	t.Helper()
	ctx := context.Background()
	db := openSnapshotSchema(t, version)
	now := time.Now().UTC().Add(-time.Hour)
	material, _, err := controlpki.Generate(controlpki.Endpoint{BindIP: "127.0.0.1", Advertised: "127.0.0.1", Port: 8443}, now)
	if err != nil {
		t.Fatal(err)
	}
	generation := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	identity := NodeIdentity{ControllerID: snapshotControllerID, NodeID: "11111111-1111-4111-8111-111111111111", BindingEpoch: 1}
	first, err := controlpki.IssueNodeCertificate(material, testNodeCSR(t), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.RegisterInitialNodeCertificate(ctx, identity, generation, first, now); err != nil {
		t.Fatal(err)
	}
	if renew {
		predecessor, err := x509.ParseCertificate(first.DER)
		if err != nil {
			t.Fatal(err)
		}
		at := predecessor.NotBefore.Add(predecessor.NotAfter.Sub(predecessor.NotBefore) * 2 / 3)
		successor, err := controlpki.IssueNodeCertificate(material, testNodeCSR(t), at)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.RenewNodeCertificate(ctx, identity.ControllerID, generation, predecessor, successor, at); err != nil {
			t.Fatal(err)
		}
	}
	return db, material, generation, identity
}

func snapshotCA(t *testing.T, material controlpki.Material) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(material.CACert)
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return ca
}

func signedWrongProfileCertificate(t *testing.T, material controlpki.Material) ([]byte, *x509.Certificate) {
	t.Helper()
	ca := snapshotCA(t, material)
	block, _ := pem.Decode(material.CAKey)
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	caKey, ok := key.(ed25519.PrivateKey)
	if !ok {
		t.Fatal("unexpected CA key type")
	}
	_, leafKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(987654321), Subject: pkix.Name{CommonName: "wrong profile"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSNames: []string{"control.example.test"},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, leafKey.Public(), caKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return der, certificate
}

func openSnapshotSchema(t *testing.T, version int) *DB {
	t.Helper()
	ctx := context.Background()
	db, err := Open(ctx, retentionTestConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.sql.ExecContext(ctx, `CREATE TABLE schema_migrations (
version INTEGER PRIMARY KEY, checksum TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadSQLiteMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:version] {
		if err := db.applyMigration(ctx, migration); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.CreateControllerUser(ctx, controllerAuthTestUser(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)), nil); err != nil {
		t.Fatal(err)
	}
	return db
}

func snapshotPath(t *testing.T, db *DB) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "snapshot.db")
	if err := db.SnapshotInto(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	return path
}
