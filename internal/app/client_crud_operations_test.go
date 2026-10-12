package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/astronaut808/awg-forge/internal/sqldb"
)

func TestClientCRUDDeletePolicyCleanupAndReplay(t *testing.T) {
	svc, state, tunnel, client := managedClientOperationService(t)
	svc.cfg.DatabaseMode = sqldb.ModeSQLite
	svc.cfg.DatabasePath = filepath.Join(svc.cfg.ConfigDir, "history.db")
	svc.cfg.DatabaseBusyTimeout = time.Second
	svc.cfg.DatabaseQueryTimeout = 2 * time.Second
	svc.cfg.DatabaseMaxOpenConns = 1
	db, err := sqldb.Open(context.Background(), svc.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	limit := uint64(1000)
	if err := db.SetClientTrafficLimit(context.Background(), tunnel.ID, client.ID, &limit); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := db.RecordTrafficSamples(context.Background(), []sqldb.TrafficSample{{SampledAt: now.Add(-time.Minute), TunnelID: tunnel.ID, ClientID: client.ID, Present: true}, {SampledAt: now, TunnelID: tunnel.ID, ClientID: client.ID, RxBytes: 100, Present: true}}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// Cleanup failure must not turn a durable deletion into failure. Replay
	// retries the same idempotent cleanup once the operational DB is available.
	goodDB := svc.cfg.DatabasePath
	svc.cfg.DatabasePath = filepath.Join(svc.cfg.ConfigDir, "corrupt.db")
	if err := os.WriteFile(svc.cfg.DatabasePath, []byte("corrupt database"), 0600); err != nil {
		t.Fatal(err)
	}
	op := clientCRUDOperation(state, tunnel.ID, client.ID, "client.delete")
	out, err := svc.ExecuteClientOperation(context.Background(), op)
	if err != nil || out.Status != "succeeded" {
		t.Fatalf("delete %s %v", out.Status, err)
	}
	svc.cfg.DatabasePath = goodDB
	limits, err := sqldb.ListClientTrafficLimits(context.Background(), svc.cfg)
	if err != nil || len(limits) != 1 {
		t.Fatalf("failed cleanup unexpectedly erased policy: %v", err)
	}
	if _, err := svc.ExecuteClientOperation(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	limits, err = sqldb.ListClientTrafficLimits(context.Background(), svc.cfg)
	if err != nil || len(limits) != 0 {
		t.Fatal("replay did not retry cleanup")
	}
	history, err := sqldb.ListTrafficSummary(context.Background(), svc.cfg, time.Now())
	if err != nil || len(history) != 1 || history[0].ClientID != client.ID {
		t.Fatalf("history lost: %v", err)
	}
}

func TestClientCRUDUnconfirmedSuccessRequiresAtomicReceipt(t *testing.T) {
	for _, kind := range []string{"client.create", "client.delete", "client.enabled.set"} {
		for _, corruption := range []string{"missing", "intent", "idempotency", "generation"} {
			t.Run(kind+"/"+corruption, func(t *testing.T) {
				svc, state, tunnel, c := managedClientOperationService(t)
				op := clientEnabledOperation(state, tunnel.ID, c.ID, false)
				if kind != "client.enabled.set" {
					id := c.ID
					if kind == "client.create" {
						id = "5023456789abcdef"
					}
					op = clientCRUDOperation(state, tunnel.ID, id, kind)
				}
				if out, err := svc.ExecuteClientOperation(context.Background(), op); err != nil || out.Status != "succeeded" {
					t.Fatalf("commit: %s %v", out.Status, err)
				}
				st, err := svc.store.Load()
				if err != nil {
					t.Fatal(err)
				}
				switch corruption {
				case "missing":
					st.ManagedNode.SuccessfulReceipts = nil
				case "intent":
					st.ManagedNode.SuccessfulReceipts[0].IntentHash = hashIdempotencyKey("different-intent")
				case "idempotency":
					st.ManagedNode.SuccessfulReceipts[0].IdempotencyKeyHash = hashIdempotencyKey("different-key")
				case "generation":
					st.ManagedNode.SuccessfulReceipts[0].DesiredGeneration++
					st.ManagedNode.DesiredGeneration++
				}
				if err := svc.store.Save(st); err != nil {
					t.Fatal(err)
				}
				if _, err := svc.ExecuteClientOperation(context.Background(), op); err == nil {
					t.Fatal("unproven replay accepted")
				}
				if _, err := svc.PendingClientResults(context.Background()); err == nil {
					t.Fatal("unproven pending result accepted")
				}
			})
		}
	}
}

func TestClientCRUDConfirmedOutcomeSurvivesReceiptPruningCrash(t *testing.T) {
	svc, state, tunnel, _ := managedClientOperationService(t)
	op := clientCRUDOperation(state, tunnel.ID, "6023456789abcdef", "client.create")
	out, err := svc.ExecuteClientOperation(context.Background(), op)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	out.Confirmed, out.AcknowledgedAt = true, &now
	if err := svc.store.SaveClientOperations([]ClientOperationOutcome{out}); err != nil {
		t.Fatal(err)
	}
	st, _ := svc.store.Load()
	st.ManagedNode.SuccessfulReceipts = nil
	if err := svc.store.Save(st); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ExecuteClientOperation(context.Background(), op); err != nil {
		t.Fatalf("confirmed replay: %v", err)
	}
	if _, err := svc.PendingClientResults(context.Background()); err != nil {
		t.Fatalf("confirmed pending: %v", err)
	}
}

func clientCRUDOperation(state config.State, tunnel, client, kind string) controlapi.OperationEnvelope {
	o := clientEnabledOperation(state, tunnel, client, true)
	o.Operation.Kind = kind
	o.Operation.Payload.Enabled = nil
	if kind == "client.create" {
		o.Operation.Payload.Name = "remote phone"
	} else {
		v := true
		o.Operation.Payload.Confirmed = &v
	}
	return o
}
func TestExecuteClientCRUDAtomicReplayAndNoResurrection(t *testing.T) {
	svc, state, tunnel, other := managedClientOperationService(t)
	create := clientCRUDOperation(state, tunnel.ID, "1023456789abcdef", "client.create")
	first, err := svc.ExecuteClientOperation(context.Background(), create)
	if err != nil || first.CreateResult == nil || first.CreateResult.Generation != 1 {
		t.Fatalf("create: %s %v", first.Status, err)
	}
	st, _ := svc.store.Load()
	_, created, found := findClient(st, create.Operation.Payload.ClientID)
	if !found || !created.Enabled || created.PrivateKey == "" || created.PresharedKey == "" || created.IPv4Address == other.IPv4Address {
		t.Fatal("invalid created resource")
	}
	_, unaffected, _ := findClient(st, other.ID)
	if !reflect.DeepEqual(other, unaffected) {
		t.Fatal("unaffected client changed")
	}
	replay, err := svc.ExecuteClientOperation(context.Background(), create)
	after, _ := svc.store.Load()
	if err != nil || !reflect.DeepEqual(first, replay) || !reflect.DeepEqual(st, after) {
		t.Fatal("create replay changed commit")
	}
	del := clientCRUDOperation(st, tunnel.ID, created.ID, "client.delete")
	deleted, err := svc.ExecuteClientOperation(context.Background(), del)
	if err != nil || deleted.DeleteResult == nil || !deleted.DeleteResult.Deleted || deleted.DeleteResult.Generation != 2 {
		t.Fatalf("delete: %s %v", deleted.Status, err)
	}
	st, _ = svc.store.Load()
	if _, _, ok := findClient(st, created.ID); ok {
		t.Fatal("client retained")
	}
	local, err := svc.AddClientToTunnel(tunnel.ID, "local replacement")
	if err != nil {
		t.Fatal(err)
	}
	if local.IPv4Address != created.IPv4Address {
		t.Fatal("IPv4 allocation not reused")
	}
	before, _ := svc.store.Load()
	// Remove transport outcomes to prove reconstruction comes only from atomic
	// receipts and full immutable intent, after original resource is absent.
	if err := os.Remove(svc.cfg.ConfigDir + "/.client-operations.json"); err != nil {
		t.Fatal(err)
	}
	for _, o := range []controlapi.OperationEnvelope{create, del} {
		out, err := svc.ExecuteClientOperation(context.Background(), o)
		if err != nil || !out.receipt().MatchesClientSuccess(o) {
			t.Fatalf("original replay: %s %v", out.Status, err)
		}
	}
	after, _ = svc.store.Load()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("receipt replay resurrected or mutated client")
	}
}
func TestExecuteClientCRUDFencesMissingCollisionAndInterrupted(t *testing.T) {
	for _, kind := range []string{"client.create", "client.delete"} {
		t.Run(kind, func(t *testing.T) {
			svc, state, tunnel, c := managedClientOperationService(t)
			op := clientCRUDOperation(state, tunnel.ID, c.ID, kind)
			code := "resource_conflict"
			if kind == "client.delete" {
				op.Operation.Payload.ClientID = "0123456789abcdef"
				code = "client_unavailable"
			}
			out, err := svc.ExecuteClientOperation(context.Background(), op)
			if err != nil || out.Code != code {
				t.Fatalf("missing/collision %s %v", out.Code, err)
			}
			assertManagedGeneration(t, svc, 0)
			op = clientCRUDOperation(state, "missing", "0123456789abcdef", kind)
			out, err = svc.ExecuteClientOperation(context.Background(), op)
			if err != nil || out.Code != "tunnel_unavailable" {
				t.Fatalf("wrong tunnel %s %v", out.Code, err)
			}
			op = clientCRUDOperation(state, tunnel.ID, "2223456789abcdef", kind)
			if err := svc.store.SaveClientOperations([]ClientOperationOutcome{{ControllerID: testControllerID, NodeID: testNodeID, Operation: op, Status: "accepted"}}); err != nil {
				t.Fatal(err)
			}
			out, err = svc.ExecuteClientOperation(context.Background(), op)
			if err != nil || out.Code != "interrupted_rolled_back" {
				t.Fatalf("interrupted %s %v", out.Code, err)
			}
			next := New(svc.cfg)
			if _, err := next.StartManagedNodeBoot(); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.ExecuteClientOperation(context.Background(), op); !errors.Is(err, ErrOperationReplayConflict) {
				t.Fatal("old worker not fenced")
			}
		})
	}
}
func TestExecuteClientCRUDRuntimeRollback(t *testing.T) {
	for _, kind := range []string{"client.create", "client.delete"} {
		t.Run(kind, func(t *testing.T) {
			svc, state, tunnel, c := managedClientOperationService(t)
			svc.cfg.ApplyConfig = true
			id := c.ID
			if kind == "client.create" {
				id = "3023456789abcdef"
			}
			before, _ := svc.store.Load()
			calls := 0
			svc.runtimeOps.applyTunnel = func(config.Tunnel) error {
				calls++
				if calls == 1 {
					return errors.New("injected apply failure")
				}
				return nil
			}
			out, err := svc.ExecuteClientOperation(context.Background(), clientCRUDOperation(state, tunnel.ID, id, kind))
			if err != nil || out.Code != "apply_failed" || calls != 2 {
				t.Fatalf("rollback %s %v calls=%d", out.Code, err, calls)
			}
			after, _ := svc.store.Load()
			if !reflect.DeepEqual(before, after) {
				t.Fatal("failed runtime committed desired state")
			}
		})
	}
}
