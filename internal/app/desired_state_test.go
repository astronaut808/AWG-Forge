package app

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/storage"
)

const (
	testNodeID             = "11111111-1111-4111-8111-111111111111"
	testControllerID       = "22222222-2222-4222-8222-222222222222"
	testStateEpoch         = "33333333-3333-4333-8333-333333333333"
	testOperationID        = "44444444-4444-4444-8444-444444444444"
	mutationIdempotencyKey = "test-idempotency-key"
)

func TestCommitDesiredStatePersistsGenerationAndReceiptTogether(t *testing.T) {
	svc, initial := managedStateService(t)
	applyCalls := 0
	result, err := svc.commitDesiredState(desiredStateMutation{
		Request: testDesiredMutationRequest(),
		Mutate: func(candidate *config.State) error {
			candidate.Tunnels[0].DNS = "9.9.9.9"
			return nil
		},
		Apply: func(previous, candidate config.State) error {
			applyCalls++
			if previous.Tunnels[0].DNS == candidate.Tunnels[0].DNS {
				t.Fatal("candidate mutation was not passed to apply")
			}
			persistedDuringApply, err := svc.store.Load()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(persistedDuringApply, previous) {
				t.Fatal("candidate state was persisted before runtime apply completed")
			}
			if _, err := svc.store.LoadPendingDesiredStateCommit(); err != nil {
				t.Fatalf("commit journal is not durable before apply: %v", err)
			}
			journal, err := os.ReadFile(svc.store.PendingDesiredStateCommitPath())
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{previous.SessionSecret, previous.Tunnels[0].ServerPrivateKey, mutationIdempotencyKey} {
				if strings.Contains(string(journal), secret) {
					t.Fatalf("commit journal contains secret value")
				}
			}
			return nil
		},
		Rollback: func(config.State, config.State) error {
			t.Fatal("rollback called for successful commit")
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Replayed {
		t.Fatal("first commit was reported as replayed")
	}
	if applyCalls != 1 {
		t.Fatalf("apply calls = %d, want 1", applyCalls)
	}
	if result.Receipt.DesiredGeneration != initial.ManagedNode.DesiredGeneration+1 {
		t.Fatalf("receipt generation = %d, want %d", result.Receipt.DesiredGeneration, initial.ManagedNode.DesiredGeneration+1)
	}
	if result.Receipt.IdempotencyKeyHash == "" || strings.Contains(result.Receipt.IdempotencyKeyHash, "test-idempotency-key") {
		t.Fatalf("idempotency key was not safely hashed: %q", result.Receipt.IdempotencyKeyHash)
	}

	persisted, err := svc.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Tunnels[0].DNS != "9.9.9.9" {
		t.Fatalf("persisted DNS = %q, want 9.9.9.9", persisted.Tunnels[0].DNS)
	}
	if persisted.ManagedNode.DesiredGeneration != result.Receipt.DesiredGeneration {
		t.Fatalf("persisted generation = %d, want %d", persisted.ManagedNode.DesiredGeneration, result.Receipt.DesiredGeneration)
	}
	if len(persisted.ManagedNode.SuccessfulReceipts) != 1 || persisted.ManagedNode.SuccessfulReceipts[0] != result.Receipt {
		t.Fatalf("persisted receipts = %#v, want committed receipt", persisted.ManagedNode.SuccessfulReceipts)
	}
	if _, err := svc.store.LoadPendingDesiredStateCommit(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("commit journal remains after success: %v", err)
	}
}

func TestCommitDesiredStateReplayDoesNotReapply(t *testing.T) {
	svc, _ := managedStateService(t)
	mutation := desiredStateMutation{
		Request: testDesiredMutationRequest(),
		Mutate: func(candidate *config.State) error {
			candidate.Tunnels[0].DNS = "9.9.9.9"
			return nil
		},
		Apply:    func(config.State, config.State) error { return nil },
		Rollback: func(config.State, config.State) error { return nil },
	}
	first, err := svc.commitDesiredState(mutation)
	if err != nil {
		t.Fatal(err)
	}

	mutation.Mutate = func(*config.State) error {
		t.Fatal("mutation ran during replay")
		return nil
	}
	mutation.Apply = func(config.State, config.State) error {
		t.Fatal("apply ran during replay")
		return nil
	}
	mutation.Rollback = func(config.State, config.State) error {
		t.Fatal("rollback ran during replay")
		return nil
	}
	replayed, err := svc.commitDesiredState(mutation)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || replayed.Receipt != first.Receipt {
		t.Fatalf("replay result = %#v, want original receipt", replayed)
	}

	mutation.Request.IdempotencyKey = "different-key"
	if _, err := svc.commitDesiredState(mutation); !errors.Is(err, ErrOperationReplayConflict) {
		t.Fatalf("conflicting replay error = %v, want %v", err, ErrOperationReplayConflict)
	}
	mutation.Request.OperationID = "55555555-5555-4555-8555-555555555555"
	mutation.Request.IdempotencyKey = mutationIdempotencyKey
	if _, err := svc.commitDesiredState(mutation); !errors.Is(err, ErrOperationReplayConflict) {
		t.Fatalf("reused idempotency key error = %v, want %v", err, ErrOperationReplayConflict)
	}
}

func TestCommitDesiredStateRejectsStaleAuthorityBeforeMutation(t *testing.T) {
	tests := []struct {
		name   string
		change func(*DesiredStateMutationRequest)
		want   error
	}{
		{name: "state epoch", change: func(request *DesiredStateMutationRequest) {
			request.ExpectedStateEpoch = "55555555-5555-4555-8555-555555555555"
		}, want: ErrStateEpochMismatch},
		{name: "binding epoch", change: func(request *DesiredStateMutationRequest) {
			request.ExpectedBindingEpoch++
		}, want: ErrBindingEpochMismatch},
		{name: "desired generation", change: func(request *DesiredStateMutationRequest) {
			request.ExpectedDesiredGeneration++
		}, want: ErrDesiredGenerationMismatch},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			svc, initial := managedStateService(t)
			request := testDesiredMutationRequest()
			test.change(&request)
			_, err := svc.commitDesiredState(desiredStateMutation{
				Request: request,
				Mutate: func(*config.State) error {
					t.Fatal("mutation ran for stale request")
					return nil
				},
				Apply: func(config.State, config.State) error {
					t.Fatal("apply ran for stale request")
					return nil
				},
				Rollback: func(config.State, config.State) error {
					t.Fatal("rollback ran for stale request")
					return nil
				},
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			assertStateEqual(t, svc, initial)
		})
	}
}

func TestCommitDesiredStateRequiresExplicitManagedNodeState(t *testing.T) {
	cfg := testServiceConfig(t)
	svc := New(cfg)
	if _, err := svc.Init(); err != nil {
		t.Fatal(err)
	}
	initial, err := svc.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	request := testDesiredMutationRequest()
	_, err = svc.commitDesiredState(desiredStateMutation{
		Request:  request,
		Mutate:   func(*config.State) error { return nil },
		Apply:    func(config.State, config.State) error { return nil },
		Rollback: func(config.State, config.State) error { return nil },
	})
	if !errors.Is(err, ErrNodeNotManaged) {
		t.Fatalf("error = %v, want %v", err, ErrNodeNotManaged)
	}
	assertStateEqual(t, svc, initial)
}

func TestValidateDesiredStateMutationRejectsInvalidFences(t *testing.T) {
	tests := []struct {
		name   string
		change func(*desiredStateMutation)
	}{
		{name: "non-canonical operation ID", change: func(mutation *desiredStateMutation) {
			mutation.Request.OperationID = "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"
		}},
		{name: "nil operation ID", change: func(mutation *desiredStateMutation) {
			mutation.Request.OperationID = "00000000-0000-0000-0000-000000000000"
		}},
		{name: "empty idempotency key", change: func(mutation *desiredStateMutation) {
			mutation.Request.IdempotencyKey = " \t"
		}},
		{name: "oversized idempotency key", change: func(mutation *desiredStateMutation) {
			mutation.Request.IdempotencyKey = strings.Repeat("x", maxDesiredStateIdempotencyKeyLength+1)
		}},
		{name: "non-canonical state epoch", change: func(mutation *desiredStateMutation) {
			mutation.Request.ExpectedStateEpoch = "urn:uuid:" + testStateEpoch
		}},
		{name: "zero binding epoch", change: func(mutation *desiredStateMutation) {
			mutation.Request.ExpectedBindingEpoch = 0
		}},
		{name: "missing mutate callback", change: func(mutation *desiredStateMutation) {
			mutation.Mutate = nil
		}},
		{name: "missing apply callback", change: func(mutation *desiredStateMutation) {
			mutation.Apply = nil
		}},
		{name: "missing rollback callback", change: func(mutation *desiredStateMutation) {
			mutation.Rollback = nil
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mutation := desiredStateMutation{
				Request:  testDesiredMutationRequest(),
				Mutate:   func(*config.State) error { return nil },
				Apply:    func(config.State, config.State) error { return nil },
				Rollback: func(config.State, config.State) error { return nil },
			}
			test.change(&mutation)
			if err := validateDesiredStateMutation(mutation); !errors.Is(err, ErrInvalidDesiredMutation) {
				t.Fatalf("error = %v, want %v", err, ErrInvalidDesiredMutation)
			}
		})
	}
}

func TestValidateManagedNodeStateRejectsCorruptMetadata(t *testing.T) {
	validReceipt := config.DesiredStateReceipt{
		OperationID:        testOperationID,
		IdempotencyKeyHash: hashIdempotencyKey(mutationIdempotencyKey),
		DesiredGeneration:  1,
		CompletedAt:        time.Now().UTC(),
	}
	tests := []struct {
		name   string
		change func(*config.ManagedNodeState)
	}{
		{name: "non-canonical node ID", change: func(managed *config.ManagedNodeState) {
			managed.NodeID = "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"
		}},
		{name: "nil controller ID", change: func(managed *config.ManagedNodeState) {
			managed.ControllerID = "00000000-0000-0000-0000-000000000000"
		}},
		{name: "invalid state epoch", change: func(managed *config.ManagedNodeState) {
			managed.StateEpoch = "invalid"
		}},
		{name: "zero binding epoch", change: func(managed *config.ManagedNodeState) {
			managed.BindingEpoch = 0
		}},
		{name: "non-canonical receipt operation ID", change: func(managed *config.ManagedNodeState) {
			managed.SuccessfulReceipts[0].OperationID = "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"
		}},
		{name: "short receipt hash", change: func(managed *config.ManagedNodeState) {
			managed.SuccessfulReceipts[0].IdempotencyKeyHash = "abcd"
		}},
		{name: "non-hex receipt hash", change: func(managed *config.ManagedNodeState) {
			managed.SuccessfulReceipts[0].IdempotencyKeyHash = strings.Repeat("z", 64)
		}},
		{name: "receipt generation above state", change: func(managed *config.ManagedNodeState) {
			managed.SuccessfulReceipts[0].DesiredGeneration = 2
		}},
		{name: "zero receipt completion time", change: func(managed *config.ManagedNodeState) {
			managed.SuccessfulReceipts[0].CompletedAt = time.Time{}
		}},
		{name: "duplicate receipt operation ID", change: func(managed *config.ManagedNodeState) {
			second := validReceipt
			second.IdempotencyKeyHash = hashIdempotencyKey("second")
			second.DesiredGeneration = 2
			managed.DesiredGeneration = 2
			managed.SuccessfulReceipts = append(managed.SuccessfulReceipts, second)
		}},
		{name: "duplicate receipt idempotency hash", change: func(managed *config.ManagedNodeState) {
			second := validReceipt
			second.OperationID = "55555555-5555-4555-8555-555555555555"
			second.DesiredGeneration = 2
			managed.DesiredGeneration = 2
			managed.SuccessfulReceipts = append(managed.SuccessfulReceipts, second)
		}},
		{name: "non-increasing receipt generation", change: func(managed *config.ManagedNodeState) {
			second := validReceipt
			second.OperationID = "55555555-5555-4555-8555-555555555555"
			second.IdempotencyKeyHash = hashIdempotencyKey("second")
			managed.SuccessfulReceipts = append(managed.SuccessfulReceipts, second)
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			managed := &config.ManagedNodeState{
				NodeID:             testNodeID,
				ControllerID:       testControllerID,
				StateEpoch:         testStateEpoch,
				BindingEpoch:       1,
				DesiredGeneration:  1,
				SuccessfulReceipts: []config.DesiredStateReceipt{validReceipt},
			}
			test.change(managed)
			if err := validateManagedNodeState(managed); !errors.Is(err, ErrInvalidManagedNodeState) {
				t.Fatalf("error = %v, want %v", err, ErrInvalidManagedNodeState)
			}
		})
	}

	tooMany := &config.ManagedNodeState{
		NodeID:             testNodeID,
		ControllerID:       testControllerID,
		StateEpoch:         testStateEpoch,
		BindingEpoch:       1,
		SuccessfulReceipts: make([]config.DesiredStateReceipt, maxSuccessfulOperationReceipts+1),
	}
	if err := validateManagedNodeState(tooMany); !errors.Is(err, ErrOperationReceiptCapacity) {
		t.Fatalf("receipt capacity error = %v, want %v", err, ErrOperationReceiptCapacity)
	}
}

func TestCommitDesiredStateApplyAndSaveFailuresRollback(t *testing.T) {
	tests := []struct {
		name      string
		failApply bool
		failSave  bool
	}{
		{name: "apply", failApply: true},
		{name: "save", failSave: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			svc, initial := managedStateService(t)
			if test.failSave {
				svc.saveState = func(config.State) error { return errors.New("forced save failure") }
			}
			rollbackCalls := 0
			_, err := svc.commitDesiredState(desiredStateMutation{
				Request: testDesiredMutationRequest(),
				Mutate: func(candidate *config.State) error {
					candidate.Tunnels[0].DNS = "9.9.9.9"
					return nil
				},
				Apply: func(config.State, config.State) error {
					if test.failApply {
						return errors.New("forced apply failure")
					}
					return nil
				},
				Rollback: func(previous, candidate config.State) error {
					rollbackCalls++
					if previous.Tunnels[0].DNS == candidate.Tunnels[0].DNS {
						t.Fatal("rollback did not receive distinct previous and candidate states")
					}
					return nil
				},
			})
			if err == nil {
				t.Fatal("expected commit failure")
			}
			if rollbackCalls != 1 {
				t.Fatalf("rollback calls = %d, want 1", rollbackCalls)
			}
			assertStateEqual(t, svc, initial)
			if _, journalErr := svc.store.LoadPendingDesiredStateCommit(); !errors.Is(journalErr, os.ErrNotExist) {
				t.Fatalf("journal remains after rollback: %v", journalErr)
			}
		})
	}
}

func TestCommitDesiredStateDoesNotApplyWithoutDurableJournal(t *testing.T) {
	svc, initial := managedStateService(t)
	applyCalled := false
	_, err := svc.commitDesiredState(desiredStateMutation{
		Request: testDesiredMutationRequest(),
		Mutate: func(candidate *config.State) error {
			candidate.Tunnels[0].DNS = "9.9.9.9"
			return os.Mkdir(svc.store.PendingDesiredStateCommitPath(), 0700)
		},
		Apply: func(config.State, config.State) error {
			applyCalled = true
			return nil
		},
		Rollback: func(config.State, config.State) error {
			t.Fatal("rollback ran before runtime apply")
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "save desired-state commit journal") {
		t.Fatalf("error = %v, want journal persistence failure", err)
	}
	if applyCalled {
		t.Fatal("runtime apply ran without a durable recovery journal")
	}
	assertStateEqual(t, svc, initial)
}

func TestChangedCandidateRuntimeTunnelsIncludesOnlyNewOrChangedCandidates(t *testing.T) {
	unchanged := config.Tunnel{ID: "unchanged", Name: "awg0", InterfaceName: "awg0"}
	changedBefore := config.Tunnel{ID: "changed", Name: "awg1", InterfaceName: "awg1", ListenPort: 51820}
	changedAfter := changedBefore
	changedAfter.ListenPort = 51821
	added := config.Tunnel{ID: "added", Name: "awg2", InterfaceName: "awg2", ListenPort: 51822}
	deleted := config.Tunnel{ID: "deleted", Name: "awg3", InterfaceName: "awg3", ListenPort: 51823}

	got := changedCandidateRuntimeTunnels(
		config.State{Tunnels: []config.Tunnel{unchanged, changedBefore, deleted}},
		config.State{Tunnels: []config.Tunnel{unchanged, changedAfter, added}},
	)
	if len(got) != 2 || got[0].ID != changedAfter.ID || got[1].ID != added.ID {
		t.Fatalf("candidate runtime tunnels = %#v, want changed and added tunnels", got)
	}
}

func TestCommitDesiredStateRejectsManagedMetadataMutation(t *testing.T) {
	svc, initial := managedStateService(t)
	_, err := svc.commitDesiredState(desiredStateMutation{
		Request: testDesiredMutationRequest(),
		Mutate: func(candidate *config.State) error {
			candidate.ManagedNode.BindingEpoch++
			return nil
		},
		Apply:    func(config.State, config.State) error { return nil },
		Rollback: func(config.State, config.State) error { return nil },
	})
	if !errors.Is(err, ErrInvalidDesiredMutation) {
		t.Fatalf("error = %v, want %v", err, ErrInvalidDesiredMutation)
	}
	assertStateEqual(t, svc, initial)
}

func TestCommitDesiredStateRejectsExhaustedGeneration(t *testing.T) {
	svc, initial := managedStateService(t)
	initial.ManagedNode.DesiredGeneration = math.MaxUint64
	if err := svc.store.Save(initial); err != nil {
		t.Fatal(err)
	}
	request := testDesiredMutationRequest()
	request.ExpectedDesiredGeneration = math.MaxUint64
	_, err := svc.commitDesiredState(desiredStateMutation{
		Request: request,
		Mutate: func(*config.State) error {
			t.Fatal("mutation ran after generation exhaustion")
			return nil
		},
		Apply:    func(config.State, config.State) error { return nil },
		Rollback: func(config.State, config.State) error { return nil },
	})
	if !errors.Is(err, ErrDesiredGenerationExhausted) {
		t.Fatalf("error = %v, want %v", err, ErrDesiredGenerationExhausted)
	}
	assertStateEqual(t, svc, initial)
}

func TestCommitDesiredStateBoundsSuccessfulReceipts(t *testing.T) {
	svc, state := managedStateService(t)
	completedAt := time.Now().UTC()
	for index := 0; index < maxSuccessfulOperationReceipts; index++ {
		state.ManagedNode.SuccessfulReceipts = append(state.ManagedNode.SuccessfulReceipts, config.DesiredStateReceipt{
			OperationID:        fmt.Sprintf("00000000-0000-4000-8000-%012x", index),
			IdempotencyKeyHash: hashIdempotencyKey(fmt.Sprintf("receipt-%d", index)),
			DesiredGeneration:  uint64(index + 1),
			CompletedAt:        completedAt.Add(time.Duration(index) * time.Second),
		})
	}
	state.ManagedNode.DesiredGeneration = maxSuccessfulOperationReceipts
	if err := svc.store.Save(state); err != nil {
		t.Fatal(err)
	}

	replayRequest := testDesiredMutationRequest()
	replayRequest.OperationID = state.ManagedNode.SuccessfulReceipts[0].OperationID
	replayRequest.IdempotencyKey = "receipt-0"
	if result, err := svc.commitDesiredState(desiredStateMutation{
		Request:  replayRequest,
		Mutate:   func(*config.State) error { return nil },
		Apply:    func(config.State, config.State) error { return nil },
		Rollback: func(config.State, config.State) error { return nil },
	}); err != nil || !result.Replayed {
		t.Fatalf("existing receipt did not replay at capacity: result=%#v error=%v", result, err)
	}

	request := testDesiredMutationRequest()
	request.ExpectedDesiredGeneration = maxSuccessfulOperationReceipts
	_, err := svc.commitDesiredState(desiredStateMutation{
		Request: request,
		Mutate: func(*config.State) error {
			t.Fatal("mutation ran at receipt capacity")
			return nil
		},
		Apply:    func(config.State, config.State) error { return nil },
		Rollback: func(config.State, config.State) error { return nil },
	})
	if !errors.Is(err, ErrOperationReceiptCapacity) {
		t.Fatalf("error = %v, want %v", err, ErrOperationReceiptCapacity)
	}
	assertStateEqual(t, svc, state)
}

func TestCommitDesiredStateSerializesConcurrentMutations(t *testing.T) {
	svc, _ := managedStateService(t)
	requests := []DesiredStateMutationRequest{
		testDesiredMutationRequest(),
		{
			OperationID:               "55555555-5555-4555-8555-555555555555",
			IdempotencyKey:            "second-idempotency-key",
			ExpectedStateEpoch:        testStateEpoch,
			ExpectedBindingEpoch:      1,
			ExpectedDesiredGeneration: 0,
		},
	}
	errorsByRequest := make([]error, len(requests))
	var wait sync.WaitGroup
	for index, request := range requests {
		wait.Add(1)
		go func(index int, request DesiredStateMutationRequest) {
			defer wait.Done()
			_, errorsByRequest[index] = svc.commitDesiredState(desiredStateMutation{
				Request: request,
				Mutate: func(candidate *config.State) error {
					candidate.Tunnels[0].DNS = request.IdempotencyKey
					return nil
				},
				Apply:    func(config.State, config.State) error { return nil },
				Rollback: func(config.State, config.State) error { return nil },
			})
		}(index, request)
	}
	wait.Wait()

	successes := 0
	conflicts := 0
	for _, err := range errorsByRequest {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrDesiredGenerationMismatch):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent mutation error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent results: successes=%d conflicts=%d", successes, conflicts)
	}
	persisted, err := svc.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ManagedNode.DesiredGeneration != 1 || len(persisted.ManagedNode.SuccessfulReceipts) != 1 {
		t.Fatalf("concurrent commit persisted invalid metadata: %#v", persisted.ManagedNode)
	}
}

func TestCommitDesiredStateLeavesJournalWhenRuntimeRollbackFails(t *testing.T) {
	svc, initial := managedStateService(t)
	_, err := svc.commitDesiredState(desiredStateMutation{
		Request: testDesiredMutationRequest(),
		Mutate: func(candidate *config.State) error {
			candidate.Tunnels[0].DNS = "9.9.9.9"
			return nil
		},
		Apply:    func(config.State, config.State) error { return errors.New("forced apply failure") },
		Rollback: func(config.State, config.State) error { return errors.New("forced rollback failure") },
	})
	if err == nil || !strings.Contains(err.Error(), "runtime rollback failed") {
		t.Fatalf("error = %v, want rollback failure", err)
	}
	assertStateEqual(t, svc, initial)
	if _, journalErr := svc.store.LoadPendingDesiredStateCommit(); journalErr != nil {
		t.Fatalf("recovery journal was removed after failed rollback: %v", journalErr)
	}
}

func TestInitRecoversUncommittedDesiredStateRuntime(t *testing.T) {
	svc, state := managedStateService(t)
	svc.cfg.ApplyConfig = true
	recorder := runtimeRecorder{}
	svc.runtimeOps = recorder.operations()
	pendingTunnel := storage.PendingRuntimeTunnel{
		ID:            "candidate-tunnel",
		Name:          "awg-next",
		InterfaceName: "awg-next",
		EgressMode:    config.EgressWAN,
		ListenPort:    51825,
		IPv4Subnet:    "10.25.0.0/24",
	}
	pending := storage.PendingDesiredStateCommit{
		OperationID:             testOperationID,
		IdempotencyKeyHash:      hashIdempotencyKey(mutationIdempotencyKey),
		StateEpoch:              testStateEpoch,
		PreviousGeneration:      state.ManagedNode.DesiredGeneration,
		CandidateGeneration:     state.ManagedNode.DesiredGeneration + 1,
		CandidateRuntimeTunnels: []storage.PendingRuntimeTunnel{pendingTunnel},
		CreatedAt:               time.Now().UTC(),
	}
	if err := svc.store.SavePendingDesiredStateCommit(pending); err != nil {
		t.Fatal(err)
	}
	candidateDir := svc.store.TunnelDir(pendingTunnel.InterfaceName)
	if err := os.MkdirAll(candidateDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidateDir, "server.conf"), []byte("candidate"), 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Init(); err != nil {
		t.Fatal(err)
	}
	if len(recorder.removed) != 1 || recorder.removed[0] != pendingTunnel.ID {
		t.Fatalf("recovery removals = %v, want [%s]", recorder.removed, pendingTunnel.ID)
	}
	if len(recorder.applied) != 1 || recorder.applied[0] != state.Tunnels[0].ID {
		t.Fatalf("recovery applies = %v, want [%s]", recorder.applied, state.Tunnels[0].ID)
	}
	if len(recorder.routeCounts) != 1 || recorder.routeCounts[0] != 0 {
		t.Fatalf("recovery WARP reconciliations = %v, want [0]", recorder.routeCounts)
	}
	if _, err := os.Stat(candidateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("candidate rendered directory remains after recovery: %v", err)
	}
	if _, err := svc.store.LoadPendingDesiredStateCommit(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("commit journal remains after recovery: %v", err)
	}
}

func TestInitClearsJournalForCommittedDesiredState(t *testing.T) {
	svc, state := managedStateService(t)
	receipt := config.DesiredStateReceipt{
		OperationID:        testOperationID,
		IdempotencyKeyHash: hashIdempotencyKey("test-idempotency-key"),
		DesiredGeneration:  1,
		CompletedAt:        time.Now().UTC(),
	}
	state.ManagedNode.DesiredGeneration = receipt.DesiredGeneration
	state.ManagedNode.SuccessfulReceipts = []config.DesiredStateReceipt{receipt}
	if err := svc.store.Save(state); err != nil {
		t.Fatal(err)
	}
	if err := svc.store.SavePendingDesiredStateCommit(storage.PendingDesiredStateCommit{
		OperationID:         receipt.OperationID,
		IdempotencyKeyHash:  receipt.IdempotencyKeyHash,
		StateEpoch:          testStateEpoch,
		PreviousGeneration:  0,
		CandidateGeneration: receipt.DesiredGeneration,
		CreatedAt:           receipt.CompletedAt,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Init(); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.store.LoadPendingDesiredStateCommit(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("commit journal remains after committed recovery: %v", err)
	}
}

func TestInitRestoresRenderedConfigForInterruptedUpdate(t *testing.T) {
	svc, state := managedStateService(t)
	tunnel := state.Tunnels[0]
	serverPath := filepath.Join(svc.store.TunnelDir(tunnel.InterfaceName), "server.conf")
	original, err := os.ReadFile(serverPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(serverPath, []byte("candidate config"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := svc.store.SavePendingDesiredStateCommit(storage.PendingDesiredStateCommit{
		OperationID:         testOperationID,
		IdempotencyKeyHash:  hashIdempotencyKey(mutationIdempotencyKey),
		StateEpoch:          testStateEpoch,
		PreviousGeneration:  state.ManagedNode.DesiredGeneration,
		CandidateGeneration: state.ManagedNode.DesiredGeneration + 1,
		CreatedAt:           time.Now().UTC(),
		CandidateRuntimeTunnels: []storage.PendingRuntimeTunnel{{
			ID:            tunnel.ID,
			Name:          tunnel.Name,
			InterfaceName: tunnel.InterfaceName,
			EgressMode:    tunnel.EgressMode,
			ListenPort:    tunnel.ListenPort + 1,
			IPv4Subnet:    tunnel.IPv4Subnet,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Init(); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(serverPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored, original) {
		t.Fatalf("persisted rendered config was not restored after interrupted update")
	}
}

func TestInitRejectsMismatchedDesiredStateJournalWithoutCleanup(t *testing.T) {
	svc, state := managedStateService(t)
	recorder := runtimeRecorder{}
	svc.runtimeOps = recorder.operations()
	pending := storage.PendingDesiredStateCommit{
		OperationID:         testOperationID,
		IdempotencyKeyHash:  hashIdempotencyKey(mutationIdempotencyKey),
		StateEpoch:          "55555555-5555-4555-8555-555555555555",
		PreviousGeneration:  state.ManagedNode.DesiredGeneration,
		CandidateGeneration: state.ManagedNode.DesiredGeneration + 1,
		CreatedAt:           time.Now().UTC(),
	}
	if err := svc.store.SavePendingDesiredStateCommit(pending); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Init(); !errors.Is(err, ErrStateEpochMismatch) {
		t.Fatalf("error = %v, want %v", err, ErrStateEpochMismatch)
	}
	if len(recorder.removed) != 0 || len(recorder.routeCounts) != 0 {
		t.Fatalf("runtime changed for mismatched journal: removals=%v routes=%v", recorder.removed, recorder.routeCounts)
	}
	if _, err := svc.store.LoadPendingDesiredStateCommit(); err != nil {
		t.Fatalf("mismatched journal was not retained: %v", err)
	}
}

func TestValidatePendingDesiredStateCommitMetadataRejectsCorruptValues(t *testing.T) {
	managed := &config.ManagedNodeState{
		NodeID:       testNodeID,
		ControllerID: testControllerID,
		StateEpoch:   testStateEpoch,
		BindingEpoch: 1,
	}
	tests := []struct {
		name   string
		change func(*storage.PendingDesiredStateCommit)
		want   error
	}{
		{name: "non-canonical operation ID", change: func(pending *storage.PendingDesiredStateCommit) {
			pending.OperationID = "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"
		}, want: ErrInvalidManagedNodeState},
		{name: "short idempotency hash", change: func(pending *storage.PendingDesiredStateCommit) {
			pending.IdempotencyKeyHash = "abcd"
		}, want: ErrInvalidManagedNodeState},
		{name: "non-hex idempotency hash", change: func(pending *storage.PendingDesiredStateCommit) {
			pending.IdempotencyKeyHash = strings.Repeat("z", 64)
		}, want: ErrInvalidManagedNodeState},
		{name: "state epoch mismatch", change: func(pending *storage.PendingDesiredStateCommit) {
			pending.StateEpoch = "55555555-5555-4555-8555-555555555555"
		}, want: ErrStateEpochMismatch},
		{name: "non-consecutive generation", change: func(pending *storage.PendingDesiredStateCommit) {
			pending.CandidateGeneration = 2
		}, want: ErrInvalidManagedNodeState},
		{name: "zero creation time", change: func(pending *storage.PendingDesiredStateCommit) {
			pending.CreatedAt = time.Time{}
		}, want: ErrInvalidManagedNodeState},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pending := storage.PendingDesiredStateCommit{
				OperationID:         testOperationID,
				IdempotencyKeyHash:  hashIdempotencyKey(mutationIdempotencyKey),
				StateEpoch:          testStateEpoch,
				PreviousGeneration:  0,
				CandidateGeneration: 1,
				CreatedAt:           time.Now().UTC(),
			}
			test.change(&pending)
			if err := validatePendingDesiredStateCommitMetadata(pending, managed); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestTunnelFromPendingRuntimeValidatesCleanupFields(t *testing.T) {
	valid := storage.PendingRuntimeTunnel{
		ID:            "candidate",
		Name:          "awg-next",
		InterfaceName: "awg-next",
		EgressMode:    config.EgressWAN,
		ListenPort:    51825,
		IPv4Subnet:    "10.25.0.0/24",
	}
	tests := []struct {
		name   string
		change func(*storage.PendingRuntimeTunnel)
	}{
		{name: "empty ID", change: func(tunnel *storage.PendingRuntimeTunnel) {
			tunnel.ID = ""
		}},
		{name: "oversized ID", change: func(tunnel *storage.PendingRuntimeTunnel) {
			tunnel.ID = strings.Repeat("x", 129)
		}},
		{name: "invalid name", change: func(tunnel *storage.PendingRuntimeTunnel) {
			tunnel.Name = "../awg"
		}},
		{name: "invalid interface", change: func(tunnel *storage.PendingRuntimeTunnel) {
			tunnel.InterfaceName = "../awg"
		}},
		{name: "zero port", change: func(tunnel *storage.PendingRuntimeTunnel) {
			tunnel.ListenPort = 0
		}},
		{name: "oversized port", change: func(tunnel *storage.PendingRuntimeTunnel) {
			tunnel.ListenPort = 65536
		}},
		{name: "invalid subnet", change: func(tunnel *storage.PendingRuntimeTunnel) {
			tunnel.IPv4Subnet = "not-a-subnet"
		}},
		{name: "empty egress", change: func(tunnel *storage.PendingRuntimeTunnel) {
			tunnel.EgressMode = ""
		}},
		{name: "invalid egress", change: func(tunnel *storage.PendingRuntimeTunnel) {
			tunnel.EgressMode = "unknown"
		}},
	}

	if _, err := tunnelFromPendingRuntime(valid); err != nil {
		t.Fatalf("valid cleanup fields rejected: %v", err)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pending := valid
			test.change(&pending)
			if _, err := tunnelFromPendingRuntime(pending); !errors.Is(err, ErrInvalidManagedNodeState) {
				t.Fatalf("error = %v, want %v", err, ErrInvalidManagedNodeState)
			}
		})
	}
}

func TestInitRetainsJournalWhenRuntimeRecoveryFails(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*runtimeRecorder)
	}{
		{name: "candidate removal", configure: func(recorder *runtimeRecorder) {
			recorder.failRemoveCall = 1
		}},
		{name: "persisted tunnel apply", configure: func(recorder *runtimeRecorder) {
			recorder.failApplyCall = 1
		}},
		{name: "WARP reconciliation", configure: func(recorder *runtimeRecorder) {
			recorder.failWarpCall = 1
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			svc, state := managedStateService(t)
			svc.cfg.ApplyConfig = true
			recorder := runtimeRecorder{}
			test.configure(&recorder)
			svc.runtimeOps = recorder.operations()
			if err := svc.store.SavePendingDesiredStateCommit(storage.PendingDesiredStateCommit{
				OperationID:         testOperationID,
				IdempotencyKeyHash:  hashIdempotencyKey(mutationIdempotencyKey),
				StateEpoch:          testStateEpoch,
				PreviousGeneration:  state.ManagedNode.DesiredGeneration,
				CandidateGeneration: state.ManagedNode.DesiredGeneration + 1,
				CreatedAt:           time.Now().UTC(),
				CandidateRuntimeTunnels: []storage.PendingRuntimeTunnel{{
					ID:            "candidate-tunnel",
					Name:          "awg-next",
					InterfaceName: "awg-next",
					EgressMode:    config.EgressWAN,
					ListenPort:    51825,
					IPv4Subnet:    "10.25.0.0/24",
				}},
			}); err != nil {
				t.Fatal(err)
			}

			if _, err := svc.Init(); err == nil {
				t.Fatal("expected runtime recovery failure")
			}
			if _, err := svc.store.LoadPendingDesiredStateCommit(); err != nil {
				t.Fatalf("journal was removed after incomplete recovery: %v", err)
			}
		})
	}
}

func TestInitRejectsCommittedReceiptThatDoesNotMatchJournal(t *testing.T) {
	svc, state := managedStateService(t)
	receipt := config.DesiredStateReceipt{
		OperationID:        testOperationID,
		IdempotencyKeyHash: hashIdempotencyKey(mutationIdempotencyKey),
		DesiredGeneration:  1,
		CompletedAt:        time.Now().UTC(),
	}
	state.ManagedNode.DesiredGeneration = 1
	state.ManagedNode.SuccessfulReceipts = []config.DesiredStateReceipt{receipt}
	if err := svc.store.Save(state); err != nil {
		t.Fatal(err)
	}
	if err := svc.store.SavePendingDesiredStateCommit(storage.PendingDesiredStateCommit{
		OperationID:         testOperationID,
		IdempotencyKeyHash:  hashIdempotencyKey("different-key"),
		StateEpoch:          testStateEpoch,
		PreviousGeneration:  0,
		CandidateGeneration: 1,
		CreatedAt:           time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Init(); !errors.Is(err, ErrInvalidManagedNodeState) {
		t.Fatalf("error = %v, want %v", err, ErrInvalidManagedNodeState)
	}
	if _, err := svc.store.LoadPendingDesiredStateCommit(); err != nil {
		t.Fatalf("mismatched committed journal was not retained: %v", err)
	}
}

func TestInitRejectsJournalWithUnexpectedCommittedGeneration(t *testing.T) {
	svc, state := managedStateService(t)
	state.ManagedNode.DesiredGeneration = 2
	if err := svc.store.Save(state); err != nil {
		t.Fatal(err)
	}
	if err := svc.store.SavePendingDesiredStateCommit(storage.PendingDesiredStateCommit{
		OperationID:         testOperationID,
		IdempotencyKeyHash:  hashIdempotencyKey(mutationIdempotencyKey),
		StateEpoch:          testStateEpoch,
		PreviousGeneration:  0,
		CandidateGeneration: 1,
		CreatedAt:           time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Init(); !errors.Is(err, ErrDesiredGenerationMismatch) {
		t.Fatalf("error = %v, want %v", err, ErrDesiredGenerationMismatch)
	}
	if _, err := svc.store.LoadPendingDesiredStateCommit(); err != nil {
		t.Fatalf("unexpected-generation journal was not retained: %v", err)
	}
}

func TestInitRejectsJournalWithoutCommittedState(t *testing.T) {
	cfg := testServiceConfig(t)
	svc := New(cfg)
	if err := svc.store.SavePendingDesiredStateCommit(storage.PendingDesiredStateCommit{
		OperationID:         testOperationID,
		IdempotencyKeyHash:  hashIdempotencyKey(mutationIdempotencyKey),
		StateEpoch:          testStateEpoch,
		PreviousGeneration:  0,
		CandidateGeneration: 1,
		CreatedAt:           time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Init(); err == nil || !strings.Contains(err.Error(), "cannot initialize state") {
		t.Fatalf("error = %v, want initialization refusal", err)
	}
	if _, err := os.Stat(svc.store.StatePath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state was initialized despite pending journal: %v", err)
	}
}

func managedStateService(t *testing.T) (*Service, config.State) {
	t.Helper()
	cfg := testServiceConfig(t)
	svc := New(cfg)
	state, err := svc.Init()
	if err != nil {
		t.Fatal(err)
	}
	state.ManagedNode = &config.ManagedNodeState{
		NodeID:       testNodeID,
		ControllerID: testControllerID,
		StateEpoch:   testStateEpoch,
		BindingEpoch: 1,
	}
	if err := svc.store.Save(state); err != nil {
		t.Fatal(err)
	}
	return svc, state
}

func testDesiredMutationRequest() DesiredStateMutationRequest {
	return DesiredStateMutationRequest{
		OperationID:               testOperationID,
		IdempotencyKey:            mutationIdempotencyKey,
		ExpectedStateEpoch:        testStateEpoch,
		ExpectedBindingEpoch:      1,
		ExpectedDesiredGeneration: 0,
	}
}

func assertStateEqual(t *testing.T, svc *Service, want config.State) {
	t.Helper()
	got, err := svc.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !statesEqual(got, want) {
		t.Fatalf("persisted state changed:\n got: %#v\nwant: %#v", got, want)
	}
}

func statesEqual(left, right config.State) bool {
	return reflect.DeepEqual(left, right)
}
