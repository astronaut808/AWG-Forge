package app

import (
	"context"
	"time"
)

// EnrollmentOnboardingStatus is deliberately limited to data safe for the
// browser that created an invitation. It contains no enrollment proof,
// certificate, CSR, session, or key material.
type EnrollmentOnboardingStatus struct {
	InvitationID string    `json:"invitation_id"`
	EnrollmentID string    `json:"enrollment_id,omitempty"`
	ControllerID string    `json:"controller_id"`
	NodeID       string    `json:"node_id,omitempty"`
	Status       string    `json:"status"`
	Connected    bool      `json:"connected"`
	ExpiresAt    time.Time `json:"expires_at"`
	BindingEpoch uint64    `json:"binding_epoch,omitempty"`
}

// EnrollmentOnboarding returns the current browser-facing state for one
// invitation after rechecking the authenticated controller session.
func (s *Service) EnrollmentOnboarding(ctx context.Context, token, invitationID string) (EnrollmentOnboardingStatus, error) {
	if err := s.lockControlRequest(ctx); err != nil {
		return EnrollmentOnboardingStatus{}, err
	}
	defer s.unlockStateMutation()
	state, _, err := s.enrollmentStateLocked()
	if err != nil {
		return EnrollmentOnboardingStatus{}, err
	}
	if _, _, err := s.controlRecentSession(ctx, token); err != nil {
		return EnrollmentOnboardingStatus{}, err
	}
	db, _, err := s.openControlRegistry(ctx)
	if err != nil {
		return EnrollmentOnboardingStatus{}, err
	}
	defer func() { _ = db.Close() }()
	status, err := db.EnrollmentOnboardingStatus(ctx, invitationID, state.Controller.ControllerID, state.Controller.Control.CAGeneration, time.Now().UTC())
	if err != nil {
		return EnrollmentOnboardingStatus{}, err
	}
	return EnrollmentOnboardingStatus{
		InvitationID: status.InvitationID,
		EnrollmentID: status.EnrollmentID,
		ControllerID: status.ControllerID,
		NodeID:       status.NodeID,
		Status:       status.Status,
		Connected:    status.Connected,
		ExpiresAt:    status.ExpiresAt,
		BindingEpoch: status.BindingEpoch,
	}, nil
}
