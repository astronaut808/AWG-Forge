package app

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/astronaut808/awg-forge/internal/sqldb"
	"github.com/google/uuid"
)

func TestRefreshRealMTLSDeliveryCorrelationDisableAndRevocation(t *testing.T) {
	f := newControlLifecycleFixture(t)
	ctx := context.Background()
	if err := f.service.EnableControl(ctx, f.token, f.receipt(t), true); err != nil {
		t.Fatal(err)
	}
	base := "https://" + f.address
	p := controlapi.Presence{BootID: uuid.NewString(), BootSequence: 1, ApplicationVersion: "test", ContractVersion: 1, StateEpoch: uuid.NewString(), BindingEpoch: 1, DesiredGeneration: 3, Capabilities: []string{"presence", "snapshot.v1", controlapi.RefreshCapability}, ObservedAt: time.Now().UTC()}
	var presence controlapi.PresenceAccepted
	enrollmentHTTP(t, f.client, base, http.MethodPut, "/control/v1/node/presence", "", p, 200, &presence)
	snapshot := testObservationSnapshot(p, presence.SessionID)
	enrollmentHTTP(t, f.client, base, http.MethodPut, "/control/v1/node/snapshot", "", snapshot, 200, nil)
	inv, err := f.service.ControllerNodes(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	node := inv.Nodes[0].NodeID
	intent := controlapi.RefreshRequest{OperationID: uuid.NewString(), IdempotencyKey: uuid.NewString(), StateEpoch: p.StateEpoch, BindingEpoch: 1, Generation: 3}
	if _, err := f.service.CreateNodeRefresh(ctx, node, intent); err != nil {
		t.Fatal(err)
	}
	poll := controlapi.OperationPoll{SessionID: presence.SessionID, BootID: p.BootID, BootSequence: 1, StateEpoch: p.StateEpoch, BindingEpoch: 1, Generation: 3}
	var delivery controlapi.OperationDelivery
	enrollmentHTTP(t, f.client, base, http.MethodPost, "/control/v1/node/operations/poll", "", poll, 200, &delivery)
	// A browser cookie/forwarded identity can never replace mTLS.
	noCert := f.tls.Clone()
	noCert.Certificates = nil
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: noCert}, Timeout: time.Second}
	defer client.CloseIdleConnections()
	enrollmentHTTP(t, client, base, http.MethodPost, "/control/v1/node/operations/poll", f.token, poll, 403, nil)
	receipt := controlapi.OperationReceipt{OperationPoll: poll, LeaseToken: delivery.LeaseToken, Status: "succeeded", Code: "snapshot_accepted", Result: &controlapi.RefreshResult{SnapshotAccepted: true, Sequence: 2}}
	route := "/control/v1/node/operations/" + intent.OperationID + "/result"
	enrollmentHTTP(t, f.client, base, http.MethodPut, route, "", receipt, 409, nil)
	snapshot.Sequence = 2
	snapshot.Refresh = &controlapi.RefreshCorrelation{OperationID: intent.OperationID, LeaseToken: delivery.LeaseToken}
	enrollmentHTTP(t, f.client, base, http.MethodPut, "/control/v1/node/snapshot", "", snapshot, 200, nil)
	enrollmentHTTP(t, f.client, base, http.MethodPut, route, "", receipt, 204, nil)
	enrollmentHTTP(t, f.client, base, http.MethodPut, route, "", receipt, 204, nil)
	receipt.Result.Sequence++
	enrollmentHTTP(t, f.client, base, http.MethodPut, route, "", receipt, 409, nil)
	enrollmentHTTP(t, f.client, base, http.MethodPut, "/control/v1/node/operations/"+uuid.NewString()+"/result", "", receipt, 409, nil)
	for _, path := range []string{route + "/", "/control/v1/node/operations/not-a-uuid/result", "/control/v1/node/operations/" + intent.OperationID + "%2f/result", route + "?x=1"} {
		enrollmentHTTP(t, f.client, base, http.MethodPut, path, "", receipt, 404, nil)
	}
	db, err := sqldb.Open(ctx, f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.RevokeNodeBinding(ctx, sqldb.NodeIdentity{ControllerID: inv.ControllerID, NodeID: node, BindingEpoch: 1}, time.Now()); err != nil {
		t.Fatal(err)
	}
	enrollmentHTTP(t, f.client, base, http.MethodPost, "/control/v1/node/operations/poll", "", poll, 403, nil)
	enrollmentHTTP(t, f.client, base, http.MethodPut, route, "", receipt, 403, nil)
	if err := f.service.DisableControl(ctx, f.token); err != nil {
		t.Fatal(err)
	}
}

func TestRefreshPollDoesNotHoldMutationLock(t *testing.T) {
	f := newControlLifecycleFixture(t)
	ctx := context.Background()
	if err := f.service.EnableControl(ctx, f.token, f.receipt(t), true); err != nil {
		t.Fatal(err)
	}
	p := controlapi.Presence{BootID: uuid.NewString(), BootSequence: 1, ApplicationVersion: "test", ContractVersion: 1, StateEpoch: uuid.NewString(), BindingEpoch: 1, DesiredGeneration: 3, Capabilities: []string{"snapshot.v1", controlapi.RefreshCapability}, ObservedAt: time.Now()}
	var presence controlapi.PresenceAccepted
	base := "https://" + f.address
	enrollmentHTTP(t, f.client, base, http.MethodPut, "/control/v1/node/presence", "", p, 200, &presence)
	snapshot := testObservationSnapshot(p, presence.SessionID)
	enrollmentHTTP(t, f.client, base, http.MethodPut, "/control/v1/node/snapshot", "", snapshot, 200, nil)
	inv, err := f.service.ControllerNodes(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	poll := controlapi.OperationPoll{SessionID: presence.SessionID, BootID: p.BootID, BootSequence: 1, StateEpoch: p.StateEpoch, BindingEpoch: 1, Generation: 3, WaitSeconds: 5}
	done := make(chan struct{})
	go func() {
		defer close(done)
		enrollmentHTTP(t, f.client, base, http.MethodPost, "/control/v1/node/operations/poll", "", poll, 200, nil)
	}()
	time.Sleep(150 * time.Millisecond)
	start := time.Now()
	intent := controlapi.RefreshRequest{OperationID: uuid.NewString(), IdempotencyKey: uuid.NewString(), StateEpoch: p.StateEpoch, BindingEpoch: 1, Generation: 3}
	if _, err := f.service.CreateNodeRefresh(ctx, inv.Nodes[0].NodeID, intent); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("held poll blocked browser admission")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("held poll failed to deliver")
	}
}
