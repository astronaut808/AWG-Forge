package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/backup"
	"github.com/astronaut808/awg-forge/internal/controlauth"
	"github.com/google/uuid"
)

func (w *web) controlSession(rw http.ResponseWriter, r *http.Request, method string) (string, bool) {
	noStore(rw)
	if r.Method != method || !w.validOrigin(r) {
		writeError(rw, http.StatusForbidden, "forbidden")
		return "", false
	}
	if w.controllerAuth == nil {
		writeOperationError(rw, http.StatusConflict, "controller_required", "active controller required")
		return "", false
	}
	cookie, err := r.Cookie("awg_forge_session")
	if err != nil {
		writeError(rw, 401, "unauthorized")
		return "", false
	}
	principal, err := w.controllerAuth.ValidateSession(r.Context(), cookie.Value, time.Now().UTC())
	if err != nil {
		writeError(rw, 401, "unauthorized")
		return "", false
	}
	if !principal.RecentAuth {
		writeOperationError(rw, 403, "recent_auth_required", "recent authentication required")
		return "", false
	}
	return cookie.Value, true
}
func controlOperationError(rw http.ResponseWriter, err error) {
	if errors.Is(err, controlauth.ErrRecentAuthRequired) {
		writeOperationError(rw, 403, "recent_auth_required", "recent authentication required")
		return
	}
	writeOperationError(rw, 409, "control_operation_failed", "control operation unavailable; verify current state and authentication")
}
func (w *web) controlStatusAPI(rw http.ResponseWriter, r *http.Request) {
	noStore(rw)
	if r.Method != http.MethodGet {
		writeError(rw, 405, "method not allowed")
		return
	}
	if w.controllerAuth == nil {
		writeOperationError(rw, 409, "controller_required", "active controller required")
		return
	}
	state, err := w.service.State()
	if err != nil || state.Controller == nil {
		writeError(rw, 503, "control unavailable")
		return
	}
	writeJSON(rw, 200, map[string]any{"controller_id": state.Controller.ControllerID, "control": state.Controller.Control})
}
func (w *web) controlPrepareAPI(rw http.ResponseWriter, r *http.Request) {
	token, ok := w.controlSession(rw, r, http.MethodPost)
	if !ok {
		return
	}
	var request struct {
		BindIP     string `json:"bind_ip"`
		Advertised string `json:"advertised"`
		Port       int    `json:"port"`
	}
	if readJSON(rw, r, &request) != nil {
		writeError(rw, 400, "invalid json")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	result, err := w.service.PrepareAuthenticatedControl(ctx, token, app.ControlIdentityRequest{BindIP: request.BindIP, Advertised: request.Advertised, Port: request.Port})
	if err != nil {
		controlOperationError(rw, err)
		return
	}
	writeJSON(rw, 200, result)
}
func (w *web) controlBackupAPI(rw http.ResponseWriter, r *http.Request) {
	token, ok := w.controlSession(rw, r, http.MethodPost)
	if !ok {
		return
	}
	var request backupRequest
	if readJSON(rw, r, &request) != nil {
		writeError(rw, 400, "invalid json")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	// Serialize the single outstanding browser receipt with its verified archive.
	w.controlEnableMu.Lock()
	defer w.controlEnableMu.Unlock()
	w.controlReceipt = nil
	w.controlReceiptID = ""
	archive, receipt, err := backup.PrepareControlEnable(ctx, w.cfg, w.service, token, request.Password)
	if err != nil {
		controlOperationError(rw, err)
		return
	}
	w.controlReceipt = receipt
	w.controlReceiptID = uuid.NewString()
	rw.Header().Set("X-Control-Enable-Receipt", w.controlReceiptID)
	rw.Header().Set("Content-Type", "application/octet-stream")
	rw.Header().Set("Content-Disposition", `attachment; filename="`+archive.Name+`"`)
	writeRawResponse(rw, archive.Data)
}
func (w *web) controlEnableAPI(rw http.ResponseWriter, r *http.Request) {
	token, ok := w.controlSession(rw, r, http.MethodPost)
	if !ok {
		return
	}
	var request struct {
		Receipt     string `json:"receipt"`
		BackupSaved bool   `json:"backup_saved"`
	}
	if readJSON(rw, r, &request) != nil {
		writeError(rw, 400, "invalid json")
		return
	}
	w.controlEnableMu.Lock()
	defer w.controlEnableMu.Unlock()
	if request.Receipt == "" || request.Receipt != w.controlReceiptID || w.controlReceipt == nil || !request.BackupSaved {
		writeOperationError(rw, 409, "verified_backup_required", "new verified backup and saved archive confirmation required")
		return
	}
	receipt := w.controlReceipt
	w.controlReceipt = nil
	w.controlReceiptID = ""
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := w.service.EnableControl(ctx, token, receipt, request.BackupSaved); err != nil {
		controlOperationError(rw, err)
		return
	}
	writeJSON(rw, 200, map[string]any{"enabled": true})
}
func (w *web) controlDisableAPI(rw http.ResponseWriter, r *http.Request) {
	token, ok := w.controlSession(rw, r, http.MethodPost)
	if !ok {
		return
	}
	w.controlEnableMu.Lock()
	defer w.controlEnableMu.Unlock()
	w.controlReceipt = nil
	w.controlReceiptID = ""
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := w.service.DisableControl(ctx, token); err != nil {
		controlOperationError(rw, err)
		return
	}
	writeJSON(rw, 200, map[string]any{"enabled": false})
}
func (w *web) enrollmentInvitationAPI(rw http.ResponseWriter, r *http.Request) {
	token, ok := w.controlSession(rw, r, http.MethodPost)
	if !ok {
		return
	}
	var request struct{}
	if readJSON(rw, r, &request) != nil {
		writeError(rw, 400, "invalid json")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	invitation, err := w.service.CreateEnrollmentInvitation(ctx, token)
	if err != nil {
		controlOperationError(rw, err)
		return
	}
	rw.Header().Set("Content-Disposition", `attachment; filename="node-invitation.json"`)
	writeJSON(rw, 200, invitation)
}
func (w *web) enrollmentReviewAPI(rw http.ResponseWriter, r *http.Request) {
	token, ok := w.controlSession(rw, r, http.MethodPost)
	if !ok {
		return
	}
	var request struct {
		InvitationID string `json:"invitation_id"`
	}
	if readJSON(rw, r, &request) != nil {
		writeError(rw, 400, "invalid json")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	review, err := w.service.ReviewEnrollment(ctx, token, request.InvitationID)
	if err != nil {
		controlOperationError(rw, err)
		return
	}
	writeJSON(rw, 200, review)
}
func (w *web) enrollmentDecideAPI(rw http.ResponseWriter, r *http.Request) {
	token, ok := w.controlSession(rw, r, http.MethodPost)
	if !ok {
		return
	}
	var request struct {
		EnrollmentID     string `json:"enrollment_id"`
		VerificationCode string `json:"verification_code"`
		Approve          *bool  `json:"approve"`
	}
	if readJSON(rw, r, &request) != nil || request.Approve == nil {
		writeError(rw, 400, "invalid json")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := w.service.DecideEnrollment(ctx, token, request.EnrollmentID, request.VerificationCode, *request.Approve); err != nil {
		controlOperationError(rw, err)
		return
	}
	writeJSON(rw, 200, map[string]any{"approved": *request.Approve})
}
