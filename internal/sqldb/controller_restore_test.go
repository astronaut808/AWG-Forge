package sqldb

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlauth"
	"github.com/astronaut808/awg-forge/internal/controlpki"
)

func TestControllerRestoreResetRevokesNodeHistoryAndRetries(t *testing.T) {
	ctx := context.Background()
	db := openControllerAuthTestDB(t)
	identity, generation, old, material := renewalFixture(t, db)
	now := renewalStart(old)
	user := controllerAuthTestUser(now)
	session := controllerAuthTestSession(user.ID, controllerAuthTestDigest(2), now)
	if err := db.CreateControllerUserWithSession(ctx, user, []controlauth.Digest{controllerAuthTestDigest(1)}, session); err != nil {
		t.Fatal(err)
	}
	csr := testNodeCSR(t)
	successor, err := controlpki.IssueNodeCertificate(material, csr, now)
	if err != nil {
		t.Fatal(err)
	}
	der, err := db.RenewNodeCertificate(ctx, identity.ControllerID, generation, old, successor, now)
	if err != nil {
		t.Fatal(err)
	}
	current, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	prior := now.Add(-time.Minute).UnixMilli()
	if _, err := db.sql.ExecContext(ctx, "UPDATE control_node_certificates SET revoked_at_unix_ms = ? WHERE serial = ?", prior, old.SerialNumber.String()); err != nil {
		t.Fatal(err)
	}
	resetAt := now.Add(time.Minute)
	if err := db.DisableControllerAuthAfterRestore(ctx, resetAt); err != nil {
		t.Fatal(err)
	}
	for _, cert := range []*x509.Certificate{old, current} {
		if _, err := db.FindActiveNodeCertificate(ctx, identity.ControllerID, generation, cert, resetAt); !errors.Is(err, ErrNodeCertificateDenied) {
			t.Fatalf("restored certificate admission: %v", err)
		}
	}
	if _, _, err := db.FindRenewedNodeCertificate(ctx, identity.ControllerID, generation, old, successor.CSRHash, successor.PublicKeyHash, resetAt); !errors.Is(err, ErrNodeCertificateDenied) {
		t.Fatalf("restored renewal retry: %v", err)
	}
	var cutoff, revoked int64
	var storedDER []byte
	if err := db.sql.QueryRowContext(ctx, "SELECT superseded_at_unix_ms, revoked_at_unix_ms, certificate_der FROM control_node_certificates WHERE serial = ?", old.SerialNumber.String()).Scan(&cutoff, &revoked, &storedDER); err != nil {
		t.Fatal(err)
	}
	if cutoff != now.Add(24*time.Hour).UnixMilli() || revoked != prior || !bytes.Equal(storedDER, old.Raw) {
		t.Fatal("restore changed public certificate history")
	}
	if err := db.DisableControllerAuthAfterRestore(ctx, resetAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var bindingEpoch, bindingRevoked, certRevoked int64
	if err := db.sql.QueryRowContext(ctx, "SELECT binding_epoch, revoked_at_unix_ms FROM control_node_bindings WHERE node_id = ?", identity.NodeID).Scan(&bindingEpoch, &bindingRevoked); err != nil {
		t.Fatal(err)
	}
	if err := db.sql.QueryRowContext(ctx, "SELECT revoked_at_unix_ms FROM control_node_certificates WHERE serial = ?", current.SerialNumber.String()).Scan(&certRevoked); err != nil {
		t.Fatal(err)
	}
	if bindingEpoch != 1 || bindingRevoked != resetAt.UnixMilli() || certRevoked != resetAt.UnixMilli() {
		t.Fatal("repeated restore reset changed revocation or binding epoch")
	}
	if err := db.RecoverControllerAdmin(ctx, user, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FindActiveNodeCertificate(ctx, identity.ControllerID, generation, current, resetAt); !errors.Is(err, ErrNodeCertificateDenied) {
		t.Fatalf("administrator recovery undid node revocation: %v", err)
	}
	newCertificate, err := controlpki.IssueNodeCertificate(material, testNodeCSR(t), resetAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.RebindRevokedNodeCertificate(ctx, identity, generation, newCertificate, resetAt); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FindActiveNodeCertificate(ctx, identity.ControllerID, generation, current, resetAt); !errors.Is(err, ErrNodeCertificateDenied) {
		t.Fatalf("explicit fresh rebind reactivated an archived serial: %v", err)
	}
}

func TestControllerRestoreResetRollsBackEveryStatementAndCommit(t *testing.T) {
	for _, fault := range []struct {
		name    string
		trigger string
	}{
		{"controller_users", "CREATE TRIGGER restore_fault BEFORE UPDATE ON controller_users BEGIN SELECT RAISE(ABORT, 'restore fault'); END"},
		{"controller_sessions", "CREATE TRIGGER restore_fault BEFORE DELETE ON controller_sessions BEGIN SELECT RAISE(ABORT, 'restore fault'); END"},
		{"controller_recovery_codes", "CREATE TRIGGER restore_fault BEFORE DELETE ON controller_recovery_codes BEGIN SELECT RAISE(ABORT, 'restore fault'); END"},
		{"controller_auth_attempts", "CREATE TRIGGER restore_fault BEFORE DELETE ON controller_auth_attempts BEGIN SELECT RAISE(ABORT, 'restore fault'); END"},
		{"control_node_certificates", "CREATE TRIGGER restore_fault BEFORE UPDATE ON control_node_certificates BEGIN SELECT RAISE(ABORT, 'restore fault'); END"},
		{"control_node_bindings", "CREATE TRIGGER restore_fault BEFORE UPDATE ON control_node_bindings BEGIN SELECT RAISE(ABORT, 'restore fault'); END"},
		{"commit", "CREATE TRIGGER restore_fault AFTER UPDATE ON control_node_bindings BEGIN INSERT INTO restore_commit_failure VALUES ('missing'); END"},
	} {
		t.Run(fault.name, func(t *testing.T) {
			ctx := context.Background()
			db := openControllerAuthTestDB(t)
			identity, generation, cert, _ := renewalFixture(t, db)
			now := time.Now().UTC()
			user := controllerAuthTestUser(now)
			session := controllerAuthTestSession(user.ID, controllerAuthTestDigest(2), now)
			if err := db.CreateControllerUserWithSession(ctx, user, []controlauth.Digest{controllerAuthTestDigest(1)}, session); err != nil {
				t.Fatal(err)
			}
			reserveControllerAuthTestAttempt(t, db, now)
			if fault.name == "commit" {
				// A deferred FK fails only at Commit, after all reset statements.
				if _, err := db.sql.ExecContext(ctx, "CREATE TABLE restore_commit_failure (node_id TEXT REFERENCES control_node_bindings(node_id) DEFERRABLE INITIALLY DEFERRED)"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.sql.ExecContext(ctx, fault.trigger); err != nil {
				t.Fatal(err)
			}
			if err := db.DisableControllerAuthAfterRestore(ctx, now.Add(time.Minute)); err == nil {
				t.Fatal("injected restore reset failure succeeded")
			}
			stored, err := db.FindControllerUser(ctx, user.Username)
			if err != nil || !stored.DisabledAt.IsZero() {
				t.Fatalf("failed reset disabled administrator: %v", err)
			}
			if _, err := db.FindControllerSession(ctx, session.Digest, now); err != nil {
				t.Fatalf("failed reset deleted session: %v", err)
			}
			for _, query := range []string{"SELECT count(*) FROM controller_recovery_codes", "SELECT count(*) FROM controller_auth_attempts"} {
				var count int
				if err := db.sql.QueryRowContext(ctx, query).Scan(&count); err != nil || count != 1 {
					t.Fatalf("failed reset deleted replay credentials: count=%d, error=%v", count, err)
				}
			}
			if _, err := db.FindActiveNodeCertificate(ctx, identity.ControllerID, generation, cert, now); err != nil {
				t.Fatalf("failed reset revoked node authority: %v", err)
			}
		})
	}
}
