package app

import (
	"context"
	"errors"
	"github.com/astronaut808/awg-forge/internal/sqldb"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/google/uuid"
)

func managedClientOperationService(t *testing.T) (*Service, config.State, config.Tunnel, config.Client) {
	t.Helper()
	svc := New(testServiceConfig(t))
	if _, err := svc.Init(); err != nil {
		t.Fatal(err)
	}
	tunnel, err := svc.CreateTunnel("awg_legacy_1_0", "node-awg", "10.42.0.0/24", 51842)
	if err != nil {
		t.Fatal(err)
	}
	client, err := svc.AddClientToTunnel(tunnel.ID, "node-client")
	if err != nil {
		t.Fatal(err)
	}
	state, err := svc.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	state.ManagedNode = &config.ManagedNodeState{NodeID: testNodeID, ControllerID: testControllerID, StateEpoch: testStateEpoch, BindingEpoch: 1}
	state.Mode = config.ModeNode
	if err := svc.store.Save(state); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.StartManagedNodeBoot(); err != nil {
		t.Fatal(err)
	}
	return svc, state, tunnel, client
}

func clientEnabledOperation(state config.State, tunnel, client string, enabled bool) controlapi.OperationEnvelope {
	return controlapi.OperationEnvelope{RefreshRequest: controlapi.RefreshRequest{OperationID: uuid.NewString(), IdempotencyKey: uuid.NewString(), StateEpoch: state.ManagedNode.StateEpoch, BindingEpoch: state.ManagedNode.BindingEpoch, Generation: state.ManagedNode.DesiredGeneration}, IssuedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Minute), Operation: controlapi.RefreshOperation{Kind: "client.enabled.set", SchemaVersion: 1, Payload: controlapi.ClientEnabledPayload{TunnelID: tunnel, ClientID: client, Enabled: &enabled}}}
}

func TestExecuteClientEnabledCommitsAndReplaysReceiptAfterLocalChange(t *testing.T) {
	svc, state, tunnel, client := managedClientOperationService(t)
	op := clientEnabledOperation(state, tunnel.ID, client.ID, false)
	first, err := svc.ExecuteClientEnabled(context.Background(), op)
	if err != nil || first.Status != "succeeded" || first.Result == nil || first.Result.Generation != 1 {
		t.Fatalf("first = %#v, %v", first, err)
	}
	persisted, err := svc.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ManagedNode.DesiredGeneration != 1 || len(persisted.ManagedNode.SuccessfulReceipts) != 1 {
		t.Fatalf("atomic state receipt missing: %#v", persisted.ManagedNode)
	}
	// Exercise the actual local mutation path between remote commit and replay.
	if err := svc.SetClientEnabled(client.ID, true); err != nil {
		t.Fatal(err)
	}
	replayed, err := svc.ExecuteClientEnabled(context.Background(), op)
	if err != nil || replayed.Status != "succeeded" || replayed.Result == nil || replayed.Result.Generation != 1 {
		t.Fatalf("replay = %#v, %v", replayed, err)
	}
	after, err := svc.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if after.ManagedNode.DesiredGeneration != 2 {
		t.Fatalf("replay mutated current state: %#v", after.ManagedNode)
	}
	changed := op
	enabled := true
	changed.Operation.Payload.Enabled = &enabled
	if _, err := svc.ExecuteClientEnabled(context.Background(), changed); !errors.Is(err, ErrOperationReplayConflict) {
		t.Fatalf("changed intent error = %v", err)
	}
}

func TestExecuteClientEnabledNoopDeadlineAndAcceptedRecovery(t *testing.T) {
	t.Run("noop consumes generation", func(t *testing.T) {
		svc, state, tunnel, client := managedClientOperationService(t)
		op := clientEnabledOperation(state, tunnel.ID, client.ID, true)
		out, err := svc.ExecuteClientEnabled(context.Background(), op)
		if err != nil || out.Status != "succeeded" || out.Result == nil || out.Result.Generation != 1 {
			t.Fatalf("out = %#v, %v", out, err)
		}
		got, err := svc.store.Load()
		if err != nil {
			t.Fatal(err)
		}
		if got.ManagedNode.DesiredGeneration != 1 {
			t.Fatalf("noop did not commit generation: %#v", got.ManagedNode)
		}
	})
	t.Run("deadline is durable failure", func(t *testing.T) {
		svc, state, tunnel, client := managedClientOperationService(t)
		op := clientEnabledOperation(state, tunnel.ID, client.ID, false)
		op.IssuedAt = time.Now().UTC().Add(-2 * time.Minute)
		op.ExpiresAt = op.IssuedAt.Add(time.Minute)
		out, err := svc.ExecuteClientEnabled(context.Background(), op)
		if err != nil || out.Status != "failed" || out.Code != "execution_expired" {
			t.Fatalf("out = %#v, %v", out, err)
		}
	})
	t.Run("accepted crash is failed closed", func(t *testing.T) {
		svc, state, tunnel, client := managedClientOperationService(t)
		op := clientEnabledOperation(state, tunnel.ID, client.ID, false)
		row := ClientOperationOutcome{ControllerID: testControllerID, NodeID: testNodeID, Operation: op, Status: "accepted"}
		if err := svc.store.SaveClientOperations([]ClientOperationOutcome{row}); err != nil {
			t.Fatal(err)
		}
		out, err := svc.ExecuteClientEnabled(context.Background(), op)
		if err != nil || out.Status != "failed" || out.Code != "interrupted_rolled_back" {
			t.Fatalf("out = %#v, %v", out, err)
		}
	})
}

func TestExecuteClientEnabledRejectsPreviousWorkerBootBeforeAcceptance(t *testing.T) {
	svc, state, tunnel, client := managedClientOperationService(t)
	op := clientEnabledOperation(state, tunnel.ID, client.ID, false)
	next := New(svc.cfg)
	if _, err := next.StartManagedNodeBoot(); err != nil {
		t.Fatal(err)
	}
	before, err := svc.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ExecuteClientEnabled(context.Background(), op); !errors.Is(err, ErrOperationReplayConflict) {
		t.Fatalf("old worker executed: %v", err)
	}
	after, err := svc.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("old worker changed desired state")
	}
	var rows []ClientOperationOutcome
	if err := svc.store.LoadClientOperations(&rows); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old worker persisted acceptance: %v", err)
	}
	if out, err := next.ExecuteClientEnabled(context.Background(), op); err != nil || out.Status != "succeeded" {
		t.Fatalf("current boot cannot execute: %v %v", out.Status, err)
	}
}

func TestExecuteClientEnabledQuotaMarkerAndStorageFailure(t *testing.T) {
	for _, failDB := range []bool{false, true} {
		t.Run(map[bool]string{false: "clear manual disable marker", true: "fail closed corrupt DB"}[failDB], func(t *testing.T) {
			svc, state, tunnel, client := managedClientOperationService(t)
			svc.cfg.DatabaseMode = sqldb.ModeSQLite
			svc.cfg.DatabasePath = filepath.Join(svc.cfg.ConfigDir, "history.db")
			svc.cfg.DatabaseBusyTimeout = time.Second
			svc.cfg.DatabaseQueryTimeout = 2 * time.Second
			svc.cfg.DatabaseMaxOpenConns = 1
			if failDB {
				if err := os.WriteFile(svc.cfg.DatabasePath, []byte("corrupt database"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if !failDB {
				db, err := sqldb.Open(context.Background(), svc.cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = db.Close() }()
				if err := db.Migrate(context.Background()); err != nil {
					t.Fatal(err)
				}
				limit := uint64(1000)
				if err := db.SetClientTrafficLimitWithPeriod(context.Background(), tunnel.ID, client.ID, &limit, sqldb.TrafficLimitPeriodRolling30Days); err != nil {
					t.Fatal(err)
				}
				if err := db.MarkClientTrafficLimitBlocked(context.Background(), tunnel.ID, client.ID, time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			op := clientEnabledOperation(state, tunnel.ID, client.ID, false)
			out, err := svc.ExecuteClientEnabled(context.Background(), op)
			if err != nil {
				t.Fatal(err)
			}
			if failDB {
				if out.Status != "failed" || out.Code != "apply_failed" {
					t.Fatalf("DB unavailable applied mutation: %#v", out)
				}
				assertManagedGeneration(t, svc, state.ManagedNode.DesiredGeneration)
				return
			}
			if out.Status != "succeeded" {
				t.Fatalf("disable failed: %#v", out)
			}
			blocked, err := sqldb.IsClientTrafficLimitBlocked(context.Background(), svc.cfg, tunnel.ID, client.ID)
			if err != nil || blocked {
				t.Fatalf("automatic release can undo manual disable: %v %v", blocked, err)
			}
		})
	}
}

func TestExecuteClientEnabledRollbackAndUnresolvedRecovery(t *testing.T) {
	for _, failRollback := range []bool{false, true} {
		t.Run(map[bool]string{false: "apply rollback", true: "unresolved rollback"}[failRollback], func(t *testing.T) {
			svc, state, tunnel, client := managedClientOperationService(t)
			svc.cfg.ApplyConfig = true
			calls := 0
			failure := errors.New("runtime apply failure")
			svc.runtimeOps.applyTunnel = func(config.Tunnel) error {
				calls++
				if calls == 1 || failRollback {
					return failure
				}
				return nil
			}
			op := clientEnabledOperation(state, tunnel.ID, client.ID, false)
			out, err := svc.ExecuteClientEnabled(context.Background(), op)
			if calls != 2 {
				t.Fatalf("apply/rollback count=%d", calls)
			}
			after, loadErr := svc.store.Load()
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if after.ManagedNode.DesiredGeneration != state.ManagedNode.DesiredGeneration || len(after.ManagedNode.SuccessfulReceipts) != 0 {
				t.Fatal("failed apply committed success")
			}
			_, journalErr := svc.store.LoadPendingDesiredStateCommit()
			if failRollback {
				if err == nil || out.Status != "accepted" || journalErr != nil {
					t.Fatalf("unresolved failure lost evidence: %v %s %v", err, out.Status, journalErr)
				}
			} else {
				if err != nil || out.Status != "failed" || out.Code != "apply_failed" || !errors.Is(journalErr, os.ErrNotExist) {
					t.Fatalf("rollback not durable failure: %v %s %v", err, out.Status, journalErr)
				}
			}
		})
	}
}

func TestExecuteClientEnabledCannotBypassExceededQuota(t *testing.T) {
	svc, _, tunnel, client := managedClientOperationService(t)
	if err := svc.SetClientEnabled(client.ID, false); err != nil {
		t.Fatal(err)
	}
	state, err := svc.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	svc.cfg.DatabaseMode = sqldb.ModeSQLite
	svc.cfg.DatabasePath = filepath.Join(svc.cfg.ConfigDir, "history.db")
	svc.cfg.DatabaseBusyTimeout = time.Second
	svc.cfg.DatabaseQueryTimeout = 2 * time.Second
	svc.cfg.DatabaseMaxOpenConns = 1
	db, err := sqldb.Open(context.Background(), svc.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	limit := uint64(100)
	if err := db.SetClientTrafficLimitWithPeriod(context.Background(), tunnel.ID, client.ID, &limit, sqldb.TrafficLimitPeriodLifetime); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	samples := []sqldb.TrafficSample{{SampledAt: now.Add(-time.Minute), TunnelID: tunnel.ID, ClientID: client.ID, Present: true}, {SampledAt: now, TunnelID: tunnel.ID, ClientID: client.ID, RxBytes: 200, Present: true}}
	if err := db.RecordTrafficSamples(context.Background(), samples); err != nil {
		t.Fatal(err)
	}
	op := clientEnabledOperation(state, tunnel.ID, client.ID, true)
	out, err := svc.ExecuteClientEnabled(context.Background(), op)
	if err != nil || out.Status != "failed" || out.Code != "quota_exceeded" {
		t.Fatalf("remote enable bypassed quota: %s %s %v", out.Status, out.Code, err)
	}
	assertManagedGeneration(t, svc, state.ManagedNode.DesiredGeneration)
}
