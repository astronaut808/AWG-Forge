package server

import (
	"errors"
	"net/http"
	"os"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
)

func (w *web) tunnelRelaysAPI(rw http.ResponseWriter, r *http.Request, tunnelID string, path []string) {
	switch {
	case len(path) == 1 && path[0] == "health" && r.Method == http.MethodGet:
		noStore(rw)
		health, err := w.service.RelayHealthByTunnel(tunnelID)
		switch {
		case errors.Is(err, os.ErrNotExist):
			writeError(rw, http.StatusNotFound, "tunnel not found")
		case err != nil:
			writeOperationError(rw, http.StatusInternalServerError, "relay_health_failed", "failed to read relay health")
		default:
			writeJSON(rw, http.StatusOK, map[string]any{"health": health})
		}
	case len(path) == 1 && path[0] == "suggestion" && r.Method == http.MethodGet:
		noStore(rw)
		ip, err := w.service.RelayTargetSuggestion(r.Context(), tunnelID)
		switch {
		case errors.Is(err, os.ErrNotExist):
			writeError(rw, http.StatusNotFound, "tunnel not found")
		case err != nil:
			writeOperationError(rw, http.StatusInternalServerError, "relay_suggestion_failed", "failed to suggest relay target")
		default:
			writeJSON(rw, http.StatusOK, map[string]string{"target_ipv4": ip})
		}
	case len(path) == 0 && r.Method == http.MethodGet:
		state, err := w.service.State()
		if err != nil {
			writeOperationError(rw, http.StatusInternalServerError, "relay_list_failed", "failed to list relays")
			return
		}
		for _, tunnel := range state.Tunnels {
			if tunnel.ID == tunnelID {
				relays := append([]config.Relay{}, tunnel.Relays...)
				writeJSON(rw, http.StatusOK, map[string]any{"relays": relays})
				return
			}
		}
		writeError(rw, http.StatusNotFound, "tunnel not found")
	case len(path) == 2 && path[0] != "" && path[1] == "install" && r.Method == http.MethodGet:
		noStore(rw)
		command, err := w.service.RelayInstallCommand(tunnelID, path[0])
		if err != nil {
			writeOperationError(rw, http.StatusBadRequest, "relay_install_unavailable", "relay install command unavailable")
			return
		}
		writeJSON(rw, http.StatusOK, map[string]string{"command": command})
	case len(path) == 0 && r.Method == http.MethodPost:
		w.mutateRelayAPI(rw, r, tunnelID, "")
	case len(path) == 1 && path[0] != "" && (r.Method == http.MethodPut || r.Method == http.MethodDelete):
		w.mutateRelayAPI(rw, r, tunnelID, path[0])
	default:
		writeError(rw, http.StatusNotFound, "not found")
	}
}

func (w *web) mutateRelayAPI(rw http.ResponseWriter, r *http.Request, tunnelID, relayID string) {
	if !w.validOrigin(r) {
		writeError(rw, http.StatusForbidden, "forbidden")
		return
	}
	w.withIdempotency(rw, r, "relay:"+r.Method+":"+tunnelID+":"+relayID, func() (int, any) {
		if r.Method == http.MethodDelete {
			if err := w.service.RemoveRelay(tunnelID, relayID); err != nil {
				w.audit("warn", "relay.delete.rejected", "relay deletion rejected", map[string]any{"tunnel_id": tunnelID, "relay_id": relayID}, err)
				return mutationErrorStatus(err, http.StatusBadRequest), operationErrorPayload("relay_delete_failed", "failed to delete relay")
			}
			return http.StatusOK, map[string]bool{"ok": true}
		}
		var req struct {
			config.Relay
			Expected *config.Relay `json:"expected"`
		}
		if err := readJSON(rw, r, &req); err != nil {
			return http.StatusBadRequest, errorPayload("invalid json")
		}
		if req.Expected != nil {
			if relayID == "" {
				return http.StatusBadRequest, errorPayload("expected settings require an existing relay")
			}
			req.Expected.ID = relayID
		}
		relay, err := w.service.SaveRelayIfCurrent(tunnelID, relayID, req.Relay, req.Expected)
		if errors.Is(err, app.ErrRelayChanged) {
			return http.StatusConflict, operationErrorPayload("relay_changed", "relay changed since it was opened")
		}
		if err != nil {
			w.audit("warn", "relay.save.rejected", "relay save rejected", map[string]any{"tunnel_id": tunnelID, "relay_id": relayID}, err)
			return mutationErrorStatus(err, http.StatusBadRequest), operationErrorPayload("relay_save_failed", "failed to save relay")
		}
		status := http.StatusOK
		if r.Method == http.MethodPost {
			status = http.StatusCreated
		}
		return status, map[string]any{"relay": relay}
	})
}
