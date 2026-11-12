package sqldb

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlpki"
)

func renewalFixture(t *testing.T, db *DB) (NodeIdentity, string, *x509.Certificate, controlpki.Material) {
	t.Helper()
	now := time.Now().UTC()
	material, _, err := controlpki.Generate(controlpki.Endpoint{BindIP: "127.0.0.1", Advertised: "127.0.0.1", Port: 8443}, now)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := controlpki.IssueNodeCertificate(material, testNodeCSR(t), now)
	if err != nil {
		t.Fatal(err)
	}
	identity := NodeIdentity{ControllerID: "22222222-2222-4222-8222-222222222222", NodeID: "11111111-1111-4111-8111-111111111111", BindingEpoch: 1}
	generation := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := db.RegisterInitialNodeCertificate(context.Background(), identity, generation, initial, now); err != nil {
		t.Fatal(err)
	}
	old, err := x509.ParseCertificate(initial.DER)
	if err != nil {
		t.Fatal(err)
	}
	return identity, generation, old, material
}

func renewalStart(old *x509.Certificate) time.Time {
	return old.NotBefore.Add(old.NotAfter.Sub(old.NotBefore) * 2 / 3)
}

func TestRenewNodeCertificateWindowRetryCutoffAndRevocation(t *testing.T) {
	ctx := context.Background()
	db := openControllerAuthTestDB(t)
	identity, generation, old, material := renewalFixture(t, db)
	start := renewalStart(old)
	csr := testNodeCSR(t)
	first, err := controlpki.IssueNodeCertificate(material, csr, start)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.RenewNodeCertificate(ctx, identity.ControllerID, generation, old, first, start.Add(-time.Millisecond)); !errors.Is(err, ErrNodeCertificateDenied) {
		t.Fatalf("before window = %v", err)
	}
	der, err := db.RenewNodeCertificate(ctx, identity.ControllerID, generation, old, first, start)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(der, first.DER) {
		t.Fatal("first renewal returned different DER")
	}
	cutoff := start.Add(24 * time.Hour)
	if old.NotAfter.Before(cutoff) {
		cutoff = old.NotAfter
	}
	var storedCutoff int64
	if err := db.sql.QueryRowContext(ctx, `SELECT superseded_at_unix_ms FROM control_node_certificates WHERE serial = ?`, old.SerialNumber.String()).Scan(&storedCutoff); err != nil || storedCutoff != cutoff.UnixMilli() {
		t.Fatalf("cutoff = %d, %v", storedCutoff, err)
	}
	reissued, err := controlpki.IssueNodeCertificate(material, csr, start.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	retry, err := db.RenewNodeCertificate(ctx, identity.ControllerID, generation, old, reissued, start.Add(time.Minute))
	if err != nil || !bytes.Equal(retry, der) {
		t.Fatalf("exact retry = %v", err)
	}
	var retryCutoff int64
	if err := db.sql.QueryRowContext(ctx, `SELECT superseded_at_unix_ms FROM control_node_certificates WHERE serial = ?`, old.SerialNumber.String()).Scan(&retryCutoff); err != nil || retryCutoff != storedCutoff {
		t.Fatalf("retry moved cutoff = %d, %v", retryCutoff, err)
	}
	different, err := controlpki.IssueNodeCertificate(material, testNodeCSR(t), start.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.RenewNodeCertificate(ctx, identity.ControllerID, generation, old, different, start.Add(time.Minute)); !errors.Is(err, ErrNodeCertificateConflict) {
		t.Fatalf("different CSR = %v", err)
	}
	if got := countRows(t, db, `SELECT count(*) FROM control_node_certificates`); got != 2 {
		t.Fatalf("certificate rows = %d", got)
	}
	newCert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{cutoff.Add(-time.Millisecond), cutoff, cutoff.Add(time.Millisecond)} {
		_, err := db.FindActiveNodeCertificate(ctx, identity.ControllerID, generation, old, at)
		if at.Before(cutoff) && err != nil || !at.Before(cutoff) && !errors.Is(err, ErrNodeCertificateDenied) {
			t.Fatalf("old certificate at cutoff %s = %v", at, err)
		}
	}
	if _, err := db.FindActiveNodeCertificate(ctx, identity.ControllerID, generation, newCert, cutoff); err != nil {
		t.Fatalf("successor denied at cutoff: %v", err)
	}
	if _, err := db.RenewNodeCertificate(ctx, identity.ControllerID, generation, old, reissued, cutoff); !errors.Is(err, ErrNodeCertificateDenied) {
		t.Fatalf("retry after cutoff = %v", err)
	}
	if err := db.RevokeNodeCertificate(ctx, generation, old.SerialNumber.String(), start.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RenewNodeCertificate(ctx, identity.ControllerID, generation, old, reissued, start.Add(time.Minute)); !errors.Is(err, ErrNodeCertificateDenied) {
		t.Fatalf("revoked predecessor = %v", err)
	}
	if _, err := db.FindActiveNodeCertificate(ctx, identity.ControllerID, generation, newCert, start.Add(time.Minute)); err != nil {
		t.Fatalf("serial revocation fenced successor: %v", err)
	}
	if err := db.RevokeNodeBinding(ctx, identity, start.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FindActiveNodeCertificate(ctx, identity.ControllerID, generation, newCert, start.Add(time.Minute)); !errors.Is(err, ErrNodeCertificateDenied) {
		t.Fatalf("binding revocation = %v", err)
	}
}

func TestRenewNodeCertificateConcurrentHandles(t *testing.T) {
	ctx := context.Background()
	cfg := retentionTestConfig(t)
	first, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	if err := first.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	second, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	identity, generation, old, material := renewalFixture(t, first)
	start := renewalStart(old)
	for _, same := range []bool{true, false} {
		if !same {
			// Use the committed successor as the next predecessor to test a
			// separate pair of competing requests in the same database.
			var der []byte
			if err := first.sql.QueryRowContext(ctx, `SELECT certificate_der FROM control_node_certificates WHERE predecessor_serial = ?`, old.SerialNumber.String()).Scan(&der); err != nil {
				t.Fatal(err)
			}
			old, err = x509.ParseCertificate(der)
			if err != nil {
				t.Fatal(err)
			}
			start = renewalStart(old)
		}
		csrA := testNodeCSR(t)
		csrB := csrA
		if !same {
			csrB = testNodeCSR(t)
		}
		a, err := controlpki.IssueNodeCertificate(material, csrA, start)
		if err != nil {
			t.Fatal(err)
		}
		b, err := controlpki.IssueNodeCertificate(material, csrB, start)
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		startCh := make(chan struct{})
		type result struct {
			der []byte
			err error
		}
		results := make(chan result, 2)
		for i, handle := range []*DB{first, second} {
			candidate := a
			if i == 1 {
				candidate = b
			}
			wg.Add(1)
			go func(db *DB, issued controlpki.NodeCertificate) {
				defer wg.Done()
				<-startCh
				der, err := db.RenewNodeCertificate(ctx, identity.ControllerID, generation, old, issued, start)
				results <- result{der, err}
			}(handle, candidate)
		}
		close(startCh)
		wg.Wait()
		close(results)
		var winner []byte
		var conflicts int
		for got := range results {
			if got.err == nil {
				if winner != nil && !bytes.Equal(winner, got.der) {
					t.Fatal("two successor certificates")
				}
				winner = got.der
			} else if errors.Is(got.err, ErrNodeCertificateConflict) {
				conflicts++
			} else {
				t.Fatalf("concurrent renewal = %v", got.err)
			}
		}
		if winner == nil || same && conflicts != 0 || !same && conflicts != 1 {
			t.Fatalf("same=%v winner=%t conflicts=%d", same, winner != nil, conflicts)
		}
	}
	if got := countRows(t, first, `SELECT count(*) FROM control_node_certificates`); got != 3 {
		t.Fatalf("successor rows = %d", got)
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
	if got := countRows(t, reopened, `SELECT count(*) FROM control_node_certificates WHERE predecessor_serial IS NOT NULL`); got != 2 {
		t.Fatalf("successors after restart = %d", got)
	}
}

func TestRenewNodeCertificateDeniedAndRollback(t *testing.T) {
	ctx := context.Background()
	db := openControllerAuthTestDB(t)
	identity, generation, old, material := renewalFixture(t, db)
	at := renewalStart(old)
	issued, err := controlpki.IssueNodeCertificate(material, testNodeCSR(t), at)
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*x509.Certificate, *string, *string){
		"wrong controller": func(_ *x509.Certificate, controller, _ *string) { *controller = "33333333-3333-4333-8333-333333333333" },
		"wrong issuer":     func(_ *x509.Certificate, _, issuer *string) { *issuer = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" },
		"wrong fingerprint": func(cert *x509.Certificate, _, _ *string) {
			cert.Raw = bytes.Clone(cert.Raw)
			cert.Raw[len(cert.Raw)-1] ^= 1
		},
	} {
		t.Run(name, func(t *testing.T) {
			copyCert := *old
			controller, issuer := identity.ControllerID, generation
			change(&copyCert, &controller, &issuer)
			if _, err := db.RenewNodeCertificate(ctx, controller, issuer, &copyCert, issued, at); !errors.Is(err, ErrNodeCertificateDenied) {
				t.Fatalf("renewal = %v", err)
			}
		})
	}
	if _, err := db.RenewNodeCertificate(ctx, identity.ControllerID, generation, old, issued, old.NotAfter); !errors.Is(err, ErrNodeCertificateDenied) {
		t.Fatalf("expired predecessor = %v", err)
	}
	if _, err := db.sql.ExecContext(ctx, `UPDATE control_node_bindings SET binding_epoch = 2 WHERE node_id = ?`, identity.NodeID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RenewNodeCertificate(ctx, identity.ControllerID, generation, old, issued, at); !errors.Is(err, ErrNodeCertificateDenied) {
		t.Fatalf("wrong binding epoch = %v", err)
	}
	if _, err := db.sql.ExecContext(ctx, `UPDATE control_node_bindings SET binding_epoch = 1 WHERE node_id = ?`, identity.NodeID); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := db.RenewNodeCertificate(cancelled, identity.ControllerID, generation, old, issued, at); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled renewal = %v", err)
	}
	if got := countRows(t, db, `SELECT count(*) FROM control_node_certificates`); got != 1 {
		t.Fatalf("failed attempts left %d rows", got)
	}
	if _, err := db.sql.ExecContext(ctx, `CREATE TRIGGER block_node_renewal BEFORE INSERT ON control_node_certificates
 WHEN NEW.predecessor_serial IS NOT NULL BEGIN SELECT RAISE(FAIL, 'blocked'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RenewNodeCertificate(ctx, identity.ControllerID, generation, old, issued, at); err == nil {
		t.Fatal("transaction failure returned certificate")
	}
	if got := countRows(t, db, `SELECT count(*) FROM control_node_certificates`); got != 1 {
		t.Fatalf("failed transaction left %d rows", got)
	}
	if _, err := db.FindActiveNodeCertificate(ctx, identity.ControllerID, generation, old, at); err != nil {
		t.Fatalf("failure superseded predecessor: %v", err)
	}
	if _, err := db.sql.ExecContext(ctx, `DROP TRIGGER block_node_renewal`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RenewNodeCertificate(ctx, identity.ControllerID, generation, old, issued, at); err != nil {
		t.Fatalf("recovery = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RenewNodeCertificate(ctx, identity.ControllerID, generation, old, issued, at); err == nil {
		t.Fatal("closed database renewed")
	}
}

func TestRenewNodeCertificateCSRFromAnotherPredecessorConflicts(t *testing.T) {
	ctx := context.Background()
	db := openControllerAuthTestDB(t)
	identity, generation, old, material := renewalFixture(t, db)
	csr := testNodeCSR(t)
	at := renewalStart(old)
	first, err := controlpki.IssueNodeCertificate(material, csr, at)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.RenewNodeCertificate(ctx, identity.ControllerID, generation, old, first, at); err != nil {
		t.Fatal(err)
	}
	other := identity
	other.NodeID = "33333333-3333-4333-8333-333333333333"
	otherInitial, err := controlpki.IssueNodeCertificate(material, testNodeCSR(t), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.RegisterInitialNodeCertificate(ctx, other, generation, otherInitial, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	otherOld, err := x509.ParseCertificate(otherInitial.DER)
	if err != nil {
		t.Fatal(err)
	}
	otherAt := renewalStart(otherOld)
	reused, err := controlpki.IssueNodeCertificate(material, csr, otherAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.RenewNodeCertificate(ctx, other.ControllerID, generation, otherOld, reused, otherAt); !errors.Is(err, ErrNodeCertificateConflict) {
		t.Fatalf("same CSR from another predecessor = %v", err)
	}
	if got := countRows(t, db, `SELECT count(*) FROM control_node_certificates`); got != 3 {
		t.Fatalf("unexpected rows: %d", got)
	}
}

func TestControlNodeRenewalMigrationFromVersionSix(t *testing.T) {
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
	if _, err := db.sql.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, checksum TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:6] {
		if err := db.applyMigration(ctx, migration); err != nil {
			t.Fatal(err)
		}
	}
	identity, generation, old, _ := renewalFixture(t, db)
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO audit_events (time, level, event) VALUES (?, 'info', 'keep-renewal')`, formatTime(time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, db, `SELECT count(*) FROM audit_events WHERE event = 'keep-renewal'`); got != 1 {
		t.Fatalf("audit lost: %d", got)
	}
	if _, err := db.FindActiveNodeCertificate(ctx, identity.ControllerID, generation, old, time.Now().UTC()); err != nil {
		t.Fatalf("certificate lost: %v", err)
	}
	if got := countRows(t, db, `SELECT count(*) FROM control_node_certificates WHERE predecessor_serial IS NULL`); got != 1 {
		t.Fatalf("old row lost: %d", got)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
}

func TestRenewNodeCertificateExactRetryNearCAExpiry(t *testing.T) {
	ctx := context.Background()
	db := openControllerAuthTestDB(t)
	material, _, err := controlpki.Generate(controlpki.Endpoint{BindIP: "127.0.0.1", Advertised: "127.0.0.1", Port: 8443}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(material.CACert)
	if block == nil {
		t.Fatal("CA certificate")
	}
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	initialAt := ca.NotAfter.Add(-4 * 24 * time.Hour)
	oldIssued, err := controlpki.IssueNodeCertificate(material, testNodeCSR(t), initialAt)
	if err != nil {
		t.Fatal(err)
	}
	identity := NodeIdentity{ControllerID: "22222222-2222-4222-8222-222222222222", NodeID: "11111111-1111-4111-8111-111111111111", BindingEpoch: 1}
	generation := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := db.RegisterInitialNodeCertificate(ctx, identity, generation, oldIssued, initialAt); err != nil {
		t.Fatal(err)
	}
	old, err := x509.ParseCertificate(oldIssued.DER)
	if err != nil {
		t.Fatal(err)
	}
	firstAt := ca.NotAfter.Add(-25 * time.Hour)
	if firstAt.Before(renewalStart(old)) {
		t.Fatal("fixture is before renewal window")
	}
	csr := testNodeCSR(t)
	first, err := controlpki.IssueNodeCertificate(material, csr, firstAt)
	if err != nil {
		t.Fatal(err)
	}
	der, err := db.RenewNodeCertificate(ctx, identity.ControllerID, generation, old, first, firstAt)
	if err != nil {
		t.Fatal(err)
	}
	retryAt := ca.NotAfter.Add(-23 * time.Hour)
	if _, err := controlpki.IssueNodeCertificate(material, csr, retryAt); err == nil {
		t.Fatal("CA unexpectedly signed within final 24 hours")
	}
	recovered, found, err := db.FindRenewedNodeCertificate(ctx, identity.ControllerID, generation, old, first.CSRHash, first.PublicKeyHash, retryAt)
	if err != nil || !found || !bytes.Equal(recovered, der) {
		t.Fatalf("exact retry failed without new signature: found=%v err=%v", found, err)
	}
}
