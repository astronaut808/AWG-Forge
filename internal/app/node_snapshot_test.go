package app

import (
	"context"
	"database/sql"
	"net/http"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/astronaut808/awg-forge/internal/sqldb"
	"github.com/google/uuid"
)

func TestPresenceStorageFailureIsRetryable(t *testing.T) {
	f := newControlLifecycleFixture(t)
	if err := f.service.EnableControl(context.Background(), f.token, f.receipt(t), true); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", f.cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	// Force a storage failure after authorization without changing authority.
	if _, err := db.Exec(`CREATE TRIGGER presence_storage_failure BEFORE INSERT ON control_node_presence BEGIN SELECT RAISE(ABORT, 'private storage diagnostic'); END`); err != nil {
		t.Fatal(err)
	}
	p := controlapi.Presence{BootID: uuid.NewString(), BootSequence: 1, ApplicationVersion: "test", ContractVersion: 1, StateEpoch: uuid.NewString(), BindingEpoch: 1, Capabilities: []string{"presence", "snapshot.v1"}, ObservedAt: time.Now().UTC()}
	var problem map[string]any
	enrollmentHTTP(t, f.client, "https://"+f.address, http.MethodPut, "/control/v1/node/presence", "", p, 503, &problem)
	if problem["code"] != "control_unavailable" || problem["title"] != "Control request failed" {
		t.Fatal("storage error was fenced or disclosed")
	}
	if _, err := db.Exec(`DROP TRIGGER presence_storage_failure`); err != nil {
		t.Fatal(err)
	}
	enrollmentHTTP(t, f.client, "https://"+f.address, http.MethodPut, "/control/v1/node/presence", "", p, 200, nil)
	p.BootID = uuid.NewString()
	enrollmentHTTP(t, f.client, "https://"+f.address, http.MethodPut, "/control/v1/node/presence", "", p, 409, nil)
}

func testObservationSnapshot(p controlapi.Presence, session string) controlapi.Snapshot {
	return controlapi.Snapshot{ContractVersion: 1, SessionID: session, StateEpoch: p.StateEpoch, BindingEpoch: p.BindingEpoch, BootID: p.BootID, BootSequence: p.BootSequence, DesiredGeneration: 3, Sequence: 1, ObservedAt: time.Now().UTC(),
		Desired:      controlapi.DesiredProjection{Tunnels: []controlapi.SnapshotTunnel{{ID: "0123456789abcdef", Name: "node-only", Interface: "awg20", Profile: "awg_2_0", Enabled: true, ListenPort: 51820, Revision: 7, Clients: []controlapi.SnapshotClient{{ID: "fedcba9876543210", Name: "phone", Enabled: true, Address: "10.20.0.2"}}}}},
		Observations: controlapi.Observations{Tunnels: []controlapi.TunnelObservation{{ID: "0123456789abcdef", Known: true, Up: true, Clients: []controlapi.ClientObservation{{ID: "fedcba9876543210", Present: true, RxBytes: 42}}}}, Doctor: controlapi.DoctorSummary{Scope: "runtime"}}}
}

// Real TLS and registry-backed mTLS, including a second request on keep-alive
// after revocation. Payload injection here supplements the real worker tests.
func TestNodeSnapshotMTLSFencesAndProjection(t *testing.T) {
	f := newControlLifecycleFixture(t)
	ctx := context.Background()
	if err := f.service.EnableControl(ctx, f.token, f.receipt(t), true); err != nil {
		t.Fatal(err)
	}
	base := "https://" + f.address
	p := controlapi.Presence{BootID: uuid.NewString(), BootSequence: 1, ApplicationVersion: "test", ContractVersion: 1, StateEpoch: uuid.NewString(), BindingEpoch: 1, DesiredGeneration: 3, Capabilities: []string{"presence", "snapshot.v1"}, ObservedAt: time.Now().UTC()}
	var accepted controlapi.PresenceAccepted
	enrollmentHTTP(t, f.client, base, http.MethodPut, "/control/v1/node/presence", "", p, 200, &accepted)
	s := testObservationSnapshot(p, accepted.SessionID)
	var ack controlapi.SnapshotAccepted
	enrollmentHTTP(t, f.client, base, http.MethodPut, "/control/v1/node/snapshot", "", s, 200, &ack)
	inv, err := f.service.ControllerNodes(ctx)
	if err != nil || len(inv.Nodes) != 1 || inv.Nodes[0].Status != "online" || inv.Nodes[0].LastConfirmedAt == nil {
		t.Fatal("inventory did not use live identity")
	}
	nodeID := inv.Nodes[0].NodeID
	view, err := f.service.ControllerNodeProjection(ctx, nodeID)
	if err != nil || !view.Available || view.Stale || view.Desired.Tunnels[0].Name != "node-only" || view.Observations.Tunnels[0].Clients[0].RxBytes != 42 {
		t.Fatal("wrong projection")
	}
	// Exact duplicate and lower sequence fail; same generation cannot alter desired.
	enrollmentHTTP(t, f.client, base, http.MethodPut, "/control/v1/node/snapshot", "", s, 409, nil)
	s.Sequence = 2
	s.Desired.Tunnels[0].Name = "substitution"
	enrollmentHTTP(t, f.client, base, http.MethodPut, "/control/v1/node/snapshot", "", s, 409, nil)
	s.Desired.Tunnels[0].Name = "node-only"
	s.Observations.Tunnels[0].Clients[0].RxBytes = 84
	enrollmentHTTP(t, f.client, base, http.MethodPut, "/control/v1/node/snapshot", "", s, 200, &ack)
	for _, fence := range []string{"session", "boot", "state", "binding", "generation", "version"} {
		candidate := s
		candidate.Sequence = 3
		switch fence {
		case "session":
			candidate.SessionID = uuid.NewString()
		case "boot":
			candidate.BootID = uuid.NewString()
		case "state":
			candidate.StateEpoch = uuid.NewString()
		case "binding":
			candidate.BindingEpoch++
		case "generation":
			candidate.DesiredGeneration--
		case "version":
			candidate.ContractVersion = 2
		}
		status := 409
		if fence == "version" {
			status = 400
		}
		enrollmentHTTP(t, f.client, base, http.MethodPut, "/control/v1/node/snapshot", "", candidate, status, nil)
	}
	unknown := map[string]any{"private_key": "secret-canary", "contract_version": 1}
	enrollmentHTTP(t, f.client, base, http.MethodPut, "/control/v1/node/snapshot", "", unknown, 400, nil)
	s.Sequence = 3
	s.DesiredGeneration++
	s.Desired.Tunnels[0].Name = "new-generation"
	enrollmentHTTP(t, f.client, base, http.MethodPut, "/control/v1/node/snapshot", "", s, 200, nil)
	previous := s
	p.BootID = uuid.NewString()
	p.BootSequence++
	enrollmentHTTP(t, f.client, base, http.MethodPut, "/control/v1/node/presence", "", p, 200, &accepted)
	view, err = f.service.ControllerNodeProjection(ctx, nodeID)
	if err != nil || !view.Stale {
		t.Fatal("pre-restart snapshot reported fresh")
	}
	enrollmentHTTP(t, f.client, base, http.MethodPut, "/control/v1/node/snapshot", "", previous, 409, nil)
	s.BootID = p.BootID
	s.BootSequence = p.BootSequence
	s.SessionID = accepted.SessionID
	s.Sequence = 1
	enrollmentHTTP(t, f.client, base, http.MethodPut, "/control/v1/node/snapshot", "", s, 200, nil)
	db, err := sqldb.Open(ctx, f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.RevokeNodeBinding(ctx, sqldb.NodeIdentity{NodeID: nodeID, ControllerID: inv.ControllerID, BindingEpoch: 1}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	s.Sequence++
	enrollmentHTTP(t, f.client, base, http.MethodPut, "/control/v1/node/snapshot", "", s, 403, nil)
	view, err = f.service.ControllerNodeProjection(ctx, nodeID)
	if err != nil || view.Node.Status != "revoked" || view.Available || view.Desired != nil {
		t.Fatal("revoked projection remained available")
	}
}
