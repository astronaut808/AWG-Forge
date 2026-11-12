package sqldb

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/google/uuid"
)

func TestClientConfigExportAuthorityAllFences(t *testing.T) {
	ctx := context.Background()
	db, _, issuer, id, serial, s, now := refreshFixture(t)
	p := controlapi.Presence{BootID: s.BootID, BootSequence: s.BootSequence, StateEpoch: s.StateEpoch, BindingEpoch: s.BindingEpoch, DesiredGeneration: s.DesiredGeneration, ApplicationVersion: "test", ContractVersion: 1, Capabilities: []string{"presence", "snapshot.v1", controlapi.ClientConfigExportCapability}}
	session, err := db.RecordNodePresence(ctx, id, issuer, serial, p, now)
	if err != nil {
		t.Fatal(err)
	}
	s.SessionID = session
	s.Sequence++
	s.DesiredGeneration++
	s.Desired.Tunnels = []controlapi.SnapshotTunnel{{ID: "aaaaaaaaaaaaaaaa", Name: "tunnel", Interface: "awg0", Profile: "awg_2_0", Enabled: true, ListenPort: 51820, Revision: 3, Clients: []controlapi.SnapshotClient{{ID: "bbbbbbbbbbbbbbbb", Name: "phone", Enabled: true, Address: "10.1.0.2"}}}}
	s.Observations.Tunnels = []controlapi.TunnelObservation{{ID: "aaaaaaaaaaaaaaaa", Clients: []controlapi.ClientObservation{{ID: "bbbbbbbbbbbbbbbb"}}}}
	if db.AcceptNodeSnapshot(ctx, id, issuer, serial, s, now) != nil {
		t.Fatal("snapshot fixture")
	}
	r := controlapi.ClientConfigExportRequest{StateEpoch: s.StateEpoch, BindingEpoch: s.BindingEpoch, Generation: s.DesiredGeneration, TunnelID: "aaaaaaaaaaaaaaaa", ClientID: "bbbbbbbbbbbbbbbb", TunnelRevision: 3}
	a, err := db.ClientConfigExportAuthority(ctx, id.ControllerID, issuer, id.NodeID, r, now)
	if err != nil || a.Poll != refreshPoll(s) || a.Serial != serial {
		t.Fatal("exact authority")
	}
	for _, field := range []string{"epoch", "binding", "generation", "revision", "association"} {
		bad := r
		switch field {
		case "epoch":
			bad.StateEpoch = uuid.NewString()
		case "binding":
			bad.BindingEpoch++
		case "generation":
			bad.Generation++
		case "revision":
			bad.TunnelRevision++
		case "association":
			bad.TunnelID = "other"
		}
		if _, err := db.ClientConfigExportAuthority(ctx, id.ControllerID, issuer, id.NodeID, bad, now); err == nil {
			t.Fatal("request fence", field)
		}
	}
	if _, err := db.ClientConfigExportAuthority(ctx, id.ControllerID, issuer, id.NodeID, r, now.Add(nodePresenceTTL+time.Second)); err == nil {
		t.Fatal("stale")
	}
	// Each current-authority change must fence even before the next snapshot.
	for _, pair := range []struct {
		name, change, restore string
		args, undo            []any
	}{
		{"session", "UPDATE control_node_presence SET session_id=?", "UPDATE control_node_presence SET session_id=?", []any{uuid.NewString()}, []any{s.SessionID}},
		{"boot", "UPDATE control_node_presence SET boot_id=?", "UPDATE control_node_presence SET boot_id=?", []any{uuid.NewString()}, []any{s.BootID}},
		{"boot-sequence", "UPDATE control_node_presence SET boot_sequence=?", "UPDATE control_node_presence SET boot_sequence=?", []any{s.BootSequence + 1}, []any{s.BootSequence}},
		{"capability", "UPDATE control_node_presence SET capabilities_json=?", "UPDATE control_node_presence SET capabilities_json=?", []any{`["snapshot.v1"]`}, []any{string(mustExportJSON(t, p.Capabilities))}},
		{"renewal", "UPDATE control_node_certificates SET superseded_at_unix_ms=?", "UPDATE control_node_certificates SET superseded_at_unix_ms=NULL", []any{now.Add(time.Hour).UnixMilli()}, nil},
		{"revoked", "UPDATE control_node_certificates SET revoked_at_unix_ms=?", "UPDATE control_node_certificates SET revoked_at_unix_ms=NULL", []any{now.UnixMilli()}, nil},
		{"cert-serial", "UPDATE control_node_presence SET certificate_serial=?", "UPDATE control_node_presence SET certificate_serial=?", []any{"other"}, []any{serial}},
		{"disabled-admin", "UPDATE controller_users SET disabled_at=?", "UPDATE controller_users SET disabled_at=''", []any{now.Format(time.RFC3339Nano)}, nil},
	} {
		if _, err := db.sql.ExecContext(ctx, pair.change, pair.args...); err != nil {
			t.Fatal(pair.name, err)
		}
		if _, err := db.ClientConfigExportAuthority(ctx, id.ControllerID, issuer, id.NodeID, r, now); err == nil {
			t.Fatal("authority fence", pair.name)
		}
		if _, err := db.sql.ExecContext(ctx, pair.restore, pair.undo...); err != nil {
			t.Fatal(err)
		}
	}
	if db.RevokeNodeBinding(ctx, id, now) != nil {
		t.Fatal("revoke")
	}
	if _, err := db.ClientConfigExportAuthority(ctx, id.ControllerID, issuer, id.NodeID, r, now); err == nil {
		t.Fatal("binding revoked")
	}
}

func mustExportJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
