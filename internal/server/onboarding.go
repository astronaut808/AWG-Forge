package server

import (
	"context"
	"net/http"
	"time"
)

func (w *web) enrollmentOnboardingStatusAPI(rw http.ResponseWriter, r *http.Request) {
	token, ok := w.controlSession(rw, r, http.MethodPost)
	if !ok {
		return
	}
	var request struct {
		InvitationID string `json:"invitation_id"`
	}
	if readJSON(rw, r, &request) != nil {
		writeError(rw, http.StatusBadRequest, "invalid json")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	status, err := w.service.EnrollmentOnboarding(ctx, token, request.InvitationID)
	if err != nil {
		controlOperationError(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, status)
}

func (w *web) installerInfoAPI(rw http.ResponseWriter, r *http.Request) {
	noStore(rw)
	if r.Method != http.MethodGet {
		writeError(rw, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if w.controllerAuth == nil {
		writeOperationError(rw, http.StatusConflict, "controller_required", "active controller required")
		return
	}
	// This build has no published installer artifact that can prove it supports
	// the current join protocol. Keep the endpoint explicit instead of guessing.
	writeJSON(rw, http.StatusOK, map[string]any{"supported": false, "reason": "unpublished_build"})
}
