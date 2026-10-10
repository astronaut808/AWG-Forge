package sqldb

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlapi"
)

var ErrSnapshotFenced = errors.New("snapshot fenced")
var ErrProjectionLimit = errors.New("node projection limit reached")

// AcceptNodeSnapshot atomically rechecks registry, live session and all fences.
// Only one bounded projection per identity is retained; no snapshot history.
func (db *DB) AcceptNodeSnapshot(ctx context.Context, identity NodeIdentity, issuer, serial string, s controlapi.Snapshot, now time.Time) error {
	if db == nil || db.sql == nil {
		return ErrDisabled
	}
	if !validNodeIdentity(identity) || s.Validate() != nil || s.BindingEpoch != identity.BindingEpoch || !validIssuerGeneration(issuer) || serial == "" || now.IsZero() || s.ObservedAt.Before(now.Add(-5*time.Minute)) || s.ObservedAt.After(now.Add(5*time.Minute)) {
		return ErrSnapshotFenced
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var admitted int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM control_node_presence p
JOIN control_node_bindings b ON b.node_id = p.node_id
JOIN control_node_certificates c ON c.node_id = b.node_id AND c.controller_id = b.controller_id AND c.binding_epoch = b.binding_epoch
WHERE p.node_id = ? AND b.controller_id = ? AND b.binding_epoch = ? AND b.revoked_at_unix_ms IS NULL
AND p.controller_id = b.controller_id AND p.binding_epoch = b.binding_epoch
AND p.state_epoch = ? AND p.boot_id = ? AND p.boot_sequence = ? AND p.session_id = ? AND p.expires_at_unix_ms > ?
AND p.certificate_issuer_generation = ? AND p.certificate_serial = ?
AND p.contract_version=1 AND EXISTS(SELECT 1 FROM json_each(p.capabilities_json) WHERE value='snapshot.v1')
AND c.issuer_generation = p.certificate_issuer_generation AND c.serial = p.certificate_serial
AND c.revoked_at_unix_ms IS NULL AND c.not_before_unix_ms <= ? AND c.not_after_unix_ms > ?
AND (c.superseded_at_unix_ms IS NULL OR c.superseded_at_unix_ms > ?)
AND EXISTS (SELECT 1 FROM controller_users WHERE singleton = 1 AND disabled_at = '')`,
		identity.NodeID, identity.ControllerID, identity.BindingEpoch, s.StateEpoch, s.BootID, s.BootSequence, s.SessionID, now.UnixMilli(), issuer, serial, now.UnixMilli(), now.UnixMilli(), now.UnixMilli()).Scan(&admitted)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrSnapshotFenced
	}
	if err != nil {
		return err
	}
	desired, err := json.Marshal(s.Desired)
	if err != nil {
		return err
	}
	observations, err := json.Marshal(s.Observations)
	if err != nil {
		return err
	}
	var epoch, boot, session string
	var binding, bootSequence, generation, sequence int64
	var priorDesired []byte
	err = tx.QueryRowContext(ctx, `SELECT binding_epoch, state_epoch, boot_id, boot_sequence, session_id, desired_generation, snapshot_sequence, desired_json FROM control_node_projections WHERE node_id = ?`, identity.NodeID).Scan(&binding, &epoch, &boot, &bootSequence, &session, &generation, &sequence, &priorDesired)
	if err == nil {
		if binding != int64(s.BindingEpoch) || epoch != s.StateEpoch || s.DesiredGeneration < uint64(generation) || s.BootSequence < uint64(bootSequence) || s.BootSequence == uint64(bootSequence) && (boot != s.BootID || s.Sequence <= uint64(sequence)) {
			return ErrSnapshotFenced
		}
		// A new session or boot can refresh observations but cannot rewrite the
		// committed desired projection at the same generation.
		if s.DesiredGeneration == uint64(generation) && !bytes.Equal(desired, priorDesired) {
			return ErrSnapshotFenced
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM control_node_projections`).Scan(&count); err != nil {
			return err
		}
		if count >= controlapi.MaxProjectedNodes {
			return ErrProjectionLimit
		}
	} else {
		return err
	}
	var storedBytes int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(sum(length(desired_json)+length(observations_json)),0) FROM control_node_projections WHERE node_id != ?`, identity.NodeID).Scan(&storedBytes); err != nil {
		return err
	}
	if storedBytes+int64(len(desired)+len(observations)) > controlapi.MaxProjectionStorageBytes {
		return ErrProjectionLimit
	}

	_, err = tx.ExecContext(ctx, `INSERT INTO control_node_projections
(node_id, controller_id, binding_epoch, state_epoch, boot_id, boot_sequence, session_id, desired_generation, snapshot_sequence, observed_at_unix_ms, received_at_unix_ms, desired_json, observations_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(node_id) DO UPDATE SET controller_id=excluded.controller_id, binding_epoch=excluded.binding_epoch, state_epoch=excluded.state_epoch,
boot_id=excluded.boot_id, boot_sequence=excluded.boot_sequence, session_id=excluded.session_id, desired_generation=excluded.desired_generation,
snapshot_sequence=excluded.snapshot_sequence, observed_at_unix_ms=excluded.observed_at_unix_ms, received_at_unix_ms=excluded.received_at_unix_ms,
desired_json=excluded.desired_json, observations_json=excluded.observations_json`, identity.NodeID, identity.ControllerID, s.BindingEpoch, s.StateEpoch, s.BootID, s.BootSequence, s.SessionID, s.DesiredGeneration, s.Sequence, s.ObservedAt.UnixMilli(), now.UnixMilli(), desired, observations)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// inventoryQuery counts reachability only for a live exact certificate-bound
// presence on the current binding. Display names never deduplicate identities.
const inventoryQuery = `SELECT b.node_id, COALESCE((SELECT e.requested_name FROM control_enrollments e WHERE e.node_id=b.node_id AND e.status='approved' ORDER BY e.created_at_unix_ms DESC LIMIT 1), 'node'),
b.binding_epoch, b.revoked_at_unix_ms, COALESCE(p.confirmed_at_unix_ms,0), COALESCE(p.application_version,''), COALESCE(p.contract_version,0), COALESCE(p.capabilities_json,'[]'), COALESCE(p.state_epoch,''), COALESCE(p.boot_id,''),
CASE WHEN p.expires_at_unix_ms > ? AND p.controller_id=b.controller_id AND p.binding_epoch=b.binding_epoch
AND EXISTS (SELECT 1 FROM control_node_certificates c WHERE c.node_id=b.node_id AND c.controller_id=b.controller_id AND c.binding_epoch=b.binding_epoch
AND c.issuer_generation=? AND c.issuer_generation=p.certificate_issuer_generation AND c.serial=p.certificate_serial
AND c.revoked_at_unix_ms IS NULL AND c.not_before_unix_ms<=? AND c.not_after_unix_ms>?
AND (c.superseded_at_unix_ms IS NULL OR c.superseded_at_unix_ms>?)) THEN 1 ELSE 0 END
FROM control_node_bindings b LEFT JOIN control_node_presence p ON p.node_id=b.node_id
WHERE b.controller_id=? AND (?='' OR b.node_id=?) ORDER BY b.node_id LIMIT 1025`

func (db *DB) NodeInventory(ctx context.Context, controllerID, issuer, nodeID string, now time.Time) (controlapi.NodeInventory, error) {
	out := controlapi.NodeInventory{ControllerID: controllerID, Nodes: []controlapi.NodeView{}}
	if db == nil || db.sql == nil {
		return out, ErrDisabled
	}
	if !validUUID(controllerID) || !validIssuerGeneration(issuer) || nodeID != "" && !validUUID(nodeID) {
		return out, ErrNodeCertificateDenied
	}
	rows, err := db.sql.QueryContext(ctx, inventoryQuery, now.UnixMilli(), issuer, now.UnixMilli(), now.UnixMilli(), now.UnixMilli(), controllerID, nodeID, nodeID)
	if err != nil {
		return out, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var n controlapi.NodeView
		var revoked sql.NullInt64
		var confirmed int64
		var live int
		var caps string
		if err := rows.Scan(&n.NodeID, &n.Name, &n.BindingEpoch, &revoked, &confirmed, &n.ApplicationVersion, &n.ContractVersion, &caps, &n.StateEpoch, &n.BootID, &live); err != nil {
			return out, err
		}
		n.Capabilities = []string{}
		if len(caps) > 8577 || strictProjectionJSON([]byte(caps), &n.Capabilities) != nil {
			return out, ErrSnapshotFenced
		}
		legacyMetadata := n.ApplicationVersion == "" && n.ContractVersion == 0 && len(n.Capabilities) == 0 && confirmed == 0
		confirmedMetadata := controlapi.ValidObservationMetadata(n.ApplicationVersion, n.ContractVersion, n.Capabilities) && confirmed > 0
		if !legacyMetadata && !confirmedMetadata {
			return out, ErrSnapshotFenced
		}
		if confirmed > 0 {
			t := time.UnixMilli(confirmed).UTC()
			n.LastConfirmedAt = &t
		}
		n.Status = "offline"
		if live == 1 {
			n.Status = "online"
		}
		if revoked.Valid {
			n.Status = "revoked"
		} else if n.ContractVersion != 1 || !hasSnapshotCapability(n.Capabilities) {
			n.Status = "incompatible"
		}
		out.Nodes = append(out.Nodes, n)
		if len(out.Nodes) > controlapi.MaxProjectedNodes {
			return out, ErrProjectionLimit
		}
	}
	return out, rows.Err()
}

func hasSnapshotCapability(caps []string) bool {
	for _, c := range caps {
		if c == "snapshot.v1" {
			return true
		}
	}
	return false
}

func (db *DB) NodeProjection(ctx context.Context, controllerID, issuer, nodeID string, now time.Time) (controlapi.NodeProjection, error) {
	inv, err := db.NodeInventory(ctx, controllerID, issuer, nodeID, now)
	if err != nil {
		return controlapi.NodeProjection{}, err
	}
	if len(inv.Nodes) != 1 {
		return controlapi.NodeProjection{}, sql.ErrNoRows
	}
	out := controlapi.NodeProjection{ControllerID: controllerID, Node: inv.Nodes[0], Stale: true}
	// Revoked and incompatible identities cannot expose a former projection.
	if out.Node.Status == "revoked" || out.Node.ContractVersion != 1 || !hasSnapshotCapability(out.Node.Capabilities) {
		return out, nil
	}
	var observed, received int64
	var desired, observations []byte
	var binding, bootSequence int64
	var session string
	err = db.sql.QueryRowContext(ctx, `SELECT state_epoch, boot_id, boot_sequence, session_id, binding_epoch, desired_generation, snapshot_sequence, observed_at_unix_ms, received_at_unix_ms, desired_json, observations_json FROM control_node_projections WHERE node_id=? AND controller_id=?`, nodeID, controllerID).Scan(&out.StateEpoch, &out.BootID, &bootSequence, &session, &binding, &out.DesiredGeneration, &out.Sequence, &observed, &received, &desired, &observations)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if binding != int64(out.Node.BindingEpoch) {
		return out, nil
	}
	var d controlapi.DesiredProjection
	var o controlapi.Observations
	if decodeStoredProjection(desired, observations, &d, &o, controlapi.Snapshot{ContractVersion: 1, SessionID: session, StateEpoch: out.StateEpoch, BindingEpoch: uint64(binding), BootID: out.BootID, BootSequence: uint64(bootSequence), DesiredGeneration: out.DesiredGeneration, Sequence: out.Sequence, ObservedAt: time.UnixMilli(observed).UTC()}, received) != nil {
		return out, ErrSnapshotFenced
	}
	obs, rec := time.UnixMilli(observed).UTC(), time.UnixMilli(received).UTC()
	out.ObservedAt, out.ReceivedAt = &obs, &rec
	out.Desired, out.Observations, out.Available = &d, &o, true
	var current int
	err = db.sql.QueryRowContext(ctx, `SELECT count(*) FROM control_node_presence WHERE node_id=? AND state_epoch=? AND boot_id=? AND boot_sequence=? AND session_id=? AND binding_epoch=?`, nodeID, out.StateEpoch, out.BootID, bootSequence, session, binding).Scan(&current)
	if err != nil {
		return out, err
	}
	out.Stale = out.Node.Status != "online" || current != 1 || !rec.After(now.Add(-nodePresenceTTL))
	return out, nil
}

func strictProjectionJSON(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		return ErrSnapshotFenced
	}
	return nil
}
func decodeStoredProjection(desired, observations []byte, d *controlapi.DesiredProjection, o *controlapi.Observations, s controlapi.Snapshot, received int64) error {
	if len(desired)+len(observations) > controlapi.MaxSnapshotBytes || strictProjectionJSON(desired, d) != nil || strictProjectionJSON(observations, o) != nil || received <= 0 || s.ObservedAt.UnixMilli() <= 0 || s.ObservedAt.Before(time.UnixMilli(received).Add(-5*time.Minute)) || s.ObservedAt.After(time.UnixMilli(received).Add(5*time.Minute)) {
		return ErrSnapshotFenced
	}
	s.Desired, s.Observations = *d, *o
	return s.Validate()
}
