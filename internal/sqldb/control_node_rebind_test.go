package sqldb

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlpki"
)

func rebindFixture(t *testing.T) (*DB, controlpki.Material, NodeIdentity, string, *x509.Certificate, time.Time) {
	t.Helper()
	db := openControllerAuthTestDB(t)
	now := time.Now().UTC()
	material, _, err := controlpki.Generate(controlpki.Endpoint{
		BindIP: "127.0.0.1", Advertised: "127.0.0.1", Port: 8443,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	identity := NodeIdentity{
		ControllerID: "22222222-2222-4222-8222-222222222222",
		NodeID:       "11111111-1111-4111-8111-111111111111", BindingEpoch: 1,
	}
	const generation = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	issued, err := controlpki.IssueNodeCertificate(material, testNodeCSR(t), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.RegisterInitialNodeCertificate(context.Background(), identity, generation, issued, now); err != nil {
		t.Fatal(err)
	}
	old, err := x509.ParseCertificate(issued.DER)
	if err != nil {
		t.Fatal(err)
	}
	return db, material, identity, generation, old, now.Add(time.Minute)
}

func TestRebindRevokedNodeCertificateFencesOldEpochAndRetries(t *testing.T) {
	ctx := context.Background()
	db, material, oldIdentity, generation, old, now := rebindFixture(t)
	csr := testNodeCSR(t)
	issued, err := controlpki.IssueNodeCertificate(material, csr, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.RebindRevokedNodeCertificate(ctx, oldIdentity, generation, issued, now); !errors.Is(err, ErrNodeCertificateDenied) {
		t.Fatalf("unrevoked binding: %v", err)
	}
	if err := db.RevokeNodeBinding(ctx, oldIdentity, now); err != nil {
		t.Fatal(err)
	}
	stored, err := db.RebindRevokedNodeCertificate(ctx, oldIdentity, generation, issued, now)
	if err != nil || !bytes.Equal(stored, issued.DER) {
		t.Fatalf("rebind: %v", err)
	}
	if _, err := db.FindActiveNodeCertificate(ctx, oldIdentity.ControllerID, generation, old, now); !errors.Is(err, ErrNodeCertificateDenied) {
		t.Fatalf("old certificate remains active: %v", err)
	}
	newCert, err := x509.ParseCertificate(stored)
	if err != nil {
		t.Fatal(err)
	}
	want := oldIdentity
	want.BindingEpoch++
	if got, err := db.FindActiveNodeCertificate(ctx, want.ControllerID, generation, newCert, now); err != nil || got != want {
		t.Fatalf("new binding = %+v, %v", got, err)
	}
	reissued, err := controlpki.IssueNodeCertificate(material, csr, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if retry, err := db.RebindRevokedNodeCertificate(ctx, oldIdentity, generation, reissued, now.Add(time.Second)); err != nil || !bytes.Equal(retry, stored) {
		t.Fatalf("exact retry changed certificate: %v", err)
	}
	if retry, found, err := db.FindReboundNodeCertificate(ctx, oldIdentity, generation,
		reissued.CSRHash, reissued.PublicKeyHash, now.Add(time.Second)); err != nil || !found || !bytes.Equal(retry, stored) {
		t.Fatalf("pre-sign retry lookup changed certificate: found=%v err=%v", found, err)
	}
	competing, err := controlpki.IssueNodeCertificate(material, testNodeCSR(t), now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.RebindRevokedNodeCertificate(ctx, oldIdentity, generation, competing, now.Add(time.Second)); !errors.Is(err, ErrNodeCertificateConflict) {
		t.Fatalf("competing CSR: %v", err)
	}
	wrongController := oldIdentity
	wrongController.ControllerID = "33333333-3333-4333-8333-333333333333"
	if _, err := db.RebindRevokedNodeCertificate(ctx, wrongController, generation, reissued, now); !errors.Is(err, ErrNodeCertificateConflict) {
		t.Fatalf("controller transfer: %v", err)
	}
	if err := db.RevokeNodeBinding(ctx, want, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FindActiveNodeCertificate(ctx, want.ControllerID, generation, newCert, now.Add(2*time.Second)); !errors.Is(err, ErrNodeCertificateDenied) {
		t.Fatalf("new binding revocation: %v", err)
	}
	if _, err := db.RebindRevokedNodeCertificate(ctx, oldIdentity, generation, reissued, now.Add(2*time.Second)); !errors.Is(err, ErrNodeCertificateConflict) {
		t.Fatalf("stale epoch retry: %v", err)
	}
	if _, err := db.RebindRevokedNodeCertificate(ctx, want, generation, reissued, now.Add(2*time.Second)); !errors.Is(err, ErrNodeCertificateDenied) {
		t.Fatalf("reused node key: %v", err)
	}
	third, err := controlpki.IssueNodeCertificate(material, testNodeCSR(t), now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	thirdDER, err := db.RebindRevokedNodeCertificate(ctx, want, generation, third, now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("second recovery: %v", err)
	}
	thirdCert, err := x509.ParseCertificate(thirdDER)
	if err != nil {
		t.Fatal(err)
	}
	want.BindingEpoch++
	if got, err := db.FindActiveNodeCertificate(ctx, want.ControllerID, generation, thirdCert, now.Add(2*time.Second)); err != nil || got != want {
		t.Fatalf("third epoch = %+v, %v", got, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := db.RebindRevokedNodeCertificate(cancelled, want, generation, third, now.Add(3*time.Second)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled request: %v", err)
	}
}

func TestRebindRetryDeniedAfterRenewalCutoff(t *testing.T) {
	ctx := context.Background()
	db, material, oldIdentity, generation, _, now := rebindFixture(t)
	if err := db.RevokeNodeBinding(ctx, oldIdentity, now); err != nil {
		t.Fatal(err)
	}
	csr := testNodeCSR(t)
	rebound, err := controlpki.IssueNodeCertificate(material, csr, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.RebindRevokedNodeCertificate(ctx, oldIdentity, generation, rebound, now); err != nil {
		t.Fatal(err)
	}
	predecessor, err := x509.ParseCertificate(rebound.DER)
	if err != nil {
		t.Fatal(err)
	}
	renewAt := predecessor.NotBefore.Add(predecessor.NotAfter.Sub(predecessor.NotBefore) * 2 / 3).Add(time.Minute)
	successor, err := controlpki.IssueNodeCertificate(material, testNodeCSR(t), renewAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.RenewNodeCertificate(ctx, oldIdentity.ControllerID, generation, predecessor, successor, renewAt); err != nil {
		t.Fatal(err)
	}
	cutoff := renewAt.Add(24 * time.Hour)
	if predecessor.NotAfter.Before(cutoff) {
		cutoff = predecessor.NotAfter
	}
	if _, found, err := db.FindReboundNodeCertificate(ctx, oldIdentity, generation,
		rebound.CSRHash, rebound.PublicKeyHash, cutoff); found || !errors.Is(err, ErrNodeCertificateDenied) {
		t.Fatalf("lookup after cutoff: found=%v err=%v", found, err)
	}
	if _, err := db.RebindRevokedNodeCertificate(ctx, oldIdentity, generation, rebound, cutoff); !errors.Is(err, ErrNodeCertificateDenied) {
		t.Fatalf("rebind retry after cutoff: %v", err)
	}
}

func TestRebindRevokedNodeCertificateTransactionFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	db, material, identity, generation, old, now := rebindFixture(t)
	if err := db.RevokeNodeBinding(ctx, identity, now); err != nil {
		t.Fatal(err)
	}
	issued, err := controlpki.IssueNodeCertificate(material, testNodeCSR(t), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, `CREATE TRIGGER fail_rebind_certificate
 BEFORE INSERT ON control_node_certificates BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RebindRevokedNodeCertificate(ctx, identity, generation, issued, now); err == nil {
		t.Fatal("injected failure accepted")
	}
	if _, err := db.FindActiveNodeCertificate(ctx, identity.ControllerID, generation, old, now); !errors.Is(err, ErrNodeCertificateDenied) {
		t.Fatalf("old certificate revived after rollback: %v", err)
	}
	var epoch int64
	var revoked int64
	if err := db.sql.QueryRowContext(ctx, `SELECT binding_epoch, revoked_at_unix_ms
 FROM control_node_bindings WHERE node_id = ?`, identity.NodeID).Scan(&epoch, &revoked); err != nil || epoch != 1 {
		t.Fatalf("binding changed after failed insert: epoch=%d err=%v", epoch, err)
	}
	if got := countRebindRows(t, db, `SELECT count(*) FROM control_node_certificates WHERE node_id = ?`, identity.NodeID); got != 1 {
		t.Fatalf("certificate inserted after failed commit: %d", got)
	}
}

func TestRebindRevokedNodeCertificateConcurrentRequests(t *testing.T) {
	for _, sameCSR := range []bool{true, false} {
		t.Run(map[bool]string{true: "same", false: "competing"}[sameCSR], func(t *testing.T) {
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
			now := time.Now().UTC()
			material, _, err := controlpki.Generate(controlpki.Endpoint{BindIP: "127.0.0.1", Advertised: "127.0.0.1", Port: 8443}, now)
			if err != nil {
				t.Fatal(err)
			}
			identity := NodeIdentity{ControllerID: "22222222-2222-4222-8222-222222222222", NodeID: "11111111-1111-4111-8111-111111111111", BindingEpoch: 1}
			const generation = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			initial, err := controlpki.IssueNodeCertificate(material, testNodeCSR(t), now)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := first.RegisterInitialNodeCertificate(ctx, identity, generation, initial, now); err != nil {
				t.Fatal(err)
			}
			if err := first.RevokeNodeBinding(ctx, identity, now); err != nil {
				t.Fatal(err)
			}
			csr := testNodeCSR(t)
			otherCSR := csr
			if !sameCSR {
				otherCSR = testNodeCSR(t)
			}
			certificates := [2]controlpki.NodeCertificate{}
			for i, request := range [][]byte{csr, otherCSR} {
				certificates[i], err = controlpki.IssueNodeCertificate(material, request, now.Add(time.Minute))
				if err != nil {
					t.Fatal(err)
				}
			}
			var wg sync.WaitGroup
			start := make(chan struct{})
			results := [2][]byte{}
			errs := [2]error{}
			for i, handle := range []*DB{first, second} {
				wg.Add(1)
				go func(index int, db *DB) {
					defer wg.Done()
					<-start
					results[index], errs[index] = db.RebindRevokedNodeCertificate(ctx, identity, generation, certificates[index], now.Add(time.Minute))
				}(i, handle)
			}
			close(start)
			wg.Wait()
			if sameCSR {
				if errs[0] != nil || errs[1] != nil || !bytes.Equal(results[0], results[1]) {
					t.Fatalf("same CSR did not converge: %v, %v", errs[0], errs[1])
				}
			} else {
				var success, conflict int
				for _, err := range errs {
					if err == nil {
						success++
					} else if errors.Is(err, ErrNodeCertificateConflict) {
						conflict++
					} else {
						t.Fatalf("unexpected concurrent error: %v", err)
					}
				}
				if success != 1 || conflict != 1 {
					t.Fatalf("concurrent outcomes: success=%d conflict=%d", success, conflict)
				}
			}
			if got := countRebindRows(t, first, `SELECT count(*) FROM control_node_certificates WHERE node_id = ? AND binding_epoch = 2`, identity.NodeID); got != 1 {
				t.Fatalf("new epoch certificates = %d", got)
			}
		})
	}
}

func TestControlNodeRebindMigrationFromVersionSeven(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, retentionTestConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	migrations, err := loadSQLiteMigrations()
	if err != nil || len(migrations) < 8 {
		t.Fatalf("load migrations: %v", err)
	}
	if _, err := db.sql.ExecContext(ctx, `CREATE TABLE schema_migrations (
 version INTEGER PRIMARY KEY, checksum TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:7] {
		if err := db.applyMigration(ctx, migration); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO audit_events (time, level, event)
 VALUES (?, 'info', 'preserved-rebind-migration')`, formatTime(now)); err != nil {
		t.Fatal(err)
	}
	material, _, err := controlpki.Generate(controlpki.Endpoint{BindIP: "127.0.0.1", Advertised: "127.0.0.1", Port: 8443}, now)
	if err != nil {
		t.Fatal(err)
	}
	identity := NodeIdentity{ControllerID: "22222222-2222-4222-8222-222222222222", NodeID: "11111111-1111-4111-8111-111111111111", BindingEpoch: 1}
	const generation = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	issued, err := controlpki.IssueNodeCertificate(material, testNodeCSR(t), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.RegisterInitialNodeCertificate(ctx, identity, generation, issued, now); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if got := countRebindRows(t, db, `SELECT count(*) FROM audit_events WHERE event = 'preserved-rebind-migration'`); got != 1 {
		t.Fatalf("audit row lost: %d", got)
	}
	old, err := x509.ParseCertificate(issued.DER)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := db.FindActiveNodeCertificate(ctx, identity.ControllerID, generation, old, now); err != nil || got != identity {
		t.Fatalf("existing certificate lost: %+v, %v", got, err)
	}
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO control_node_certificates
 (issuer_generation, serial, node_id, controller_id, binding_epoch,
 certificate_sha256, public_key_sha256, csr_sha256, certificate_der,
 not_before_unix_ms, not_after_unix_ms)
 SELECT issuer_generation, serial || '2', node_id, controller_id, binding_epoch,
 certificate_sha256, public_key_sha256, zeroblob(32), certificate_der,
 not_before_unix_ms, not_after_unix_ms FROM control_node_certificates
 WHERE issuer_generation = ? AND serial = ?`, generation, issued.Serial); err == nil {
		t.Fatal("duplicate initial certificate accepted for one epoch")
	}
}

func countRebindRows(t *testing.T, db *DB, query string, args ...any) int {
	t.Helper()
	var count int
	if err := db.sql.QueryRowContext(context.Background(), query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
