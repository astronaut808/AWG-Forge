package sqldb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/astronaut808/awg-forge/internal/controlpki"
	"github.com/google/uuid"
)

func TestControllerSnapshotSchema11RejectsProjectionUnknownFieldsAndBadMetadata(t *testing.T) {
	ctx := context.Background()
	db, material, generation, identity := populatedSnapshotSchema(t, 11, false)
	now := time.Now().UTC()
	stateEpoch, bootID := uuid.NewString(), uuid.NewString()
	var serial string
	if err := db.sql.QueryRowContext(ctx, "SELECT serial FROM control_node_certificates WHERE node_id=?", identity.NodeID).Scan(&serial); err != nil {
		t.Fatal(err)
	}
	session, err := db.AcceptAuthenticatedNodePresence(ctx, identity, stateEpoch, bootID, 1, generation, serial, now)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := projectionSnapshot(session, stateEpoch, bootID, now)
	desired, err := json.Marshal(snapshot.Desired)
	if err != nil {
		t.Fatal(err)
	}
	observations, err := json.Marshal(snapshot.Observations)
	if err != nil {
		t.Fatal(err)
	}
	insertProjection := func(desired, observations []byte) {
		t.Helper()
		if _, err := db.sql.ExecContext(ctx, `INSERT INTO control_node_projections
(node_id,controller_id,binding_epoch,state_epoch,boot_id,boot_sequence,session_id,desired_generation,snapshot_sequence,observed_at_unix_ms,received_at_unix_ms,desired_json,observations_json)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, identity.NodeID, identity.ControllerID, 1, stateEpoch, bootID, 1, session, 1, 1, now.UnixMilli(), now.UnixMilli(), desired, observations); err != nil {
			t.Fatal(err)
		}
	}
	insertProjection(desired, observations)
	if err := VerifyControllerRegistrySnapshot(ctx, snapshotPath(t, db), snapshotControllerID, generation, snapshotCA(t, material)); err != nil {
		t.Fatalf("valid schema 11 archive rejected: %v", err)
	}

	if _, err := db.sql.ExecContext(ctx, "UPDATE control_node_projections SET desired_json=?", []byte(`{"tunnels":[],"private_key":"canary"}`)); err != nil {
		t.Fatal(err)
	}
	if err := VerifyControllerRegistrySnapshot(ctx, snapshotPath(t, db), snapshotControllerID, generation, snapshotCA(t, material)); err == nil {
		t.Fatal("projection with unknown field accepted")
	}
	if _, err := db.sql.ExecContext(ctx, "UPDATE control_node_projections SET desired_json=?", desired); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, "UPDATE control_node_presence SET capabilities_json='[\"snapshot.v1\",\"snapshot.v1\"]', application_version='v1', contract_version=1, confirmed_at_unix_ms=?", now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err := VerifyControllerRegistrySnapshot(ctx, snapshotPath(t, db), snapshotControllerID, generation, snapshotCA(t, material)); err == nil {
		t.Fatal("duplicate presence capability accepted")
	}
}

func TestPresenceReadFailureDoesNotBecomeAuthorityDenial(t *testing.T) {
	db, _, generation, identity := populatedSnapshotSchema(t, 11, false)
	if _, err := db.sql.Exec(`ALTER TABLE control_node_bindings RENAME COLUMN controller_id TO unavailable_controller_id`); err != nil {
		t.Fatal(err)
	}
	p := controlapi.Presence{BootID: uuid.NewString(), BootSequence: 1, StateEpoch: uuid.NewString(), ApplicationVersion: "test", ContractVersion: 1, Capabilities: []string{"presence", "snapshot.v1"}}
	_, err := db.RecordNodePresence(context.Background(), identity, generation, "1", p, time.Now())
	if err == nil || errors.Is(err, ErrNodeCertificateDenied) {
		t.Fatal("registry read failure became a permanent authority denial")
	}
}

func projectionSnapshot(session, stateEpoch, bootID string, now time.Time) controlapi.Snapshot {
	return controlapi.Snapshot{ContractVersion: 1, SessionID: session, StateEpoch: stateEpoch, BindingEpoch: 1, BootID: bootID, BootSequence: 1, DesiredGeneration: 1, Sequence: 1, ObservedAt: now,
		Desired:      controlapi.DesiredProjection{Tunnels: []controlapi.SnapshotTunnel{}},
		Observations: controlapi.Observations{Tunnels: []controlapi.TunnelObservation{}, Doctor: controlapi.DoctorSummary{Scope: "runtime"}},
	}
}

func TestAcceptNodeSnapshotReplacementSubtractsPriorProjectionFromTotalQuota(t *testing.T) {
	ctx := context.Background()
	db, _, generation, identity, serial, snapshot, now := projectionFixture(t)
	if err := db.AcceptNodeSnapshot(ctx, identity, generation, serial, snapshot, now); err != nil {
		t.Fatal(err)
	}
	// A different desired generation is deliberately larger. Fill all remaining
	// quota with unrelated retained rows: an update must replace its own old
	// bytes, not count both generations.
	snapshot.Sequence++
	snapshot.DesiredGeneration++
	snapshot.Desired.Tunnels = []controlapi.SnapshotTunnel{{ID: "0123456789abcdef", Name: "quota", Interface: "awg0", Profile: "awg_2_0", Enabled: true, ListenPort: 51820, Clients: []controlapi.SnapshotClient{}}}
	snapshot.Observations.Tunnels = []controlapi.TunnelObservation{{ID: "0123456789abcdef", Clients: []controlapi.ClientObservation{}}}
	desired, err := json.Marshal(snapshot.Desired)
	if err != nil {
		t.Fatal(err)
	}
	observations, err := json.Marshal(snapshot.Observations)
	if err != nil {
		t.Fatal(err)
	}
	remaining := controlapi.MaxProjectionStorageBytes - len(desired) - len(observations)
	if _, err := db.sql.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		t.Fatal(err)
	}
	for n := 0; remaining >= 2; n++ {
		chunk := min(remaining, controlapi.MaxSnapshotBytes)
		if chunk == 1 {
			break
		}
		if _, err := db.sql.ExecContext(ctx, `INSERT INTO control_node_projections
(node_id,controller_id,binding_epoch,state_epoch,boot_id,boot_sequence,session_id,desired_generation,snapshot_sequence,observed_at_unix_ms,received_at_unix_ms,desired_json,observations_json)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, fmt.Sprintf("padding-%d", n), identity.ControllerID, 1, "state", "boot", 1, "session", 0, 1, now.UnixMilli(), now.UnixMilli(), make([]byte, chunk-1), []byte("x")); err != nil {
			t.Fatal(err)
		}
		remaining -= chunk
	}
	if _, err := db.sql.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		t.Fatal(err)
	}
	if err := db.AcceptNodeSnapshot(ctx, identity, generation, serial, snapshot, now); err != nil {
		t.Fatalf("replacement should subtract prior projection size: %v", err)
	}
	snapshot.Sequence++
	snapshot.DesiredGeneration++
	snapshot.Desired.Tunnels[0].Name += "-larger"
	if err := db.AcceptNodeSnapshot(ctx, identity, generation, serial, snapshot, now); !errors.Is(err, ErrProjectionLimit) {
		t.Fatalf("over-quota update error = %v", err)
	}
}

func TestNodeProjectionFailsClosedOnNestedPrivateJSONAndRestoreClearsCache(t *testing.T) {
	ctx := context.Background()
	db, _, generation, identity, serial, snapshot, now := projectionFixture(t)
	if err := db.AcceptNodeSnapshot(ctx, identity, generation, serial, snapshot, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, "UPDATE control_node_projections SET desired_json=? WHERE node_id=?", []byte(`{"tunnels":[{"id":"0123456789abcdef","name":"node","interface":"awg0","profile":"awg_2_0","enabled":true,"listen_port":51820,"revision":0,"clients":[],"private_key":"canary"}]}`), identity.NodeID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.NodeProjection(ctx, identity.ControllerID, generation, identity.NodeID, now); !errors.Is(err, ErrSnapshotFenced) {
		t.Fatalf("nested private projection field error = %v", err)
	}
	if _, err := db.sql.ExecContext(ctx, "UPDATE control_node_projections SET desired_json=? WHERE node_id=?", mustJSON(t, snapshot.Desired), identity.NodeID); err != nil {
		t.Fatal(err)
	}
	if err := db.DisableControllerAuthAfterRestore(ctx, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := db.VerifyControllerRestoreReset(ctx); err != nil {
		t.Fatalf("restore left authority cache: %v", err)
	}
	var projections int
	if err := db.sql.QueryRowContext(ctx, "SELECT count(*) FROM control_node_projections").Scan(&projections); err != nil || projections != 0 {
		t.Fatalf("projection cache count = %d, err=%v", projections, err)
	}
}

func TestSchema11ArchiveAcceptsHistoricalProjectionButRequiresCertificateProvenance(t *testing.T) {
	ctx := context.Background()
	db, material, generation, identity, serial, snapshot, now := projectionFixture(t)
	if err := db.AcceptNodeSnapshot(ctx, identity, generation, serial, snapshot, now); err != nil {
		t.Fatal(err)
	}
	// Presence moved to a later boot/session; retained projections are history and
	// must remain archive-valid while runtime reads mark them stale.
	p := controlapi.Presence{StateEpoch: snapshot.StateEpoch, BootID: uuid.NewString(), BootSequence: 2, ApplicationVersion: "v1", ContractVersion: 1, BindingEpoch: 1, Capabilities: []string{"presence", "snapshot.v1"}}
	if _, err := db.RecordNodePresence(ctx, identity, generation, serial, p, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := VerifyControllerRegistrySnapshot(ctx, snapshotPath(t, db), snapshotControllerID, generation, snapshotCA(t, material)); err != nil {
		t.Fatalf("historical projection rejected: %v", err)
	}
	if _, err := db.sql.ExecContext(ctx, "DELETE FROM control_node_certificates WHERE node_id=?", identity.NodeID); err != nil {
		t.Fatal(err)
	}
	if err := VerifyControllerRegistrySnapshot(ctx, snapshotPath(t, db), snapshotControllerID, generation, snapshotCA(t, material)); err == nil {
		t.Fatal("projection without certificate provenance accepted")
	}
}

func TestNodeProjectionHidesCachedSnapshotAfterCapabilityDowngrade(t *testing.T) {
	ctx := context.Background()
	db, _, generation, identity, serial, snapshot, now := projectionFixture(t)
	if err := db.AcceptNodeSnapshot(ctx, identity, generation, serial, snapshot, now); err != nil {
		t.Fatal(err)
	}
	// The same live boot has lost snapshot support. Existing cached content must
	// not remain visible, including after its presence lease has expired.
	p := controlapi.Presence{StateEpoch: snapshot.StateEpoch, BootID: snapshot.BootID, BootSequence: snapshot.BootSequence, ApplicationVersion: "v1", ContractVersion: 1, BindingEpoch: identity.BindingEpoch, Capabilities: []string{"presence"}}
	if _, err := db.RecordNodePresence(ctx, identity, generation, serial, p, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{now.Add(time.Second), now.Add(nodePresenceTTL + time.Second)} {
		projection, err := db.NodeProjection(ctx, identity.ControllerID, generation, identity.NodeID, at)
		if err != nil {
			t.Fatal(err)
		}
		if projection.Node.Status != "incompatible" || projection.Available || projection.Desired != nil || projection.Observations != nil {
			t.Fatalf("cached projection exposed after downgrade at %s: %#v", at, projection)
		}
	}
}

func projectionFixture(t *testing.T) (*DB, controlpki.Material, string, NodeIdentity, string, controlapi.Snapshot, time.Time) {
	t.Helper()
	ctx := context.Background()
	db, material, generation, identity := populatedSnapshotSchema(t, 11, false)
	now := time.Now().UTC()
	var serial string
	if err := db.sql.QueryRowContext(ctx, "SELECT serial FROM control_node_certificates WHERE node_id=?", identity.NodeID).Scan(&serial); err != nil {
		t.Fatal(err)
	}
	p := controlapi.Presence{StateEpoch: uuid.NewString(), BootID: uuid.NewString(), BootSequence: 1, ApplicationVersion: "v1", ContractVersion: 1, BindingEpoch: 1, Capabilities: []string{"presence", "snapshot.v1"}}
	session, err := db.RecordNodePresence(ctx, identity, generation, serial, p, now)
	if err != nil {
		t.Fatal(err)
	}
	return db, material, generation, identity, serial, projectionSnapshot(session, p.StateEpoch, p.BootID, now), now
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
