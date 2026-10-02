package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/astronaut808/awg-forge/internal/controlauth"
	"github.com/astronaut808/awg-forge/internal/controlpki"
	"github.com/astronaut808/awg-forge/internal/controlserver"
	"github.com/astronaut808/awg-forge/internal/sqldb"
	"github.com/astronaut808/awg-forge/internal/storage"
	"github.com/google/uuid"
)

// EnableControl requires a process-local verified backup receipt and an explicit
// acknowledgement that the operator retained the archive. It remains loopback-only.
func (s *Service) EnableControl(ctx context.Context, token string, receipt *ControlEnableReceipt, backupRetained bool) error {
	if !backupRetained {
		return errors.New("retained backup confirmation required")
	}
	return s.enableControlLoopback(ctx, token, receipt, s.enrollmentRoutes())
}
func (s *Service) DisableControl(ctx context.Context, token string) error {
	return s.disableControlLoopback(ctx, token)
}
func (s *Service) ShutdownControl() error { return s.shutdownControlLoopback() }

// StartControl restarts only explicitly committed enablement; Init never starts it.
func (s *Service) StartControl(ctx context.Context) error {
	state, err := s.State()
	if err != nil {
		return err
	}
	if state.EffectiveMode() != config.ModeController || state.Controller == nil || state.Controller.Control == nil || !state.Controller.Control.Enabled {
		return nil
	}
	return s.restartControlLoopback(ctx, s.enrollmentRoutes())
}

func (s *Service) PrepareAuthenticatedControl(ctx context.Context, token string, request ControlIdentityRequest) (ControlIdentityResult, error) {
	advertised, err := netip.ParseAddr(request.Advertised)
	if err != nil || !advertised.IsLoopback() || advertised.Is4In6() || advertised.Zone() != "" {
		return ControlIdentityResult{}, errors.New("control advertised endpoint must be literal loopback")
	}
	if err := s.lockControlRequest(ctx); err != nil {
		return ControlIdentityResult{}, err
	}
	defer s.unlockStateMutation()
	if err := s.store.CheckRestorePending(); err != nil {
		return ControlIdentityResult{}, err
	}
	if _, _, err := s.controlRecentSession(ctx, token); err != nil {
		return ControlIdentityResult{}, err
	}
	return s.prepareControlIdentityLocked(ctx, request)
}

func (s *Service) CreateEnrollmentInvitation(ctx context.Context, token string) (controlapi.Invitation, error) {
	if err := s.lockControlRequest(ctx); err != nil {
		return controlapi.Invitation{}, err
	}
	defer s.unlockStateMutation()
	state, material, err := s.enrollmentStateLocked()
	if err != nil {
		return controlapi.Invitation{}, err
	}
	if _, _, err := s.controlRecentSession(ctx, token); err != nil {
		return controlapi.Invitation{}, err
	}
	db, keys, err := s.openControlRegistry(ctx)
	if err != nil {
		return controlapi.Invitation{}, err
	}
	defer func() { _ = db.Close() }()
	secret, err := controlauth.NewSessionToken(nil)
	if err != nil {
		return controlapi.Invitation{}, err
	}
	digest, err := keys.EnrollmentDigest("invitation", secret)
	if err != nil {
		return controlapi.Invitation{}, err
	}
	now := time.Now().UTC()
	invitation := sqldb.EnrollmentInvitation{ID: uuid.NewString(), ControllerID: state.Controller.ControllerID, NodeID: uuid.NewString(), SecretDigest: [32]byte(digest), CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute)}
	if err := db.CreateEnrollmentInvitation(ctx, invitation); err != nil {
		return controlapi.Invitation{}, err
	}
	control := state.Controller.Control
	return controlapi.Invitation{InvitationID: invitation.ID, Secret: secret, ControllerURL: "https://" + net.JoinHostPort(control.Advertised, strconv.Itoa(control.Port)), CAPin: control.CAPin, CACertPEM: string(material.CACert), ExpiresAt: invitation.ExpiresAt}, nil
}

// EnrollmentReview exposes public comparison material, never CSR or credentials.
type EnrollmentReview struct {
	EnrollmentID     string    `json:"enrollment_id"`
	RequestedName    string    `json:"requested_name"`
	VerificationCode string    `json:"verification_code"`
	Status           string    `json:"status"`
	ExpiresAt        time.Time `json:"expires_at"`
}

func (s *Service) ReviewEnrollment(ctx context.Context, token, id string) (EnrollmentReview, error) {
	if err := s.lockControlRequest(ctx); err != nil {
		return EnrollmentReview{}, err
	}
	defer s.unlockStateMutation()
	if _, _, err := s.enrollmentStateLocked(); err != nil {
		return EnrollmentReview{}, err
	}
	if _, _, err := s.controlRecentSession(ctx, token); err != nil {
		return EnrollmentReview{}, err
	}
	db, _, err := s.openControlRegistry(ctx)
	if err != nil {
		return EnrollmentReview{}, err
	}
	defer func() { _ = db.Close() }()
	enrollment, err := db.FindEnrollmentByInvitation(ctx, id)
	if err != nil {
		return EnrollmentReview{}, err
	}
	return EnrollmentReview{enrollment.ID, enrollment.RequestedName, enrollmentCode(enrollment.InvitationID, enrollment.CSRHash), enrollment.Status, enrollment.ExpiresAt}, nil
}
func (s *Service) DecideEnrollment(ctx context.Context, token, id, code string, approve bool) error {
	if err := s.lockControlRequest(ctx); err != nil {
		return err
	}
	defer s.unlockStateMutation()
	state, material, err := s.enrollmentStateLocked()
	if err != nil {
		return err
	}
	if _, _, err := s.controlRecentSession(ctx, token); err != nil {
		return err
	}
	db, _, err := s.openControlRegistry(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	enrollment, err := db.FindEnrollment(ctx, id)
	if err != nil {
		return err
	}
	if enrollment.ControllerID != state.Controller.ControllerID || code != enrollmentCode(enrollment.InvitationID, enrollment.CSRHash) {
		return sqldb.ErrEnrollmentDenied
	}
	now := time.Now().UTC()
	if !approve {
		return db.RejectEnrollment(ctx, id, now)
	}
	issued, err := controlpki.IssueNodeCertificate(material, enrollment.CSRDER, now)
	if err != nil {
		return err
	}
	_, err = db.ApproveEnrollment(ctx, id, enrollment.CSRHash, state.Controller.Control.CAGeneration, issued, now)
	return err
}

// Requests waiting for the application mutex must leave when runtime shutdown
// cancels their context. Otherwise disable could wait for a request behind itself.
func (s *Service) lockControlRequest(ctx context.Context) error {
	timer := time.NewTicker(10 * time.Millisecond)
	defer timer.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if s.mu.TryLock() {
			lock, err := storage.AcquireStateMutationLockContext(ctx, s.cfg.ConfigDir)
			if err != nil {
				s.mu.Unlock()
				return err
			}
			s.stateMutationLock = lock
			// nosemgrep: trailofbits.go.missing-unlock-before-return.missing-unlock-before-return
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
}
func (s *Service) enrollmentStateLocked() (config.State, controlpki.Material, error) {
	denied := errors.New("control enrollment is unavailable")
	if s.store.CheckRestorePending() != nil || s.checkControlMutationJournalsLocked() != nil || s.store.CheckNoControlServerRotation() != nil {
		return config.State{}, controlpki.Material{}, denied
	}
	if _, err := s.store.LoadControlIdentityJournal(); !errors.Is(err, os.ErrNotExist) {
		return config.State{}, controlpki.Material{}, denied
	}
	state, err := s.store.Load()
	if err != nil || validateStateMode(state) != nil || state.Controller == nil || state.Controller.Control == nil || !state.Controller.Control.Enabled || s.validateControlIdentityLocked(state.Controller.Control, time.Time{}, false) != nil {
		return config.State{}, controlpki.Material{}, denied
	}
	material, err := s.store.LoadControlIdentity(state.Controller.Control.CAGeneration, state.Controller.Control.ServerGeneration)
	if err != nil {
		return config.State{}, controlpki.Material{}, denied
	}
	return state, material, nil
}

func (s *Service) enrollmentRoutes() []controlserver.Route {
	return []controlserver.Route{
		{ID: "enrollment.claim", Method: http.MethodPost, Path: "/control/v1/enrollments/{invitation_id}/claim", Bootstrap: true, Handler: http.HandlerFunc(s.claimEnrollmentHTTP)},
		{ID: "enrollment.status", Method: http.MethodGet, Path: "/control/v1/enrollments/{enrollment_id}", Bootstrap: true, Handler: http.HandlerFunc(s.enrollmentStatusHTTP)},
		{ID: "node.presence", Method: http.MethodPut, Path: "/control/v1/node/presence", Handler: http.HandlerFunc(s.nodePresenceHTTP)},
	}
}
func bootstrapToken(r *http.Request) (string, error) {
	values := r.Header.Values("Authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
		return "", sqldb.ErrEnrollmentDenied
	}
	token := strings.TrimPrefix(values[0], "Bearer ")
	value, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(value) != 32 {
		return "", sqldb.ErrEnrollmentDenied
	}
	return token, nil
}
func enrollmentCode(invitation string, digest [32]byte) string {
	sum := sha256.Sum256(append([]byte("awg-forge/enrollment-comparison/v1\x00"+invitation+"\x00"), digest[:]...))
	value := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:5])
	return value[:4] + "-" + value[4:]
}
func claimCredential(secret, invitation string, digest [32]byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("awg-forge/enrollment-claim/v1\x00" + invitation + "\x00"))
	_, _ = mac.Write(digest[:])
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func readControlJSON(w http.ResponseWriter, r *http.Request, value any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("invalid request")
	}
	return nil
}
func controlJSON(w http.ResponseWriter, status int, value any) {
	if status >= 400 {
		w.Header().Set("Content-Type", "application/problem+json")
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func controlProblem(w http.ResponseWriter, err error) {
	status, code := http.StatusServiceUnavailable, "control_unavailable"
	if errors.Is(err, sqldb.ErrEnrollmentDenied) {
		status, code = http.StatusUnauthorized, "enrollment_denied"
	}
	if errors.Is(err, sqldb.ErrEnrollmentConflict) {
		status, code = http.StatusConflict, "enrollment_conflict"
	}
	if errors.Is(err, sqldb.ErrEnrollmentExpired) {
		status, code = http.StatusGone, "enrollment_expired"
	}
	controlJSON(w, status, map[string]any{"type": "about:blank", "title": "Control request failed", "status": status, "code": code})
}

var capabilityRE = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)

func validControlMetadata(version string, capabilities []string) bool {
	if len(version) < 1 || len(version) > 64 || !utf8.ValidString(version) || strings.ContainsAny(version, "\r\n\x00") || len(capabilities) < 1 || len(capabilities) > 128 {
		return false
	}
	seen := make(map[string]bool, len(capabilities))
	for _, value := range capabilities {
		if !capabilityRE.MatchString(value) || seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}
func (s *Service) claimEnrollmentHTTP(w http.ResponseWriter, r *http.Request) {
	token, err := bootstrapToken(r)
	if err != nil {
		controlProblem(w, err)
		return
	}
	var request controlapi.ClaimRequest
	if readControlJSON(w, r, &request) != nil || len(request.RequestedName) < 1 || utf8.RuneCountInString(request.RequestedName) > 128 || !utf8.ValidString(request.RequestedName) || strings.ContainsAny(request.RequestedName, "\r\n\x00") || len(request.CSRPEM) > 8192 || !validControlMetadata(request.ApplicationVersion, request.Capabilities) || !validControlUUID(request.BootID) || len(request.ContractVersions) < 1 || len(request.ContractVersions) > 4 {
		controlJSON(w, 400, map[string]any{"type": "about:blank", "title": "Invalid claim", "status": 400, "code": "invalid_claim"})
		return
	}
	versions := map[int]bool{}
	for _, version := range request.ContractVersions {
		if version < 1 || version > 255 || versions[version] {
			controlJSON(w, 400, map[string]any{"type": "about:blank", "title": "Invalid versions", "status": 400, "code": "invalid_claim"})
			return
		}
		versions[version] = true
	}
	if !versions[1] {
		controlJSON(w, 400, map[string]any{"type": "about:blank", "title": "Unsupported contract", "status": 400, "code": "unsupported_contract"})
		return
	}
	block, rest := pem.Decode([]byte(request.CSRPEM))
	if block == nil || block.Type != "CERTIFICATE REQUEST" || len(block.Headers) > 0 || len(strings.TrimSpace(string(rest))) > 0 {
		controlProblem(w, sqldb.ErrEnrollmentDenied)
		return
	}
	_, digest, err := controlpki.ParseNodeCSR(block.Bytes)
	if err != nil {
		controlProblem(w, sqldb.ErrEnrollmentDenied)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.lockControlRequest(ctx); err != nil {
		controlProblem(w, err)
		return
	}
	defer s.unlockStateMutation()
	state, _, err := s.enrollmentStateLocked()
	if err != nil {
		controlProblem(w, err)
		return
	}
	db, keys, err := s.openControlRegistry(ctx)
	if err != nil {
		controlProblem(w, err)
		return
	}
	defer func() { _ = db.Close() }()
	invitationID := strings.Split(r.URL.Path, "/")[4]
	claimToken := claimCredential(token, invitationID, digest)
	invitationDigest, err := keys.EnrollmentDigest("invitation", token)
	if err != nil {
		controlProblem(w, sqldb.ErrEnrollmentDenied)
		return
	}
	claimDigest, err := keys.EnrollmentDigest("claim", claimToken)
	if err != nil {
		controlProblem(w, sqldb.ErrEnrollmentDenied)
		return
	}
	now := time.Now().UTC()
	enrollment, err := db.ClaimEnrollment(ctx, invitationID, [32]byte(invitationDigest), sqldb.Enrollment{ID: uuid.NewString(), ControllerID: state.Controller.ControllerID, RequestedName: request.RequestedName, CSRDER: block.Bytes, CSRHash: digest, ClaimDigest: [32]byte(claimDigest), CreatedAt: now}, now)
	if err != nil {
		controlProblem(w, err)
		return
	}
	controlJSON(w, http.StatusAccepted, controlapi.ClaimAccepted{EnrollmentID: enrollment.ID, ClaimToken: claimToken, VerificationCode: enrollmentCode(enrollment.InvitationID, enrollment.CSRHash), ExpiresAt: enrollment.ExpiresAt, PollAfterSeconds: 5})
}
func (s *Service) enrollmentStatusHTTP(w http.ResponseWriter, r *http.Request) {
	token, err := bootstrapToken(r)
	if err != nil {
		controlProblem(w, err)
		return
	}
	if r.ContentLength > 0 || len(r.TransferEncoding) > 0 {
		controlProblem(w, sqldb.ErrEnrollmentDenied)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.lockControlRequest(ctx); err != nil {
		controlProblem(w, err)
		return
	}
	defer s.unlockStateMutation()
	_, material, err := s.enrollmentStateLocked()
	if err != nil {
		controlProblem(w, err)
		return
	}
	db, keys, err := s.openControlRegistry(ctx)
	if err != nil {
		controlProblem(w, err)
		return
	}
	defer func() { _ = db.Close() }()
	digest, err := keys.EnrollmentDigest("claim", token)
	if err != nil {
		controlProblem(w, sqldb.ErrEnrollmentDenied)
		return
	}
	enrollment, err := db.EnrollmentStatus(ctx, strings.Split(r.URL.Path, "/")[4], [32]byte(digest), time.Now().UTC())
	if err != nil {
		controlProblem(w, err)
		return
	}
	response := controlapi.EnrollmentStatus{Status: enrollment.Status}
	if response.Status == "pending" {
		response.VerificationCode = enrollmentCode(enrollment.InvitationID, enrollment.CSRHash)
		response.ExpiresAt = &enrollment.ExpiresAt
		response.PollAfterSeconds = 5
	}
	if response.Status == "approved" {
		cert, err := x509.ParseCertificate(enrollment.CertificateDER)
		if err != nil {
			controlProblem(w, sqldb.ErrEnrollmentDenied)
			return
		}
		response.NodeID = enrollment.NodeID
		response.ControllerID = enrollment.ControllerID
		response.BindingEpoch = 1
		response.ContractVersion = 1
		response.Certificate = &controlapi.IssuedCertificate{CertificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})), ChainPEM: string(material.CACert), Serial: cert.SerialNumber.String(), NotBefore: cert.NotBefore, NotAfter: cert.NotAfter}
	}
	controlJSON(w, http.StatusOK, response)
}
func validControlUUID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id.String() == value
}
func (s *Service) nodePresenceHTTP(w http.ResponseWriter, r *http.Request) {
	identity, ok := controlserver.IdentityFromContext(r.Context())
	if !ok {
		controlProblem(w, sqldb.ErrEnrollmentDenied)
		return
	}
	var request controlapi.Presence
	if readControlJSON(w, r, &request) != nil || request.BindingEpoch != identity.BindingEpoch || !validControlUUID(request.BootID) || !validControlUUID(request.StateEpoch) || request.BootSequence == 0 || request.ContractVersion != 1 || !validControlMetadata(request.ApplicationVersion, request.Capabilities) || request.ObservedAt.IsZero() {
		controlJSON(w, 400, map[string]any{"type": "about:blank", "title": "Invalid presence", "status": 400, "code": "invalid_presence"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.lockControlRequest(ctx); err != nil {
		controlProblem(w, err)
		return
	}
	defer s.unlockStateMutation()
	state, _, err := s.enrollmentStateLocked()
	if err != nil || state.Controller.ControllerID != identity.ControllerID {
		controlProblem(w, sqldb.ErrEnrollmentDenied)
		return
	}
	db, _, err := s.openControlRegistry(ctx)
	if err != nil {
		controlProblem(w, err)
		return
	}
	defer func() { _ = db.Close() }()
	// Recheck the exact certificate after taking the mutation lock, closing the
	// interval between transport authorization and disable/revocation.
	if _, err := db.FindActiveNodeCertificate(ctx, identity.ControllerID, state.Controller.Control.CAGeneration, r.TLS.PeerCertificates[0], time.Now()); err != nil {
		controlJSON(w, 403, map[string]any{"type": "about:blank", "title": "Forbidden", "status": 403, "code": "node_denied"})
		return
	}
	now := time.Now().UTC()
	sessionID, err := db.AcceptNodePresence(ctx, sqldb.NodeIdentity{ControllerID: identity.ControllerID, NodeID: identity.NodeID, BindingEpoch: identity.BindingEpoch}, request.StateEpoch, request.BootID, request.BootSequence, now)
	if err != nil {
		controlJSON(w, 409, map[string]any{"type": "about:blank", "title": "Presence fenced", "status": 409, "code": "presence_fenced"})
		return
	}
	cert := r.TLS.PeerCertificates[0]
	controlJSON(w, 200, controlapi.PresenceAccepted{ControllerID: identity.ControllerID, SessionID: sessionID, SessionExpiresAt: now.Add(time.Minute), ServerTime: now, NextPollSeconds: 15, RenewCertificateAfter: cert.NotBefore.Add(cert.NotAfter.Sub(cert.NotBefore) * 2 / 3)})
}
