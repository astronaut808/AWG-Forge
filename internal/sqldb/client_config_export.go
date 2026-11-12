package sqldb

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlapi"
)

type ClientConfigExportAuthority struct {
	ControllerID string
	Issuer       string
	Poll         controlapi.OperationPoll
	Serial       string
}

// Read only. The caller holds the existing cross-process state mutation lock.
// Unlike generic observations, export authority excludes predecessor overlap.
func (db *DB) ClientConfigExportAuthority(ctx context.Context, controller, issuer, node string, request controlapi.ClientConfigExportRequest, now time.Time) (ClientConfigExportAuthority, error) {
	var out ClientConfigExportAuthority
	out.ControllerID, out.Issuer = controller, issuer
	if request.Validate() != nil || !validUUID(node) || !validUUID(controller) || !validIssuerGeneration(issuer) || now.IsZero() {
		return out, ErrOperationFenced
	}
	view, err := db.NodeProjection(ctx, controller, issuer, node, now)
	if err != nil {
		return out, err
	}
	if !view.Available || view.Stale || view.Desired == nil || view.Node.Status != "online" {
		return out, ErrOperationOffline
	}
	if view.StateEpoch != request.StateEpoch || view.Node.BindingEpoch != request.BindingEpoch || view.DesiredGeneration != request.Generation {
		return out, ErrOperationFenced
	}
	target := false
	for _, tunnel := range view.Desired.Tunnels {
		if tunnel.ID != request.TunnelID {
			continue
		}
		if tunnel.Revision != request.TunnelRevision {
			return out, ErrOperationFenced
		}
		for _, client := range tunnel.Clients {
			if client.ID == request.ClientID {
				target = true
				break
			}
		}
	}
	if !target {
		return out, ErrOperationClientMissing
	}
	err = db.sql.QueryRowContext(ctx, `SELECT p.session_id, p.boot_id, p.boot_sequence, p.state_epoch, p.binding_epoch, q.desired_generation, p.certificate_serial
FROM control_node_presence p
JOIN control_node_bindings b ON b.node_id=p.node_id AND b.controller_id=p.controller_id AND b.binding_epoch=p.binding_epoch
JOIN control_node_certificates c ON c.node_id=b.node_id AND c.controller_id=b.controller_id AND c.binding_epoch=b.binding_epoch
JOIN control_node_projections q ON q.node_id=p.node_id AND q.controller_id=p.controller_id AND q.binding_epoch=p.binding_epoch AND q.state_epoch=p.state_epoch AND q.boot_id=p.boot_id AND q.boot_sequence=p.boot_sequence AND q.session_id=p.session_id
WHERE b.controller_id=? AND b.node_id=? AND b.binding_epoch=? AND b.revoked_at_unix_ms IS NULL
AND p.state_epoch=? AND q.desired_generation=? AND p.boot_id=? AND p.expires_at_unix_ms>? AND q.received_at_unix_ms>?
AND p.contract_version=1 AND EXISTS(SELECT 1 FROM json_each(p.capabilities_json) WHERE value='client.config.export.v1')
AND c.issuer_generation=? AND c.issuer_generation=p.certificate_issuer_generation AND c.serial=p.certificate_serial
AND c.revoked_at_unix_ms IS NULL AND c.superseded_at_unix_ms IS NULL AND c.not_before_unix_ms<=? AND c.not_after_unix_ms>?
AND EXISTS(SELECT 1 FROM controller_users WHERE singleton=1 AND disabled_at='')`, controller, node, request.BindingEpoch, request.StateEpoch, request.Generation, view.BootID, now.UnixMilli(), now.Add(-nodePresenceTTL).UnixMilli(), issuer, now.UnixMilli(), now.UnixMilli()).Scan(&out.Poll.SessionID, &out.Poll.BootID, &out.Poll.BootSequence, &out.Poll.StateEpoch, &out.Poll.BindingEpoch, &out.Poll.Generation, &out.Serial)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrOperationFenced
	}
	if err != nil {
		return out, err
	}
	if out.Poll.Validate() != nil {
		return out, ErrOperationFenced
	}
	return out, nil
}
