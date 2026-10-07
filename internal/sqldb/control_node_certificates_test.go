package sqldb

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlpki"
)

func TestNodeCertificateRegistryDurabilityConflictAndRevocation(t *testing.T) {
	ctx := context.Background()
	db := openControllerAuthTestDB(t)
	now := time.Now().UTC()
	material, _, err := controlpki.Generate(controlpki.Endpoint{BindIP: "127.0.0.1", Advertised: "127.0.0.1", Port: 8443}, now)
	if err != nil {
		t.Fatal(err)
	}
	csr := testNodeCSR(t)
	certificate, err := controlpki.IssueNodeCertificate(material, csr, now)
	if err != nil {
		t.Fatal(err)
	}
	identity := NodeIdentity{
		ControllerID: "22222222-2222-4222-8222-222222222222",
		NodeID:       "11111111-1111-4111-8111-111111111111", BindingEpoch: 1,
	}
	generation := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	stored, err := db.RegisterInitialNodeCertificate(ctx, identity, generation, certificate, now)
	if err != nil || !bytes.Equal(stored, certificate.DER) {
		t.Fatalf("first registration: %v", err)
	}
	retryCertificate, err := controlpki.IssueNodeCertificate(material, csr, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	retry, err := db.RegisterInitialNodeCertificate(ctx, identity, generation, retryCertificate, now.Add(time.Second))
	if err != nil || !bytes.Equal(retry, certificate.DER) {
		t.Fatalf("exact CSR retry did not return original DER: %v", err)
	}
	different, err := controlpki.IssueNodeCertificate(material, testNodeCSR(t), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.RegisterInitialNodeCertificate(ctx, identity, generation, different, now); !errors.Is(err, ErrNodeCertificateConflict) {
		t.Fatalf("different CSR error = %v", err)
	}
	parsed, err := x509.ParseCertificate(stored)
	if err != nil {
		t.Fatal(err)
	}
	active, err := db.FindActiveNodeCertificate(ctx, identity.ControllerID, generation, parsed, now)
	if err != nil || active != identity {
		t.Fatalf("active binding = %+v, %v", active, err)
	}
	forged := *parsed
	forged.Raw = bytes.Clone(parsed.Raw)
	forged.Raw[len(forged.Raw)-1] ^= 1
	if _, err := db.FindActiveNodeCertificate(ctx, identity.ControllerID, generation, &forged, now); !errors.Is(err, ErrNodeCertificateDenied) {
		t.Fatalf("same-serial different fingerprint error = %v", err)
	}
	other := identity
	other.NodeID = "33333333-3333-4333-8333-333333333333"
	if _, err := db.RegisterInitialNodeCertificate(ctx, other, generation, certificate, now); err == nil {
		t.Fatal("same CSR enrolled a second node")
	}
	var bindings int
	if err := db.sql.QueryRowContext(ctx, "SELECT count(*) FROM control_node_bindings").Scan(&bindings); err != nil || bindings != 1 {
		t.Fatalf("failed transaction left a binding: %d, %v", bindings, err)
	}
	if err := db.RevokeNodeCertificate(ctx, generation, certificate.Serial, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FindActiveNodeCertificate(ctx, identity.ControllerID, generation, parsed, now.Add(time.Minute)); !errors.Is(err, ErrNodeCertificateDenied) {
		t.Fatalf("revoked certificate error = %v", err)
	}
	if _, err := db.RegisterInitialNodeCertificate(ctx, identity, generation, retryCertificate, now.Add(time.Minute)); !errors.Is(err, ErrNodeCertificateDenied) {
		t.Fatalf("revoked retry error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FindActiveNodeCertificate(ctx, identity.ControllerID, generation, parsed, now); err == nil {
		t.Fatal("closed database authorized a certificate")
	}
}

func TestControlNodeCertificateMigrationFromVersionFive(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, retentionTestConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	migrations, err := loadSQLiteMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, `CREATE TABLE schema_migrations (
version INTEGER PRIMARY KEY, checksum TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:5] {
		if err := db.applyMigration(ctx, migration); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.sql.ExecContext(ctx, "INSERT INTO audit_events (time, level, event) VALUES (?, 'info', 'preserved')", formatTime(time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, db, "SELECT count(*) FROM audit_events WHERE event = 'preserved'"); got != 1 {
		t.Fatalf("existing history lost: %d", got)
	}
	if got := countRows(t, db, "SELECT count(*) FROM control_node_bindings"); got != 0 {
		t.Fatalf("unexpected node bindings after migration: %d", got)
	}
}

func TestControlNodeBindingAndSupersessionFenceOldCertificate(t *testing.T) {
	ctx := context.Background()
	db := openControllerAuthTestDB(t)
	now := time.Now().UTC()
	material, _, err := controlpki.Generate(controlpki.Endpoint{BindIP: "127.0.0.1", Advertised: "127.0.0.1", Port: 8443}, now)
	if err != nil {
		t.Fatal(err)
	}
	issued, err := controlpki.IssueNodeCertificate(material, testNodeCSR(t), now)
	if err != nil {
		t.Fatal(err)
	}
	identity := NodeIdentity{ControllerID: "22222222-2222-4222-8222-222222222222", NodeID: "11111111-1111-4111-8111-111111111111", BindingEpoch: 1}
	generation := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := db.RegisterInitialNodeCertificate(ctx, identity, generation, issued, now); err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(issued.DER)
	if err != nil {
		t.Fatal(err)
	}
	cutoff := now.Add(time.Minute)
	if _, err := db.sql.ExecContext(ctx, `UPDATE control_node_certificates SET superseded_at_unix_ms = ?
WHERE issuer_generation = ? AND serial = ?`, cutoff.UnixMilli(), generation, issued.Serial); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FindActiveNodeCertificate(ctx, identity.ControllerID, generation, cert, cutoff.Add(-time.Second)); err != nil {
		t.Fatalf("certificate denied before overlap cutoff: %v", err)
	}
	if _, err := db.FindActiveNodeCertificate(ctx, identity.ControllerID, generation, cert, cutoff); !errors.Is(err, ErrNodeCertificateDenied) {
		t.Fatalf("certificate accepted at overlap cutoff: %v", err)
	}
	if _, err := db.sql.ExecContext(ctx, `UPDATE control_node_certificates SET binding_epoch = 2
WHERE issuer_generation = ? AND serial = ?`, generation, issued.Serial); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RegisterInitialNodeCertificate(ctx, identity, generation, issued, now); !errors.Is(err, ErrNodeCertificateConflict) {
		t.Fatalf("old CSR accepted for different certificate epoch: %v", err)
	}
	if _, err := db.sql.ExecContext(ctx, `UPDATE control_node_certificates SET binding_epoch = 1
WHERE issuer_generation = ? AND serial = ?`, generation, issued.Serial); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, "UPDATE control_node_bindings SET binding_epoch = 2 WHERE node_id = ?", identity.NodeID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FindActiveNodeCertificate(ctx, identity.ControllerID, generation, cert, now); !errors.Is(err, ErrNodeCertificateDenied) {
		t.Fatalf("old certificate accepted after rebind epoch change: %v", err)
	}
	if _, err := db.sql.ExecContext(ctx, "UPDATE control_node_bindings SET binding_epoch = 1 WHERE node_id = ?", identity.NodeID); err != nil {
		t.Fatal(err)
	}
	if err := db.RevokeNodeBinding(ctx, identity, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FindActiveNodeCertificate(ctx, identity.ControllerID, generation, cert, now); !errors.Is(err, ErrNodeCertificateDenied) {
		t.Fatalf("revoked binding accepted: %v", err)
	}
	if err := db.RevokeNodeBinding(ctx, identity, now); !errors.Is(err, ErrNodeCertificateDenied) {
		t.Fatalf("double binding revocation = %v", err)
	}
}

func TestNodeCertificateConcurrentRetryAndRevocationSurviveReopen(t *testing.T) {
	ctx := context.Background()
	cfg := retentionTestConfig(t)
	first, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()
	if err := first.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	second, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()
	now := time.Now().UTC()
	material, _, err := controlpki.Generate(controlpki.Endpoint{BindIP: "127.0.0.1", Advertised: "127.0.0.1", Port: 8443}, now)
	if err != nil {
		t.Fatal(err)
	}
	csr := testNodeCSR(t)
	one, err := controlpki.IssueNodeCertificate(material, csr, now)
	if err != nil {
		t.Fatal(err)
	}
	two, err := controlpki.IssueNodeCertificate(material, csr, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	identity := NodeIdentity{ControllerID: "22222222-2222-4222-8222-222222222222", NodeID: "11111111-1111-4111-8111-111111111111", BindingEpoch: 1}
	generation := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	start := make(chan struct{})
	results := make(chan struct {
		der []byte
		err error
	}, 2)
	var workers sync.WaitGroup
	for index, db := range []*DB{first, second} {
		workers.Add(1)
		go func(index int, db *DB) {
			defer workers.Done()
			<-start
			issued := one
			if index == 1 {
				issued = two
			}
			der, err := db.RegisterInitialNodeCertificate(ctx, identity, generation, issued, now.Add(2*time.Second))
			results <- struct {
				der []byte
				err error
			}{der, err}
		}(index, db)
	}
	close(start)
	workers.Wait()
	close(results)
	var original []byte
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent retry failed: %v", result.err)
		}
		if original == nil {
			original = result.der
		} else if !bytes.Equal(original, result.der) {
			t.Fatal("concurrent retries returned different certificates")
		}
	}
	if got := countRows(t, first, "SELECT count(*) FROM control_node_certificates"); got != 1 {
		t.Fatalf("concurrent issuance rows = %d", got)
	}
	cert, err := x509.ParseCertificate(original)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.RevokeNodeCertificate(ctx, generation, cert.SerialNumber.String(), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if _, err := reopened.FindActiveNodeCertificate(ctx, identity.ControllerID, generation, cert, now.Add(time.Minute)); !errors.Is(err, ErrNodeCertificateDenied) {
		t.Fatalf("revocation lost after reopen: %v", err)
	}
}

func testNodeCSR(t *testing.T) []byte {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}
