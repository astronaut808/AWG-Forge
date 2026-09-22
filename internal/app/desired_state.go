package app

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/storage"
	"github.com/google/uuid"
)

const (
	maxDesiredStateIdempotencyKeyLength = 128
	maxSuccessfulOperationReceipts      = 256
)

var (
	ErrNodeNotManaged             = errors.New("node is not managed")
	ErrInvalidManagedNodeState    = errors.New("managed node state is invalid")
	ErrInvalidDesiredMutation     = errors.New("desired-state mutation is invalid")
	ErrStateEpochMismatch         = errors.New("state epoch mismatch")
	ErrBindingEpochMismatch       = errors.New("binding epoch mismatch")
	ErrDesiredGenerationMismatch  = errors.New("desired generation mismatch")
	ErrDesiredGenerationExhausted = errors.New("desired generation exhausted")
	ErrOperationReplayConflict    = errors.New("operation replay conflict")
	ErrOperationReceiptCapacity   = errors.New("successful operation receipt capacity reached")
)

// advanceLocalDesiredGeneration prepares a node-local desired-state commit so
// its eventual save can fence stale remote operations.
func advanceLocalDesiredGeneration(state *config.State) error {
	if state.ManagedNode == nil {
		return nil
	}
	if err := validateManagedNodeState(state.ManagedNode); err != nil {
		return err
	}
	if state.ManagedNode.DesiredGeneration == math.MaxUint64 {
		return ErrDesiredGenerationExhausted
	}
	state.ManagedNode.DesiredGeneration++
	return nil
}

// saveLocalDesiredState persists an ordinary node-originated configuration
// commit. Callers hold s.mu; rollback and observational saves bypass this path.
func (s *Service) saveLocalDesiredState(state *config.State) error {
	if err := advanceLocalDesiredGeneration(state); err != nil {
		return err
	}
	return s.store.Save(*state)
}

// DesiredStateMutationRequest fences a controller mutation to one node state.
type DesiredStateMutationRequest struct {
	OperationID               string
	IdempotencyKey            string
	ExpectedStateEpoch        string
	ExpectedBindingEpoch      uint64
	ExpectedDesiredGeneration uint64
}

type desiredStateMutation struct {
	Request  DesiredStateMutationRequest
	Mutate   func(*config.State) error
	Apply    func(previous, candidate config.State) error
	Rollback func(previous, candidate config.State) error
}

type desiredStateCommitResult struct {
	Receipt  config.DesiredStateReceipt
	Replayed bool
}

// commitDesiredState serializes a managed mutation and commits its state and
// success receipt together. Apply and Rollback must treat the supplied states
// as immutable and must not persist them.
func (s *Service) commitDesiredState(mutation desiredStateMutation) (desiredStateCommitResult, error) {
	if err := s.lockStateMutation(); err != nil {
		return desiredStateCommitResult{}, err
	}
	defer s.unlockStateMutation()

	if err := validateDesiredStateMutation(mutation); err != nil {
		return desiredStateCommitResult{}, err
	}
	current, err := s.initLocked()
	if err != nil {
		return desiredStateCommitResult{}, err
	}
	if err := validateManagedNodeState(current.ManagedNode); err != nil {
		return desiredStateCommitResult{}, err
	}
	managed := current.ManagedNode
	if mutation.Request.ExpectedStateEpoch != managed.StateEpoch {
		return desiredStateCommitResult{}, ErrStateEpochMismatch
	}
	if mutation.Request.ExpectedBindingEpoch != managed.BindingEpoch {
		return desiredStateCommitResult{}, ErrBindingEpochMismatch
	}

	idempotencyHash := hashIdempotencyKey(mutation.Request.IdempotencyKey)
	if receipt, ok := successfulReceipt(managed.SuccessfulReceipts, mutation.Request.OperationID); ok {
		if receipt.IdempotencyKeyHash != idempotencyHash {
			return desiredStateCommitResult{}, ErrOperationReplayConflict
		}
		return desiredStateCommitResult{Receipt: receipt, Replayed: true}, nil
	}
	if _, reused := receiptByIdempotencyHash(managed.SuccessfulReceipts, idempotencyHash); reused {
		return desiredStateCommitResult{}, ErrOperationReplayConflict
	}
	if mutation.Request.ExpectedDesiredGeneration != managed.DesiredGeneration {
		return desiredStateCommitResult{}, ErrDesiredGenerationMismatch
	}
	if managed.DesiredGeneration == math.MaxUint64 {
		return desiredStateCommitResult{}, ErrDesiredGenerationExhausted
	}
	if len(managed.SuccessfulReceipts) >= maxSuccessfulOperationReceipts {
		return desiredStateCommitResult{}, ErrOperationReceiptCapacity
	}

	candidate, err := cloneState(current)
	if err != nil {
		return desiredStateCommitResult{}, err
	}
	if err := mutation.Mutate(&candidate); err != nil {
		return desiredStateCommitResult{}, err
	}
	if !reflect.DeepEqual(current.ManagedNode, candidate.ManagedNode) {
		return desiredStateCommitResult{}, fmt.Errorf("%w: mutation changed managed-node metadata", ErrInvalidDesiredMutation)
	}

	completedAt := time.Now().UTC()
	receipt := config.DesiredStateReceipt{
		OperationID:        mutation.Request.OperationID,
		IdempotencyKeyHash: idempotencyHash,
		DesiredGeneration:  managed.DesiredGeneration + 1,
		CompletedAt:        completedAt,
	}
	candidate.ManagedNode.DesiredGeneration = receipt.DesiredGeneration
	candidate.ManagedNode.SuccessfulReceipts = append(candidate.ManagedNode.SuccessfulReceipts, receipt)
	candidate.UpdatedAt = completedAt

	pending := storage.PendingDesiredStateCommit{
		OperationID:             receipt.OperationID,
		IdempotencyKeyHash:      receipt.IdempotencyKeyHash,
		StateEpoch:              managed.StateEpoch,
		PreviousGeneration:      managed.DesiredGeneration,
		CandidateGeneration:     receipt.DesiredGeneration,
		CandidateRuntimeTunnels: changedCandidateRuntimeTunnels(current, candidate),
		CreatedAt:               completedAt,
	}
	if err := s.store.SavePendingDesiredStateCommit(pending); err != nil {
		return desiredStateCommitResult{}, fmt.Errorf("save desired-state commit journal: %w", err)
	}
	if err := mutation.Apply(current, candidate); err != nil {
		return desiredStateCommitResult{}, s.rollbackDesiredStateMutation(current, candidate, mutation.Rollback, err)
	}
	if err := s.saveState(candidate); err != nil {
		return desiredStateCommitResult{}, s.rollbackDesiredStateMutation(current, candidate, mutation.Rollback, fmt.Errorf("commit desired state: %w", err))
	}
	if err := s.store.DeletePendingDesiredStateCommit(); err != nil {
		s.log("warn", "desired_state.journal.cleanup_failed", "desired-state mutation committed but its recovery journal could not be removed", map[string]any{"operation_id": receipt.OperationID}, err)
	}
	return desiredStateCommitResult{Receipt: receipt}, nil
}

func (s *Service) rollbackDesiredStateMutation(previous, candidate config.State, rollback func(config.State, config.State) error, cause error) error {
	if err := rollback(previous, candidate); err != nil {
		return errors.Join(cause, fmt.Errorf("desired-state runtime rollback failed: %w", err))
	}
	if err := s.store.DeletePendingDesiredStateCommit(); err != nil {
		return errors.Join(cause, fmt.Errorf("remove desired-state commit journal: %w", err))
	}
	return cause
}

func validateDesiredStateMutation(mutation desiredStateMutation) error {
	if !isCanonicalUUID(mutation.Request.OperationID) {
		return fmt.Errorf("%w: operation ID must be a UUID", ErrInvalidDesiredMutation)
	}
	if strings.TrimSpace(mutation.Request.IdempotencyKey) == "" || len(mutation.Request.IdempotencyKey) > maxDesiredStateIdempotencyKeyLength {
		return fmt.Errorf("%w: idempotency key must contain 1-%d bytes", ErrInvalidDesiredMutation, maxDesiredStateIdempotencyKeyLength)
	}
	if !isCanonicalUUID(mutation.Request.ExpectedStateEpoch) {
		return fmt.Errorf("%w: expected state epoch must be a UUID", ErrInvalidDesiredMutation)
	}
	if mutation.Request.ExpectedBindingEpoch == 0 {
		return fmt.Errorf("%w: expected binding epoch must be positive", ErrInvalidDesiredMutation)
	}
	if mutation.Mutate == nil || mutation.Apply == nil || mutation.Rollback == nil {
		return fmt.Errorf("%w: mutation, apply, and rollback functions are required", ErrInvalidDesiredMutation)
	}
	return nil
}

func validateManagedNodeState(managed *config.ManagedNodeState) error {
	if managed == nil {
		return ErrNodeNotManaged
	}
	identifiers := []struct {
		label string
		value string
	}{
		{label: "node ID", value: managed.NodeID},
		{label: "controller ID", value: managed.ControllerID},
		{label: "state epoch", value: managed.StateEpoch},
	}
	for _, identifier := range identifiers {
		label, value := identifier.label, identifier.value
		if !isCanonicalUUID(value) {
			return fmt.Errorf("%w: %s must be a UUID", ErrInvalidManagedNodeState, label)
		}
	}
	if managed.BindingEpoch == 0 {
		return fmt.Errorf("%w: binding epoch must be positive", ErrInvalidManagedNodeState)
	}
	if len(managed.SuccessfulReceipts) > maxSuccessfulOperationReceipts {
		return ErrOperationReceiptCapacity
	}
	seenOperationIDs := make(map[string]struct{}, len(managed.SuccessfulReceipts))
	seenIdempotencyHashes := make(map[string]struct{}, len(managed.SuccessfulReceipts))
	var previousReceiptGeneration uint64
	for _, receipt := range managed.SuccessfulReceipts {
		if !isCanonicalUUID(receipt.OperationID) {
			return fmt.Errorf("%w: receipt operation ID must be a UUID", ErrInvalidManagedNodeState)
		}
		if len(receipt.IdempotencyKeyHash) != sha256.Size*2 {
			return fmt.Errorf("%w: receipt idempotency hash is invalid", ErrInvalidManagedNodeState)
		}
		if _, err := hex.DecodeString(receipt.IdempotencyKeyHash); err != nil {
			return fmt.Errorf("%w: receipt idempotency hash is invalid", ErrInvalidManagedNodeState)
		}
		if receipt.DesiredGeneration <= previousReceiptGeneration || receipt.DesiredGeneration > managed.DesiredGeneration || receipt.CompletedAt.IsZero() {
			return fmt.Errorf("%w: receipt metadata is inconsistent", ErrInvalidManagedNodeState)
		}
		if _, exists := seenOperationIDs[receipt.OperationID]; exists {
			return fmt.Errorf("%w: duplicate receipt operation ID", ErrInvalidManagedNodeState)
		}
		if _, exists := seenIdempotencyHashes[receipt.IdempotencyKeyHash]; exists {
			return fmt.Errorf("%w: duplicate receipt idempotency hash", ErrInvalidManagedNodeState)
		}
		seenOperationIDs[receipt.OperationID] = struct{}{}
		seenIdempotencyHashes[receipt.IdempotencyKeyHash] = struct{}{}
		previousReceiptGeneration = receipt.DesiredGeneration
	}
	return nil
}

func isCanonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}

func hashIdempotencyKey(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func successfulReceipt(receipts []config.DesiredStateReceipt, operationID string) (config.DesiredStateReceipt, bool) {
	for _, receipt := range receipts {
		if receipt.OperationID == operationID {
			return receipt, true
		}
	}
	return config.DesiredStateReceipt{}, false
}

func receiptByIdempotencyHash(receipts []config.DesiredStateReceipt, idempotencyHash string) (config.DesiredStateReceipt, bool) {
	for _, receipt := range receipts {
		if receipt.IdempotencyKeyHash == idempotencyHash {
			return receipt, true
		}
	}
	return config.DesiredStateReceipt{}, false
}

func changedCandidateRuntimeTunnels(previous, candidate config.State) []storage.PendingRuntimeTunnel {
	previousByID := make(map[string]config.Tunnel, len(previous.Tunnels))
	for _, tunnel := range previous.Tunnels {
		previousByID[tunnel.ID] = tunnel
	}
	changed := make([]storage.PendingRuntimeTunnel, 0)
	for _, tunnel := range candidate.Tunnels {
		if old, ok := previousByID[tunnel.ID]; ok && reflect.DeepEqual(old, tunnel) {
			continue
		}
		changed = append(changed, storage.PendingRuntimeTunnel{
			ID:            tunnel.ID,
			Name:          tunnel.Name,
			InterfaceName: tunnel.InterfaceName,
			EgressMode:    tunnel.EgressMode,
			ListenPort:    tunnel.ListenPort,
			IPv4Subnet:    tunnel.IPv4Subnet,
		})
	}
	return changed
}

func (s *Service) recoverPendingDesiredStateCommitLocked(state config.State) error {
	pending, err := s.store.LoadPendingDesiredStateCommit()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load desired-state commit journal: %w", err)
	}
	if err := validatePendingDesiredStateCommitMetadata(pending, state.ManagedNode); err != nil {
		return err
	}
	if receipt, committed := successfulReceipt(state.ManagedNode.SuccessfulReceipts, pending.OperationID); committed {
		if state.ManagedNode.DesiredGeneration != pending.CandidateGeneration ||
			receipt.DesiredGeneration != pending.CandidateGeneration ||
			receipt.IdempotencyKeyHash != pending.IdempotencyKeyHash {
			return fmt.Errorf("%w: committed receipt does not match journal", ErrInvalidManagedNodeState)
		}
		return s.store.DeletePendingDesiredStateCommit()
	}
	if state.ManagedNode.DesiredGeneration != pending.PreviousGeneration {
		return fmt.Errorf("%w: journal generation does not match committed state", ErrDesiredGenerationMismatch)
	}
	candidateRuntimeTunnels := make([]config.Tunnel, 0, len(pending.CandidateRuntimeTunnels))
	for _, pendingTunnel := range pending.CandidateRuntimeTunnels {
		tunnel, err := tunnelFromPendingRuntime(pendingTunnel)
		if err != nil {
			return err
		}
		candidateRuntimeTunnels = append(candidateRuntimeTunnels, tunnel)
	}
	for _, tunnel := range state.Tunnels {
		if !s.profileAvailable(tunnel.ProtocolProfileID) {
			return fmt.Errorf("cannot recover desired state: runtime support for profile %q is unavailable", tunnel.ProtocolProfileID)
		}
	}
	if s.cfg.ApplyConfig {
		for _, tunnel := range candidateRuntimeTunnels {
			if err := s.runtimeOps.removeTunnel(tunnel); err != nil {
				return fmt.Errorf("recover desired-state tunnel runtime %q: %w", tunnel.InterfaceName, err)
			}
		}
	}
	for _, tunnel := range candidateRuntimeTunnels {
		if err := s.store.DeleteRenderedTunnel(tunnel.InterfaceName); err != nil {
			return fmt.Errorf("recover desired-state rendered tunnel %q: %w", tunnel.InterfaceName, err)
		}
	}
	for _, tunnel := range state.Tunnels {
		if err := s.writeRenderedTunnelFiles(state, tunnel.ID); err != nil {
			return fmt.Errorf("restore desired-state rendered tunnel %q: %w", tunnel.InterfaceName, err)
		}
		if s.cfg.ApplyConfig && tunnel.Enabled {
			if err := s.runtimeOps.applyTunnel(tunnel); err != nil {
				return fmt.Errorf("restore desired-state tunnel runtime %q: %w", tunnel.InterfaceName, err)
			}
		}
	}
	if s.cfg.ApplyConfig {
		if err := s.runtimeOps.reconcileWarp(state); err != nil {
			return fmt.Errorf("recover desired-state WARP runtime: %w", err)
		}
	}
	return s.store.DeletePendingDesiredStateCommit()
}

func validatePendingDesiredStateCommitMetadata(pending storage.PendingDesiredStateCommit, managed *config.ManagedNodeState) error {
	if err := validateManagedNodeState(managed); err != nil {
		return err
	}
	if !isCanonicalUUID(pending.OperationID) {
		return fmt.Errorf("%w: journal operation ID must be a UUID", ErrInvalidManagedNodeState)
	}
	if len(pending.IdempotencyKeyHash) != sha256.Size*2 {
		return fmt.Errorf("%w: journal idempotency hash is invalid", ErrInvalidManagedNodeState)
	}
	if _, err := hex.DecodeString(pending.IdempotencyKeyHash); err != nil {
		return fmt.Errorf("%w: journal idempotency hash is invalid", ErrInvalidManagedNodeState)
	}
	if pending.StateEpoch != managed.StateEpoch {
		return ErrStateEpochMismatch
	}
	if pending.CandidateGeneration != pending.PreviousGeneration+1 || pending.CandidateGeneration == 0 || pending.CreatedAt.IsZero() {
		return fmt.Errorf("%w: journal generation metadata is inconsistent", ErrInvalidManagedNodeState)
	}
	return nil
}

func tunnelFromPendingRuntime(pending storage.PendingRuntimeTunnel) (config.Tunnel, error) {
	if pending.ID == "" || len(pending.ID) > 128 {
		return config.Tunnel{}, fmt.Errorf("%w: journal tunnel ID is invalid", ErrInvalidManagedNodeState)
	}
	if err := validateTunnelInterfaceName(pending.Name); err != nil {
		return config.Tunnel{}, fmt.Errorf("%w: journal tunnel name is invalid", ErrInvalidManagedNodeState)
	}
	if err := validateTunnelInterfaceName(pending.InterfaceName); err != nil {
		return config.Tunnel{}, fmt.Errorf("%w: journal tunnel interface is invalid", ErrInvalidManagedNodeState)
	}
	if pending.ListenPort < 1 || pending.ListenPort > 65535 {
		return config.Tunnel{}, fmt.Errorf("%w: journal listen port is invalid", ErrInvalidManagedNodeState)
	}
	if _, _, err := normalizeIPv4CIDR(pending.IPv4Subnet); err != nil {
		return config.Tunnel{}, fmt.Errorf("%w: journal IPv4 subnet is invalid", ErrInvalidManagedNodeState)
	}
	if pending.EgressMode != config.EgressWAN && pending.EgressMode != config.EgressWarp {
		return config.Tunnel{}, fmt.Errorf("%w: journal egress mode is invalid", ErrInvalidManagedNodeState)
	}
	return config.Tunnel{
		ID:            pending.ID,
		Name:          pending.Name,
		InterfaceName: pending.InterfaceName,
		EgressMode:    pending.EgressMode,
		Enabled:       true,
		ListenPort:    pending.ListenPort,
		IPv4Subnet:    pending.IPv4Subnet,
	}, nil
}
