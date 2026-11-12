package sqldb

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/google/uuid"
)

var (
	ErrOperationFenced           = errors.New("operation fenced")
	ErrOperationLimit            = errors.New("operation limit")
	ErrOperationUnsupported      = errors.New("refresh unsupported")
	ErrOperationOffline          = errors.New("node unavailable")
	ErrOperationTunnelMissing    = errors.New("tunnel unavailable")
	ErrOperationClientMissing    = errors.New("client unavailable")
	ErrOperationResourceConflict = errors.New("resource conflict")
)

type refreshRow struct {
	id, controller, node, key, status, code, token string
	envelope, owner, receipt                       []byte
	issued, expires, lease, sequence               int64
	completed, acknowledged                        sql.NullInt64
}

const refreshColumns = `operation_id,controller_id,node_id,idempotency_key,envelope_json,status,code,issued_at_unix_ms,expires_at_unix_ms,completed_at_unix_ms,lease_token,lease_expires_at_unix_ms,owner_json,snapshot_sequence,receipt_json,node_acknowledged_at_unix_ms`

func scanRefresh(row *sql.Row) (r refreshRow, err error) {
	err = row.Scan(&r.id, &r.controller, &r.node, &r.key, &r.envelope, &r.status, &r.code, &r.issued, &r.expires, &r.completed, &r.token, &r.lease, &r.owner, &r.sequence, &r.receipt, &r.acknowledged)
	return
}
func (r refreshRow) intent() (o controlapi.OperationEnvelope, err error) {
	err = controlapi.DecodeOperation(r.envelope, &o)
	if err == nil {
		err = o.Validate()
	}
	return
}
func (r refreshRow) view(at ...time.Time) (controlapi.OperationStatus, error) {
	o, err := r.intent()
	if err != nil || validateRefreshRow(r, r.controller) != nil {
		return controlapi.OperationStatus{}, ErrOperationFenced
	}
	v := controlapi.OperationStatus{ControllerID: r.controller, NodeID: r.node, OperationID: r.id, StateEpoch: o.StateEpoch, BindingEpoch: o.BindingEpoch, Generation: o.Generation, Status: r.status, Code: r.code, IssuedAt: o.IssuedAt, ExpiresAt: o.ExpiresAt, Sequence: uint64(r.sequence)}
	if controlapi.IsClientMutation(o.Operation.Kind) {
		v.Kind = o.Operation.Kind
		now := time.Now()
		if len(at) > 0 {
			now = at[0]
		}
		if r.status == "leased" && !now.Before(o.ExpiresAt) {
			v.Status = "reconciling"
		}
		if r.status == "succeeded" {
			var receipt controlapi.OperationReceipt
			if controlapi.DecodeOperation(r.receipt, &receipt) != nil {
				return v, ErrOperationFenced
			}
			v.ClientResult = receipt.ClientResult
			v.CreateResult = receipt.CreateResult
			v.DeleteResult = receipt.DeleteResult
		}
	}
	if r.completed.Valid {
		t := time.UnixMilli(r.completed.Int64).UTC()
		v.CompletedAt = &t
	}
	return v, nil
}
func terminalRefresh(status string) bool { return status != "queued" && status != "leased" }
func maintainRefresh(ctx context.Context, tx *sql.Tx, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM control_refresh_operations WHERE expires_at_unix_ms < ? AND (json_extract(envelope_json,'$.operation.kind') NOT IN ('client.enabled.set','client.create','client.delete') OR lease_token='' OR node_acknowledged_at_unix_ms IS NOT NULL)`, now.Add(-controlapi.OperationRetention).UnixMilli()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE control_refresh_operations SET status='expired',code='operation_expired',completed_at_unix_ms=? WHERE status IN ('queued','leased') AND expires_at_unix_ms<=? AND (json_extract(envelope_json,'$.operation.kind') NOT IN ('client.enabled.set','client.create','client.delete') OR lease_token='')`, now.UnixMilli(), now.UnixMilli()); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE control_refresh_operations SET status='rejected',code='operation_fenced',completed_at_unix_ms=? WHERE status IN ('queued','leased') AND NOT EXISTS (SELECT 1 FROM control_node_bindings b WHERE b.node_id=control_refresh_operations.node_id AND b.controller_id=control_refresh_operations.controller_id AND b.revoked_at_unix_ms IS NULL AND b.binding_epoch=json_extract(control_refresh_operations.envelope_json,'$.expected_binding_epoch'))`, now.UnixMilli())
	return err
}

// CreateRefresh serializes intent replay, quotas and insertion in one immediate
// transaction. Only current online compatible projection authority can enqueue.
func (db *DB) CreateRefresh(ctx context.Context, controller, issuer, node string, r controlapi.RefreshRequest, now time.Time) (controlapi.OperationStatus, error) {
	return db.createOperation(ctx, controller, issuer, node, r, controlapi.RefreshOperation{Kind: "snapshot.refresh", SchemaVersion: 1}, now)
}
func (db *DB) CreateClientEnabled(ctx context.Context, controller, issuer, node string, r controlapi.ClientEnabledRequest, now time.Time) (controlapi.OperationStatus, error) {
	if r.Validate() != nil {
		return controlapi.OperationStatus{}, ErrOperationFenced
	}
	return db.createOperation(ctx, controller, issuer, node, r.RefreshRequest, controlapi.RefreshOperation{Kind: "client.enabled.set", SchemaVersion: 1, Payload: r.ClientEnabledPayload}, now)
}
func (db *DB) CreateClientMutation(ctx context.Context, controller, issuer, node string, r controlapi.RefreshRequest, kind string, p controlapi.ClientEnabledPayload, now time.Time) (controlapi.OperationStatus, error) {
	if !controlapi.IsClientMutation(kind) || !p.ValidFor(kind) {
		return controlapi.OperationStatus{}, ErrOperationFenced
	}
	return db.createOperation(ctx, controller, issuer, node, r, controlapi.RefreshOperation{Kind: kind, SchemaVersion: 1, Payload: p}, now)
}
func (db *DB) createOperation(ctx context.Context, controller, issuer, node string, r controlapi.RefreshRequest, operation controlapi.RefreshOperation, now time.Time) (controlapi.OperationStatus, error) {
	if db == nil || db.sql == nil {
		return controlapi.OperationStatus{}, ErrDisabled
	}
	if !validUUID(controller) || !validUUID(node) || !validIssuerGeneration(issuer) || r.Validate() != nil || now.IsZero() || (controlapi.OperationEnvelope{RefreshRequest: r, IssuedAt: now, ExpiresAt: now.Add(controlapi.OperationTTL), Operation: operation}).Validate() != nil {
		return controlapi.OperationStatus{}, ErrOperationFenced
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return controlapi.OperationStatus{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = maintainRefresh(ctx, tx, now); err != nil {
		return controlapi.OperationStatus{}, err
	}
	prior, err := scanRefresh(tx.QueryRowContext(ctx, `SELECT `+refreshColumns+` FROM control_refresh_operations WHERE operation_id=? OR (controller_id=? AND node_id=? AND idempotency_key=?) LIMIT 1`, r.OperationID, controller, node, r.IdempotencyKey))
	if err == nil {
		o, e := prior.intent()
		if e != nil || prior.controller != controller || prior.node != node || o.RefreshRequest != r || !reflect.DeepEqual(o.Operation, operation) {
			return controlapi.OperationStatus{}, ErrOperationFenced
		}
		v, e := prior.view(now)
		if e != nil {
			return v, e
		}
		return v, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return controlapi.OperationStatus{}, err
	}
	var epoch, boot, session, caps, serial string
	var binding, generation, sequence, bootSequence, expires int64
	var contract int
	err = tx.QueryRowContext(ctx, `SELECT p.state_epoch,p.boot_id,p.session_id,p.capabilities_json,p.certificate_serial,p.binding_epoch,x.desired_generation,x.snapshot_sequence,p.boot_sequence,p.expires_at_unix_ms,p.contract_version FROM control_node_presence p JOIN control_node_bindings b ON b.node_id=p.node_id JOIN control_node_projections x ON x.node_id=b.node_id WHERE b.node_id=? AND b.controller_id=? AND b.revoked_at_unix_ms IS NULL AND p.controller_id=b.controller_id AND p.binding_epoch=b.binding_epoch AND x.controller_id=b.controller_id AND x.binding_epoch=b.binding_epoch AND x.state_epoch=p.state_epoch AND x.boot_id=p.boot_id AND x.boot_sequence=p.boot_sequence AND x.session_id=p.session_id AND p.certificate_issuer_generation=?`, node, controller, issuer).Scan(&epoch, &boot, &session, &caps, &serial, &binding, &generation, &sequence, &bootSequence, &expires, &contract)
	if errors.Is(err, sql.ErrNoRows) {
		return controlapi.OperationStatus{}, ErrOperationOffline
	}
	if err != nil {
		return controlapi.OperationStatus{}, err
	}
	var capabilities []string
	if strictProjectionJSON([]byte(caps), &capabilities) != nil || contract != 1 {
		return controlapi.OperationStatus{}, ErrOperationUnsupported
	}
	supported := false
	for _, c := range capabilities {
		if c == controlapi.OperationCapability(operation.Kind) {
			supported = true
		}
	}
	if !supported {
		return controlapi.OperationStatus{}, ErrOperationUnsupported
	}
	p := controlapi.OperationPoll{SessionID: session, BootID: boot, BootSequence: uint64(bootSequence), StateEpoch: epoch, BindingEpoch: uint64(binding), Generation: uint64(generation)}
	if expires <= now.UnixMilli() {
		return controlapi.OperationStatus{}, ErrOperationOffline
	}
	if err = refreshAuthority(ctx, tx, NodeIdentity{ControllerID: controller, NodeID: node, BindingEpoch: uint64(binding)}, issuer, serial, p, now); err != nil {
		return controlapi.OperationStatus{}, err
	}
	if r.StateEpoch != epoch || r.BindingEpoch != uint64(binding) || r.Generation != uint64(generation) {
		return controlapi.OperationStatus{}, ErrOperationFenced
	}
	if controlapi.IsClientMutation(operation.Kind) {
		var desiredRaw []byte
		if err = tx.QueryRowContext(ctx, "SELECT desired_json FROM control_node_projections WHERE node_id=?", node).Scan(&desiredRaw); err != nil {
			return controlapi.OperationStatus{}, err
		}
		var desired controlapi.DesiredProjection
		if strictProjectionJSON(desiredRaw, &desired) != nil {
			return controlapi.OperationStatus{}, ErrOperationFenced
		}
		found := false
		tunnelFound := false
		collision := false
		for _, t := range desired.Tunnels {
			for _, c := range t.Clients {
				if c.ID == operation.Payload.ClientID {
					collision = true
				}
			}
			if t.ID == operation.Payload.TunnelID {
				tunnelFound = true
				for _, c := range t.Clients {
					if c.ID == operation.Payload.ClientID {
						found = true
					}
				}
			}
		}
		if operation.Kind == "client.enabled.set" && !found {
			return controlapi.OperationStatus{}, ErrOperationFenced
		}
		if !tunnelFound {
			return controlapi.OperationStatus{}, ErrOperationTunnelMissing
		}
		if operation.Kind == "client.create" {
			if collision {
				return controlapi.OperationStatus{}, ErrOperationResourceConflict
			}
		} else if !found {
			return controlapi.OperationStatus{}, ErrOperationClientMissing
		}
	}
	var total, nodeCount, active, recent, globalRecent int
	err = tx.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(node_id=?),0),COALESCE(sum(node_id=? AND status IN ('queued','leased')),0),COALESCE(sum(node_id=? AND issued_at_unix_ms>?),0),COALESCE(sum(issued_at_unix_ms>?),0) FROM control_refresh_operations`, node, node, node, now.Add(-5*time.Second).UnixMilli(), now.Add(-time.Minute).UnixMilli()).Scan(&total, &nodeCount, &active, &recent, &globalRecent)
	if err != nil {
		return controlapi.OperationStatus{}, err
	}
	if total >= controlapi.MaxOperations || nodeCount >= controlapi.MaxNodeOperations || active > 0 || recent > 0 || globalRecent >= 64 {
		return controlapi.OperationStatus{}, ErrOperationLimit
	}
	o := controlapi.OperationEnvelope{RefreshRequest: r, IssuedAt: now.UTC(), ExpiresAt: now.UTC().Add(controlapi.OperationTTL), Operation: operation}
	raw, err := json.Marshal(o)
	if err != nil {
		return controlapi.OperationStatus{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO control_refresh_operations(operation_id,controller_id,node_id,idempotency_key,envelope_json,status,issued_at_unix_ms,expires_at_unix_ms) VALUES(?,?,?,?,?,'queued',?,?)`, r.OperationID, controller, node, r.IdempotencyKey, raw, now.UnixMilli(), o.ExpiresAt.UnixMilli())
	if err != nil {
		return controlapi.OperationStatus{}, err
	}
	v := controlapi.OperationStatus{ControllerID: controller, NodeID: node, OperationID: r.OperationID, StateEpoch: r.StateEpoch, BindingEpoch: r.BindingEpoch, Generation: r.Generation, Status: "queued", IssuedAt: o.IssuedAt, ExpiresAt: o.ExpiresAt}
	if controlapi.IsClientMutation(operation.Kind) {
		v.Kind = operation.Kind
	}
	return v, tx.Commit()
}

// refreshAuthority is repeated inside every dequeue/snapshot/result transaction.
func refreshAuthority(ctx context.Context, tx *sql.Tx, id NodeIdentity, issuer, serial string, p controlapi.OperationPoll, now time.Time) error {
	if !validNodeIdentity(id) || !validIssuerGeneration(issuer) || serial == "" || p.Validate() != nil || p.BindingEpoch != id.BindingEpoch || now.IsZero() {
		return ErrOperationFenced
	}
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM control_node_presence p JOIN control_node_bindings b ON b.node_id=p.node_id JOIN control_node_certificates c ON c.node_id=b.node_id AND c.controller_id=b.controller_id AND c.binding_epoch=b.binding_epoch WHERE p.node_id=? AND b.controller_id=? AND b.binding_epoch=? AND b.revoked_at_unix_ms IS NULL AND p.controller_id=b.controller_id AND p.binding_epoch=b.binding_epoch AND p.session_id=? AND p.state_epoch=? AND p.boot_id=? AND p.boot_sequence=? AND p.expires_at_unix_ms>? AND p.certificate_issuer_generation=? AND p.certificate_serial=? AND p.contract_version=1 AND EXISTS(SELECT 1 FROM json_each(p.capabilities_json) WHERE value=?) AND c.issuer_generation=p.certificate_issuer_generation AND c.serial=p.certificate_serial AND c.revoked_at_unix_ms IS NULL AND c.not_before_unix_ms<=? AND c.not_after_unix_ms>? AND (c.superseded_at_unix_ms IS NULL OR c.superseded_at_unix_ms>?) AND EXISTS(SELECT 1 FROM controller_users WHERE singleton=1 AND disabled_at='')`, id.NodeID, id.ControllerID, id.BindingEpoch, p.SessionID, p.StateEpoch, p.BootID, p.BootSequence, now.UnixMilli(), issuer, serial, controlapi.RefreshCapability, now.UnixMilli(), now.UnixMilli(), now.UnixMilli()).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrOperationFenced
	}
	return err
}
func (db *DB) LeaseRefresh(ctx context.Context, id NodeIdentity, issuer, serial string, p controlapi.OperationPoll, now time.Time) (*controlapi.OperationDelivery, error) {
	if db == nil || db.sql == nil {
		return nil, ErrDisabled
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = refreshAuthority(ctx, tx, id, issuer, serial, p, now); err != nil {
		return nil, err
	}
	if err = maintainRefresh(ctx, tx, now); err != nil {
		return nil, err
	}
	row, err := scanRefresh(tx.QueryRowContext(ctx, `SELECT `+refreshColumns+` FROM control_refresh_operations WHERE node_id=? AND controller_id=? AND status IN ('queued','leased') AND (json_extract(envelope_json,'$.operation.kind')='snapshot.refresh' OR EXISTS(SELECT 1 FROM control_node_presence p,json_each(p.capabilities_json) c WHERE p.node_id=control_refresh_operations.node_id AND c.value=CASE json_extract(control_refresh_operations.envelope_json,'$.operation.kind') WHEN 'client.enabled.set' THEN 'client.enabled.set.v1' WHEN 'client.create' THEN 'client.create.v1' WHEN 'client.delete' THEN 'client.delete.v1' ELSE '' END)) ORDER BY issued_at_unix_ms,operation_id LIMIT 1`, id.NodeID, id.ControllerID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	o, err := row.intent()
	if err != nil {
		return nil, ErrOperationFenced
	}
	if o.StateEpoch != p.StateEpoch || o.BindingEpoch != p.BindingEpoch {
		return nil, ErrOperationFenced
	}
	owner := p
	owner.WaitSeconds = 0
	owner.Generation = 0
	raw, _ := json.Marshal(owner)
	token, lease := row.token, row.lease
	if row.status != "leased" || lease <= now.UnixMilli() || !bytes.Equal(row.owner, raw) {
		row.sequence = 0
		token = uuid.NewString()
		deadline := now.Add(controlapi.OperationLease)
		if o.ExpiresAt.Before(deadline) && (!controlapi.IsClientMutation(o.Operation.Kind) || now.Before(o.ExpiresAt)) {
			deadline = o.ExpiresAt
		}
		lease = deadline.UnixMilli()
		_, err = tx.ExecContext(ctx, `UPDATE control_refresh_operations SET status='leased',lease_token=?,lease_expires_at_unix_ms=?,owner_json=?,snapshot_sequence=0 WHERE operation_id=?`, token, lease, raw, row.id)
		if err != nil {
			return nil, err
		}
	}
	delivery := &controlapi.OperationDelivery{AcceptedSequence: uint64(row.sequence), LeaseToken: token, LeaseExpiresAt: time.UnixMilli(lease).UTC(), Operation: o, ReconciliationOnly: controlapi.IsClientMutation(o.Operation.Kind) && !now.Before(o.ExpiresAt)}
	return delivery, tx.Commit()
}

// Correlation is committed with the accepted projection, never from a result's
// assertion or an older periodic snapshot. No snapshot history is needed.
func correlateRefresh(ctx context.Context, tx *sql.Tx, id NodeIdentity, issuer, serial string, s controlapi.Snapshot, now time.Time) error {
	if s.Refresh == nil {
		return nil
	}
	p := controlapi.OperationPoll{SessionID: s.SessionID, BootID: s.BootID, BootSequence: s.BootSequence, StateEpoch: s.StateEpoch, BindingEpoch: s.BindingEpoch, Generation: s.DesiredGeneration}
	if err := refreshAuthority(ctx, tx, id, issuer, serial, p, now); err != nil {
		return err
	}
	row, err := scanRefresh(tx.QueryRowContext(ctx, `SELECT `+refreshColumns+` FROM control_refresh_operations WHERE operation_id=? AND controller_id=? AND node_id=?`, s.Refresh.OperationID, id.ControllerID, id.NodeID))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrOperationFenced
	}
	if err != nil {
		return err
	}
	o, err := row.intent()
	if err != nil {
		return ErrOperationFenced
	}
	owner := p
	owner.Generation = 0
	raw, _ := json.Marshal(owner)
	if row.status != "leased" || row.token != s.Refresh.LeaseToken || row.lease <= now.UnixMilli() || row.expires <= now.UnixMilli() || !bytes.Equal(row.owner, raw) || o.StateEpoch != s.StateEpoch || o.BindingEpoch != s.BindingEpoch || o.Generation != s.DesiredGeneration || o.Operation.Kind != "snapshot.refresh" {
		return ErrOperationFenced
	}
	// First accepted snapshot wins this lease's evidence; lost upload ack can be
	// recovered by its same worker without silently changing a terminal result.
	if row.sequence != 0 && row.sequence != int64(s.Sequence) {
		return ErrOperationFenced
	}
	_, err = tx.ExecContext(ctx, `UPDATE control_refresh_operations SET snapshot_sequence=? WHERE operation_id=?`, s.Sequence, row.id)
	return err
}
func (db *DB) CompleteRefresh(ctx context.Context, id NodeIdentity, issuer, serial, operation string, r controlapi.OperationReceipt, now time.Time) error {
	if db == nil || db.sql == nil {
		return ErrDisabled
	}
	if !validUUID(operation) || r.Validate() != nil {
		return ErrOperationFenced
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = refreshAuthority(ctx, tx, id, issuer, serial, r.OperationPoll, now); err != nil {
		return err
	}
	row, err := scanRefresh(tx.QueryRowContext(ctx, `SELECT `+refreshColumns+` FROM control_refresh_operations WHERE operation_id=? AND controller_id=? AND node_id=?`, operation, id.ControllerID, id.NodeID))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrOperationFenced
	}
	if err != nil {
		return err
	}
	o, err := row.intent()
	if err != nil {
		return ErrOperationFenced
	}
	if controlapi.IsClientMutation(o.Operation.Kind) {
		return completeClientEnabled(ctx, tx, row, o, r, now)
	}
	if r.IntentHash != "" || r.ClientResult != nil || r.CreateResult != nil || r.DeleteResult != nil {
		return ErrOperationFenced
	}
	raw, _ := json.Marshal(r)
	if row.expires <= now.Add(-controlapi.OperationRetention).UnixMilli() {
		return ErrOperationFenced
	}
	if terminalRefresh(row.status) {
		if bytes.Equal(row.receipt, raw) {
			return tx.Commit()
		}
		return ErrOperationFenced
	}
	owner := r.OperationPoll
	owner.WaitSeconds = 0
	owner.Generation = 0
	ownerRaw, _ := json.Marshal(owner)
	if row.status != "leased" || row.token != r.LeaseToken || row.lease <= now.UnixMilli() || row.expires <= now.UnixMilli() || !bytes.Equal(row.owner, ownerRaw) || o.StateEpoch != r.StateEpoch || o.BindingEpoch != r.BindingEpoch {
		return ErrOperationFenced
	}
	if r.Status == "succeeded" && (o.Generation != r.Generation || row.sequence == 0 || uint64(row.sequence) != r.Result.Sequence) {
		return ErrOperationFenced
	}
	if r.Status == "conflict" && o.Generation == r.Generation {
		return ErrOperationFenced
	}
	if r.Status == "failed" && o.Generation != r.Generation {
		return ErrOperationFenced
	}
	_, err = tx.ExecContext(ctx, `UPDATE control_refresh_operations SET status=?,code=?,receipt_json=?,completed_at_unix_ms=? WHERE operation_id=?`, r.Status, r.Code, raw, now.UnixMilli(), row.id)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (db *DB) RefreshStatus(ctx context.Context, controller, node, operation string, now time.Time) (controlapi.OperationStatus, error) {
	if db == nil || db.sql == nil {
		return controlapi.OperationStatus{}, ErrDisabled
	}
	if !validUUID(controller) || !validUUID(node) || !validUUID(operation) {
		return controlapi.OperationStatus{}, ErrOperationFenced
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return controlapi.OperationStatus{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = maintainRefresh(ctx, tx, now); err != nil {
		return controlapi.OperationStatus{}, err
	}
	row, err := scanRefresh(tx.QueryRowContext(ctx, `SELECT `+refreshColumns+` FROM control_refresh_operations WHERE operation_id=? AND controller_id=? AND node_id=?`, operation, controller, node))
	if err != nil {
		return controlapi.OperationStatus{}, err
	}
	v, err := row.view(now)
	if err != nil {
		return v, err
	}
	return v, tx.Commit()
}

func validateRefreshRow(r refreshRow, controller string) error {
	o, err := r.intent()
	if err != nil || !validUUID(r.id) || !validUUID(r.node) || r.controller != controller || o.OperationID != r.id || o.IdempotencyKey != r.key || o.IssuedAt.UnixMilli() != r.issued || o.ExpiresAt.UnixMilli() != r.expires || len(r.envelope) > controlapi.MaxOperationBytes || len(r.owner) > controlapi.MaxOperationBytes || len(r.receipt) > controlapi.MaxOperationBytes {
		return ErrOperationFenced
	}
	if r.acknowledged.Valid && (!controlapi.IsClientMutation(o.Operation.Kind) || !r.completed.Valid || len(r.receipt) == 0 || r.acknowledged.Int64 < r.completed.Int64) {
		return ErrOperationFenced
	}
	switch r.status {
	case "queued", "leased", "succeeded", "conflict", "failed", "expired", "rejected":
	default:
		return ErrOperationFenced
	}
	if terminalRefresh(r.status) != r.completed.Valid || r.completed.Valid && r.completed.Int64 < r.issued {
		return ErrOperationFenced
	}
	if r.status == "queued" && (r.token != "" || r.lease != 0 || len(r.owner) != 0 || r.sequence != 0 || len(r.receipt) != 0) {
		return ErrOperationFenced
	}
	if r.token == "" {
		if r.status != "queued" && r.status != "expired" && r.status != "rejected" || r.lease != 0 || len(r.owner) != 0 || r.sequence != 0 {
			return ErrOperationFenced
		}
	} else {
		var owner controlapi.OperationPoll
		if !validUUID(r.token) || controlapi.DecodeOperation(r.owner, &owner) != nil || owner.Validate() != nil || owner.WaitSeconds != 0 || owner.Generation != 0 || owner.StateEpoch != o.StateEpoch || owner.BindingEpoch != o.BindingEpoch || r.lease <= r.issued || r.lease > r.expires && !controlapi.IsClientMutation(o.Operation.Kind) {
			return ErrOperationFenced
		}
	}
	if r.status == "succeeded" || r.status == "conflict" || r.status == "failed" {
		var receipt controlapi.OperationReceipt
		if controlapi.DecodeOperation(r.receipt, &receipt) != nil || receipt.Validate() != nil || receipt.Status != r.status || receipt.Code != r.code || receipt.LeaseToken != r.token && !controlapi.IsClientMutation(o.Operation.Kind) || receipt.StateEpoch != o.StateEpoch || receipt.BindingEpoch != o.BindingEpoch {
			return ErrOperationFenced
		}
		owner := receipt.OperationPoll
		owner.Generation = 0
		raw, _ := json.Marshal(owner)
		if controlapi.IsClientMutation(o.Operation.Kind) {
			if validateClientOutcome(o, receipt) != nil || r.sequence != 0 {
				return ErrOperationFenced
			}
		} else if !bytes.Equal(raw, r.owner) || receipt.IntentHash != "" || receipt.ClientResult != nil || receipt.CreateResult != nil || receipt.DeleteResult != nil || r.status == "succeeded" && (receipt.Generation != o.Generation || r.sequence == 0 || receipt.Result.Sequence != uint64(r.sequence)) || r.status == "conflict" && receipt.Generation == o.Generation || r.status == "failed" && receipt.Generation != o.Generation {
			return ErrOperationFenced
		}
	} else if len(r.receipt) != 0 || r.status == "expired" && r.code != "operation_expired" || r.status == "rejected" && r.code != "operation_fenced" || (r.status == "queued" || r.status == "leased") && r.code != "" {
		return ErrOperationFenced
	}
	return nil
}
func verifySnapshotRefresh(ctx context.Context, db *sql.DB, controller string, schema int) error {
	var rows *sql.Rows
	var err error
	if schema == 12 {
		rows, err = db.QueryContext(ctx, `SELECT operation_id,controller_id,node_id,idempotency_key,envelope_json,status,code,issued_at_unix_ms,expires_at_unix_ms,completed_at_unix_ms,lease_token,lease_expires_at_unix_ms,owner_json,snapshot_sequence,receipt_json,NULL FROM control_refresh_operations`)
	} else {
		rows, err = db.QueryContext(ctx, `SELECT `+refreshColumns+` FROM control_refresh_operations`)
	}
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	count := 0
	nodes := map[string]int{}
	active := map[string]int{}
	for rows.Next() {
		var r refreshRow
		if err = rows.Scan(&r.id, &r.controller, &r.node, &r.key, &r.envelope, &r.status, &r.code, &r.issued, &r.expires, &r.completed, &r.token, &r.lease, &r.owner, &r.sequence, &r.receipt, &r.acknowledged); err != nil {
			return err
		}
		if validateRefreshRow(r, controller) != nil {
			return ErrOperationFenced
		}
		if schema == 12 {
			o, _ := r.intent()
			if o.Operation.Kind != "snapshot.refresh" {
				return ErrOperationFenced
			}
		}
		count++
		nodes[r.node]++
		if !terminalRefresh(r.status) {
			active[r.node]++
		}
		if count > controlapi.MaxOperations || nodes[r.node] > controlapi.MaxNodeOperations || active[r.node] > 1 {
			return ErrOperationLimit
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	_ = rows.Close()
	// Historical binding epochs may be revoked, but never belong to another
	// controller, a future epoch or an identity lacking certificate provenance.
	var invalid int
	err = db.QueryRowContext(ctx, `SELECT count(*) FROM control_refresh_operations o JOIN control_node_bindings b ON b.node_id=o.node_id WHERE b.controller_id!=o.controller_id OR b.binding_epoch<json_extract(o.envelope_json,'$.expected_binding_epoch') OR NOT EXISTS (SELECT 1 FROM control_node_certificates c WHERE c.node_id=o.node_id AND c.controller_id=o.controller_id AND c.binding_epoch=json_extract(o.envelope_json,'$.expected_binding_epoch'))`).Scan(&invalid)
	if err != nil {
		return err
	}
	if invalid != 0 {
		return ErrOperationFenced
	}
	return nil
}
