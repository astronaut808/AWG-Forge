package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image/png"
	"net/http"
	"net/url"
	"path/filepath"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlauth"
	"github.com/astronaut808/awg-forge/internal/sqldb"
	"github.com/boombuler/barcode/qr"
)

type controllerLoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Code     string `json:"code"`
}

type controllerActivationRequest struct {
	Username         string `json:"username"`
	Password         string `json:"password"`
	TOTPSecret       string `json:"totp_secret"`
	ConfirmationCode string `json:"confirmation_code"`
}

type controllerReauthRequest struct {
	Password string `json:"password"`
	Code     string `json:"code"`
	Recovery bool   `json:"recovery"`
}

func (w *web) authStatusAPI(rw http.ResponseWriter, r *http.Request) {
	noStore(rw)
	if r.Method != http.MethodGet {
		writeError(rw, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	w.authMu.RLock()
	defer w.authMu.RUnlock()
	mode := "standalone"
	if w.controllerAuth != nil {
		mode = "controller"
	} else if w.activating.Load() {
		mode = "activating"
	}
	writeJSON(rw, http.StatusOK, map[string]any{"mode": mode})
}

func (w *web) authSessionAPI(rw http.ResponseWriter, r *http.Request) {
	noStore(rw)
	if r.Method != http.MethodGet {
		writeError(rw, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if w.controllerAuth == nil {
		writeJSON(rw, http.StatusOK, map[string]any{"mode": "standalone", "authenticated": true})
		return
	}
	cookie, err := r.Cookie("awg_forge_session")
	if err != nil {
		writeError(rw, http.StatusUnauthorized, "unauthorized")
		return
	}
	principal, err := w.controllerAuth.ValidateSession(r.Context(), cookie.Value, time.Now().UTC())
	if err != nil {
		writeError(rw, http.StatusUnauthorized, "unauthorized")
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"mode": "controller", "authenticated": true, "username": principal.Username, "recent_auth": principal.RecentAuth, "expires_at": principal.ExpiresAt})
}

func (w *web) controllerSetupAPI(rw http.ResponseWriter, r *http.Request) {
	noStore(rw)
	if r.Method != http.MethodPost || !w.validOrigin(r) {
		writeError(rw, http.StatusForbidden, "forbidden")
		return
	}
	if w.controllerAuth != nil || w.cfg.DatabaseMode != sqldb.ModeSQLite {
		writeOperationError(rw, http.StatusConflict, "setup_unavailable", "controller setup is unavailable")
		return
	}
	if !w.hasSession(r) {
		writeError(rw, http.StatusUnauthorized, "unauthorized")
		return
	}
	var request struct {
		Username string `json:"username"`
	}
	if err := readJSON(rw, r, &request); err != nil {
		writeError(rw, http.StatusBadRequest, "invalid json")
		return
	}
	username, err := controlauth.NormalizeUsername(request.Username)
	if err != nil {
		writeOperationError(rw, http.StatusBadRequest, "invalid_username", "invalid username")
		return
	}
	secret, err := controlauth.NewTOTPSecret(nil)
	if err != nil {
		writeError(rw, http.StatusInternalServerError, "setup failed")
		return
	}
	uri := "otpauth://totp/" + url.PathEscape("AWG-Forge:"+username) + "?secret=" + url.QueryEscape(secret) + "&issuer=AWG-Forge&algorithm=SHA1&digits=6&period=30"
	code, err := qr.Encode(uri, qr.M, qr.Auto)
	if err != nil {
		writeError(rw, http.StatusInternalServerError, "setup failed")
		return
	}
	var image bytes.Buffer
	if err := png.Encode(&image, renderQRCodeImage(code)); err != nil {
		writeError(rw, http.StatusInternalServerError, "setup failed")
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"totp_secret": secret, "qr_png": "data:image/png;base64," + base64.StdEncoding.EncodeToString(image.Bytes())})
}

func (w *web) controllerActivateAPI(rw http.ResponseWriter, r *http.Request) {
	noStore(rw)
	if r.Method != http.MethodPost || !w.validOrigin(r) {
		writeError(rw, http.StatusForbidden, "forbidden")
		return
	}
	var request controllerActivationRequest
	if err := readJSON(rw, r, &request); err != nil {
		writeError(rw, http.StatusBadRequest, "invalid json")
		return
	}
	w.authMu.RLock()
	allowed := w.controllerAuth == nil && !w.activating.Load() && w.hasSession(r)
	w.authMu.RUnlock()
	if !allowed {
		writeError(rw, http.StatusUnauthorized, "unauthorized")
		return
	}
	if w.cfg.DatabaseMode != sqldb.ModeSQLite {
		writeOperationError(rw, http.StatusConflict, "database_required", "controller activation requires SQLite")
		return
	}
	if !w.activating.CompareAndSwap(false, true) {
		writeOperationError(rw, http.StatusConflict, "auth_transition", "authentication is changing")
		return
	}
	if w.activationStop != nil {
		close(w.activationStop)
	}
	w.authMu.Lock()
	defer w.authMu.Unlock()
	// Another activation can only start after a complete standalone rollback.
	if w.controllerAuth != nil {
		writeOperationError(rw, http.StatusConflict, "already_active", "controller already active")
		return
	}
	result, err := w.service.ActivateController(r.Context(), app.ControllerActivationRequest{
		Username: request.Username, Password: request.Password, TOTPSecret: request.TOTPSecret,
		TOTPConfirmation: request.ConfirmationCode, Now: time.Now().UTC(),
	})
	if err != nil {
		state, stateErr := w.service.State()
		if stateErr == nil && state.EffectiveMode() == config.ModeStandalone {
			w.activationStop = make(chan struct{})
			w.activating.Store(false)
		}
		if errors.Is(err, controlauth.ErrInvalidCredentials) {
			writeOperationError(rw, http.StatusUnauthorized, "invalid_credentials", "invalid credentials")
			return
		}
		if errors.Is(err, controlauth.ErrInvalidPassword) || errors.Is(err, controlauth.ErrInvalidUsername) {
			writeOperationError(rw, http.StatusBadRequest, "invalid_setup", "invalid controller setup")
			return
		}
		writeOperationError(rw, http.StatusServiceUnavailable, "activation_failed", "controller activation failed")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), w.cfg.DatabaseQueryTimeout)
	defer cancel()
	auth, db, err := openControllerAuthRuntime(ctx, w.cfg)
	if err != nil {
		// The state has committed. Keep legacy authentication closed; restart can load the runtime.
		writeOperationError(rw, http.StatusServiceUnavailable, "auth_runtime_unavailable", "controller authentication is unavailable")
		return
	}
	w.controllerAuth, w.controllerDB = auth, db
	http.SetCookie(rw, sessionCookie(r, result.Authentication.Token, 0, w.sessionCookieSecure(r)))
	writeJSON(rw, http.StatusOK, map[string]any{"mode": "controller", "username": result.Username, "recovery_codes": result.RecoveryCodes})
}

func openControllerAuthRuntime(ctx context.Context, cfg config.Config) (*controlauth.Service, *sqldb.DB, error) {
	if cfg.DatabaseMode != sqldb.ModeSQLite {
		return nil, nil, app.ErrControllerActivationRequiresDB
	}
	db, err := sqldb.Open(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	if err := db.Migrate(ctx); err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	initialized, err := db.ControllerAuthInitialized(ctx)
	if err != nil || !initialized {
		_ = db.Close()
		return nil, nil, errors.New("controller administrator unavailable")
	}
	keys, err := controlauth.LoadKeys(filepath.Join(cfg.ConfigDir, controlauth.KeyFileName))
	if err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	auth, err := controlauth.NewService(db, keys, controlauth.Options{})
	if err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	return auth, db, nil
}

func (w *web) closeControllerDB() {
	w.authMu.Lock()
	defer w.authMu.Unlock()
	if w.controllerDB != nil {
		_ = w.controllerDB.Close()
		w.controllerDB = nil
	}
}

func (w *web) controllerLoginAPI(rw http.ResponseWriter, r *http.Request) {
	var request controllerLoginRequest
	if err := readJSON(rw, r, &request); err != nil {
		writeError(rw, http.StatusBadRequest, "invalid json")
		return
	}
	authentication, err := w.controllerAuth.Authenticate(r.Context(), request.Username, request.Password, request.Code, w.clientIP(r), time.Now().UTC())
	if err != nil {
		controllerAuthError(rw, err)
		return
	}
	http.SetCookie(rw, sessionCookie(r, authentication.Token, 0, w.sessionCookieSecure(r)))
	writeJSON(rw, http.StatusOK, map[string]any{"ok": true})
}

func (w *web) controllerRecoveryLoginAPI(rw http.ResponseWriter, r *http.Request) {
	w.authMu.RLock()
	defer w.authMu.RUnlock()
	noStore(rw)
	if r.Method != http.MethodPost || !w.validOrigin(r) {
		writeError(rw, http.StatusForbidden, "forbidden")
		return
	}
	if w.controllerAuth == nil {
		writeOperationError(rw, http.StatusConflict, "controller_required", "controller mode is required")
		return
	}
	var request controllerLoginRequest
	if err := readJSON(rw, r, &request); err != nil {
		writeError(rw, http.StatusBadRequest, "invalid json")
		return
	}
	authentication, err := w.controllerAuth.AuthenticateRecovery(r.Context(), request.Username, request.Password, request.Code, w.clientIP(r), time.Now().UTC())
	if err != nil {
		controllerAuthError(rw, err)
		return
	}
	http.SetCookie(rw, sessionCookie(r, authentication.Token, 0, w.sessionCookieSecure(r)))
	writeJSON(rw, http.StatusOK, map[string]any{"ok": true})
}

func (w *web) controllerReauthAPI(rw http.ResponseWriter, r *http.Request) {
	noStore(rw)
	if r.Method != http.MethodPost || !w.validOrigin(r) {
		writeError(rw, http.StatusForbidden, "forbidden")
		return
	}
	if w.controllerAuth == nil {
		writeOperationError(rw, http.StatusConflict, "controller_required", "controller mode is required")
		return
	}
	cookie, err := r.Cookie("awg_forge_session")
	if err != nil {
		writeError(rw, http.StatusUnauthorized, "unauthorized")
		return
	}
	var request controllerReauthRequest
	if err := readJSON(rw, r, &request); err != nil {
		writeError(rw, http.StatusBadRequest, "invalid json")
		return
	}
	authentication, err := w.controllerAuth.Reauthenticate(r.Context(), cookie.Value, request.Password, request.Code, w.clientIP(r), request.Recovery, time.Now().UTC())
	if err != nil {
		controllerAuthError(rw, err)
		return
	}
	http.SetCookie(rw, sessionCookie(r, authentication.Token, 0, w.sessionCookieSecure(r)))
	writeJSON(rw, http.StatusOK, map[string]any{"ok": true})
}

func (w *web) controllerRecoveryCodesAPI(rw http.ResponseWriter, r *http.Request) {
	noStore(rw)
	if r.Method != http.MethodPost || !w.validOrigin(r) {
		writeError(rw, http.StatusForbidden, "forbidden")
		return
	}
	if w.controllerAuth == nil {
		writeOperationError(rw, http.StatusConflict, "controller_required", "controller mode is required")
		return
	}
	cookie, err := r.Cookie("awg_forge_session")
	if err != nil {
		writeError(rw, http.StatusUnauthorized, "unauthorized")
		return
	}
	codes, err := w.controllerAuth.RotateRecoveryCodes(r.Context(), cookie.Value, time.Now().UTC())
	if err != nil {
		controllerAuthError(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"recovery_codes": codes})
}

func controllerAuthError(rw http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, controlauth.ErrRateLimited):
		writeOperationError(rw, http.StatusTooManyRequests, "rate_limited", "too many attempts")
	case errors.Is(err, controlauth.ErrRecentAuthRequired):
		writeOperationError(rw, http.StatusForbidden, "recent_auth_required", "recent authentication required")
	case errors.Is(err, controlauth.ErrSessionNotFound):
		writeError(rw, http.StatusUnauthorized, "unauthorized")
	case errors.Is(err, controlauth.ErrInvalidCredentials):
		writeOperationError(rw, http.StatusUnauthorized, "invalid_credentials", "invalid credentials")
	default:
		writeOperationError(rw, http.StatusInternalServerError, "auth_failed", "authentication failed")
	}
}
