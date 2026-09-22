package app

import (
	"errors"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/google/uuid"
)

func TestServiceBootIDIsStableForProcessLifetime(t *testing.T) {
	first := New(testServiceConfig(t))
	firstID, err := first.BootID()
	if err != nil {
		t.Fatal(err)
	}
	if parsed, err := uuid.Parse(firstID); err != nil || parsed == uuid.Nil || parsed.String() != firstID {
		t.Fatalf("boot ID = %q, want canonical non-nil UUID", firstID)
	}
	again, err := first.BootID()
	if err != nil {
		t.Fatal(err)
	}
	if again != firstID {
		t.Fatalf("boot ID changed within one service: %q != %q", again, firstID)
	}

	secondID, err := New(testServiceConfig(t)).BootID()
	if err != nil {
		t.Fatal(err)
	}
	if secondID != firstID {
		t.Fatalf("services in one process have different boot IDs: %q != %q", secondID, firstID)
	}
}

func TestNewBootIDReturnsDistinctIDs(t *testing.T) {
	first, err := newBootID()
	if err != nil {
		t.Fatal(err)
	}
	second, err := newBootID()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("newBootID returned duplicate IDs: %q", first)
	}
}

func TestStartManagedNodeBootPersistsOneSequencePerService(t *testing.T) {
	service := New(testServiceConfig(t))
	state, err := service.Init()
	if err != nil {
		t.Fatal(err)
	}
	state.ManagedNode = testManagedNodeState()
	state.ManagedNode.DesiredGeneration = 9
	if err := service.store.Save(state); err != nil {
		t.Fatal(err)
	}

	boot, err := service.StartManagedNodeBoot()
	if err != nil {
		t.Fatal(err)
	}
	again, err := service.StartManagedNodeBoot()
	if err != nil {
		t.Fatal(err)
	}
	if boot != again {
		t.Fatalf("managed boot changed within one service: %#v != %#v", boot, again)
	}
	if boot.BootID != processBootID || boot.BootSequence != 1 || boot.StateEpoch != testStateEpoch {
		t.Fatalf("managed boot = %#v", boot)
	}
	persisted, err := service.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ManagedNode.BootSequence != 1 {
		t.Fatalf("persisted boot sequence = %d, want 1", persisted.ManagedNode.BootSequence)
	}
	if persisted.ManagedNode.DesiredGeneration != 9 {
		t.Fatalf("desired generation = %d, want 9", persisted.ManagedNode.DesiredGeneration)
	}
}

func TestStartManagedNodeBootRejectsSequenceExhaustion(t *testing.T) {
	service := New(testServiceConfig(t))
	state, err := service.Init()
	if err != nil {
		t.Fatal(err)
	}
	state.ManagedNode = testManagedNodeState()
	state.ManagedNode.BootSequence = ^uint64(0)
	if err := service.store.Save(state); err != nil {
		t.Fatal(err)
	}
	if _, err := service.StartManagedNodeBoot(); !errors.Is(err, ErrBootSequenceExhausted) {
		t.Fatalf("error = %v, want %v", err, ErrBootSequenceExhausted)
	}
}

func TestPrepareRestoredStatePreservesOnlyMatchingManagedIdentity(t *testing.T) {
	managed := testManagedNodeState()
	current := config.State{ManagedNode: managed}
	restored := config.State{ManagedNode: testManagedNodeState()}

	prepared, detached, err := PrepareRestoredState(&current, restored, false, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if detached {
		t.Fatal("matching managed identity was detached")
	}
	if prepared.ManagedNode == nil || prepared.ManagedNode.NodeID != managed.NodeID {
		t.Fatalf("managed identity was not preserved: %#v", prepared.ManagedNode)
	}

	restored.ManagedNode.StateEpoch = "55555555-5555-4555-8555-555555555555"
	if _, _, err := PrepareRestoredState(&current, restored, false, time.Time{}); !errors.Is(err, ErrManagedNodeRestoreIdentityConflict) {
		t.Fatalf("identity mismatch error = %v, want %v", err, ErrManagedNodeRestoreIdentityConflict)
	}
}

func TestPrepareRestoredStateRequiresExplicitDetachForModeTransition(t *testing.T) {
	managed := config.State{ManagedNode: testManagedNodeState()}
	standalone := config.State{}

	for _, test := range []struct {
		name     string
		current  *config.State
		restored config.State
	}{
		{name: "managed backup on new installation", restored: managed},
		{name: "managed backup on standalone installation", current: &standalone, restored: managed},
		{name: "standalone backup on managed installation", current: &managed, restored: standalone},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := PrepareRestoredState(test.current, test.restored, false, time.Time{}); !errors.Is(err, ErrManagedNodeRestoreIdentityConflict) {
				t.Fatalf("error = %v, want %v", err, ErrManagedNodeRestoreIdentityConflict)
			}
		})
	}
}

func TestPrepareRestoredStateDetachPreservesConfiguration(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.FixedZone("test", 2*60*60))
	restored := config.State{
		ManagedNode: testManagedNodeState(),
		Tunnels:     []config.Tunnel{{ID: "tunnel-1", Name: "main"}},
	}

	prepared, detached, err := PrepareRestoredState(nil, restored, true, now)
	if err != nil {
		t.Fatal(err)
	}
	if !detached {
		t.Fatal("explicit identity detach was not reported")
	}
	if prepared.ManagedNode != nil {
		t.Fatalf("managed identity remains after detach: %#v", prepared.ManagedNode)
	}
	if len(prepared.Tunnels) != 1 || prepared.Tunnels[0].ID != restored.Tunnels[0].ID {
		t.Fatalf("local configuration changed during detach: %#v", prepared.Tunnels)
	}
	if !prepared.UpdatedAt.Equal(now.UTC()) {
		t.Fatalf("updated at = %v, want %v", prepared.UpdatedAt, now.UTC())
	}
}

func TestPrepareRestoredStateRejectsInvalidManagedMetadata(t *testing.T) {
	restored := config.State{ManagedNode: testManagedNodeState()}
	restored.ManagedNode.BindingEpoch = 0
	if _, _, err := PrepareRestoredState(nil, restored, true, time.Time{}); !errors.Is(err, ErrInvalidManagedNodeState) {
		t.Fatalf("error = %v, want %v", err, ErrInvalidManagedNodeState)
	}
}

func testManagedNodeState() *config.ManagedNodeState {
	return &config.ManagedNodeState{
		NodeID:       testNodeID,
		ControllerID: testControllerID,
		StateEpoch:   testStateEpoch,
		BindingEpoch: 1,
	}
}
