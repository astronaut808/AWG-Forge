package nodeagent

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/google/uuid"
)

func TestRefreshWorkerForcedSnapshotAndLostAcknowledgement(t *testing.T) {
	var uploads atomic.Int32
	var results atomic.Int32
	var controller string
	epoch := uuid.NewString()
	session := uuid.NewString()
	operation := uuid.NewString()
	lease := uuid.NewString()
	var forced uint64
	seen := make(chan controlapi.OperationReceipt, 1)
	server, service, cfg, id := observationWorkerFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/control/v1/node/presence":
			var p controlapi.Presence
			_ = json.NewDecoder(r.Body).Decode(&p)
			epoch = p.StateEpoch
			_ = json.NewEncoder(w).Encode(controlapi.PresenceAccepted{ControllerID: controller, SessionID: session, SessionExpiresAt: time.Now().Add(time.Minute), NextPollSeconds: 1})
		case "/control/v1/node/snapshot":
			var snapshot controlapi.Snapshot
			_ = json.NewDecoder(r.Body).Decode(&snapshot)
			if snapshot.Refresh != nil {
				if snapshot.Refresh.OperationID != operation || snapshot.Refresh.LeaseToken != lease {
					t.Error("wrong refresh correlation")
				}
				forced = snapshot.Sequence
				uploads.Add(1)
			}
			_ = json.NewEncoder(w).Encode(controlapi.SnapshotAccepted{Sequence: snapshot.Sequence, ReceivedAt: time.Now()})
		case "/control/v1/node/operations/poll":
			_ = json.NewEncoder(w).Encode(controlapi.OperationDelivery{LeaseToken: lease, LeaseExpiresAt: time.Now().Add(35 * time.Second), Operation: controlapi.OperationEnvelope{RefreshRequest: controlapi.RefreshRequest{OperationID: operation, IdempotencyKey: uuid.NewString(), StateEpoch: epoch, BindingEpoch: 1}, IssuedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute), Operation: controlapi.RefreshOperation{Kind: "snapshot.refresh", SchemaVersion: 1}}})
		case "/control/v1/node/operations/" + operation + "/result":
			var receipt controlapi.OperationReceipt
			_ = json.NewDecoder(r.Body).Decode(&receipt)
			if results.Add(1) == 1 {
				w.WriteHeader(503)
				return
			}
			seen <- receipt
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
		}
	})
	controller = id
	defer server.Close()
	before, err := service.NodeObservationState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, service, cfg) }()
	select {
	case receipt := <-seen:
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if receipt.Validate() != nil || receipt.Status != "succeeded" || receipt.Result.Sequence != forced || uploads.Load() != 1 || results.Load() != 2 {
			t.Fatal("lost ack recollected or invalid result")
		}
	case <-ctx.Done():
		t.Fatal("refresh result not retried")
	}
	after, err := service.NodeObservationState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Tunnels) != 0 || after.ManagedNode.DesiredGeneration != before.ManagedNode.DesiredGeneration || after.ManagedNode.StateEpoch != before.ManagedNode.StateEpoch {
		t.Fatal("refresh mutated node state")
	}
}
