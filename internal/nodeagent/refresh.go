package nodeagent

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlapi"
)

type pendingRefresh struct {
	id       string
	receipt  controlapi.OperationReceipt
	deadline time.Time
}

// Called only by Run: the same owner allocates periodic and forced sequences.
// One in-memory result is enough because refresh is read-only and leases can
// safely reclaim collection after a process crash, without enabling node SQLite.
func runRefresh(ctx context.Context, service *app.Service, cfg config.Config, client *http.Client, base string, p controlapi.OperationPoll, sequence *uint64, pending **pendingRefresh) error {
	if err := runPendingClientResults(ctx, service, client, base, p); err != nil {
		return err
	}
	if *pending != nil {
		previous := *pending
		if previous.receipt.SessionID != p.SessionID || previous.receipt.BootID != p.BootID || !time.Now().Before(previous.deadline) {
			*pending = nil
		} else {
			err := requestJSON(ctx, client, base, http.MethodPut, "/control/v1/node/operations/"+previous.id+"/result", "", previous.receipt, nil)
			if err == nil || errors.Is(err, errFenced) {
				*pending = nil
			}
			return err
		}
	}
	var delivery controlapi.OperationDelivery
	if err := requestJSON(ctx, client, base, http.MethodPost, "/control/v1/node/operations/poll", "", p, &delivery); err != nil {
		if errors.Is(err, errNoOperation) {
			return nil
		}
		return err
	}
	if delivery.Validate() != nil || delivery.Operation.StateEpoch != p.StateEpoch || delivery.Operation.BindingEpoch != p.BindingEpoch || !time.Now().Before(delivery.LeaseExpiresAt) {
		return errFenced
	}
	if controlapi.IsClientMutation(delivery.Operation.Operation.Kind) {
		opCtx, cancel := context.WithDeadline(ctx, delivery.LeaseExpiresAt)
		defer cancel()
		outcome, err := service.ExecuteClientOperation(opCtx, delivery.Operation)
		if err != nil {
			return err
		}
		return sendClientOutcome(ctx, service, client, base, p, delivery.LeaseToken, outcome)
	}
	// Recheck local committed generation at execution, not just at poll time.
	state, err := service.NodeObservationState(ctx)
	if err != nil {
		return err
	}
	if state.ManagedNode.StateEpoch != p.StateEpoch || state.ManagedNode.BindingEpoch != p.BindingEpoch {
		return errFenced
	}
	receipt := controlapi.OperationReceipt{OperationPoll: p, LeaseToken: delivery.LeaseToken}
	receipt.WaitSeconds = 0
	receipt.Generation = state.ManagedNode.DesiredGeneration
	deadline := delivery.LeaseExpiresAt
	opCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	switch {
	case delivery.AcceptedSequence > 0:
		// Controller already durably accepted this exact lease's snapshot. This
		// also recovers a lost upload/result acknowledgement without recollection.
		receipt.Generation = delivery.Operation.Generation
		receipt.Status, receipt.Code = "succeeded", "snapshot_accepted"
		receipt.Result = &controlapi.RefreshResult{SnapshotAccepted: true, Sequence: delivery.AcceptedSequence}
	case state.ManagedNode.DesiredGeneration != delivery.Operation.Generation:
		receipt.Status, receipt.Code = "conflict", "generation_conflict"
	default:
		*sequence++
		snapshot, collectErr := collectSnapshot(opCtx, service, cfg, p.SessionID, *sequence)
		if collectErr != nil {
			receipt.Status, receipt.Code = "failed", "collection_failed"
		} else if snapshot.DesiredGeneration != delivery.Operation.Generation {
			receipt.Generation = snapshot.DesiredGeneration
			receipt.Status, receipt.Code = "conflict", "generation_conflict"
		} else {
			snapshot.Refresh = &controlapi.RefreshCorrelation{OperationID: delivery.Operation.OperationID, LeaseToken: delivery.LeaseToken}
			var ack controlapi.SnapshotAccepted
			uploadErr := requestJSON(opCtx, client, base, http.MethodPut, "/control/v1/node/snapshot", "", snapshot, &ack)
			if errors.Is(uploadErr, errForbidden) || errors.Is(uploadErr, errFenced) {
				return uploadErr
			}
			if uploadErr == nil && (ack.Sequence != snapshot.Sequence || ack.ReceivedAt.IsZero()) {
				return errors.New("invalid refresh snapshot acknowledgement")
			}
			// An uncertain upload is never success by itself: CompleteRefresh requires
			// independently committed evidence, otherwise it fences this assertion.
			receipt.Status, receipt.Code = "succeeded", "snapshot_accepted"
			receipt.Result = &controlapi.RefreshResult{SnapshotAccepted: true, Sequence: snapshot.Sequence}
		}
	}
	*pending = &pendingRefresh{id: delivery.Operation.OperationID, receipt: receipt, deadline: deadline}
	err = requestJSON(opCtx, client, base, http.MethodPut, "/control/v1/node/operations/"+delivery.Operation.OperationID+"/result", "", receipt, nil)
	if err == nil || errors.Is(err, errFenced) {
		*pending = nil
	}
	return err
}
