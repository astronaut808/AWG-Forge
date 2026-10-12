package server

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/astronaut808/awg-forge/internal/sqldb"
	"github.com/google/uuid"
)

func canonicalNodeUUID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}
func (w *web) nodeRefreshAPI(rw http.ResponseWriter, r *http.Request, parts []string) {
	noStore(rw)
	if w.controllerAuth == nil {
		writeOperationError(rw, 409, "controller_required", "active controller required")
		return
	}
	if len(parts) < 2 || !canonicalNodeUUID(parts[0]) {
		writeError(rw, 404, "not found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	switch {
	case len(parts) == 2 && parts[1] == "client-enabled":
		if r.Method != http.MethodPost {
			writeError(rw, 405, "method not allowed")
			return
		}
		if !w.validOrigin(r) {
			writeError(rw, 403, "forbidden")
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(rw, r.Body, controlapi.MaxOperationBytes))
		var request controlapi.ClientEnabledRequest
		if err != nil || controlapi.DecodeOperation(raw, &request) != nil || request.Validate() != nil {
			writeOperationError(rw, 400, "invalid_client_operation", "invalid client operation")
			return
		}
		result, err := w.service.CreateNodeClientEnabled(ctx, parts[0], request)
		if err != nil {
			browserRefreshError(rw, err)
			return
		}
		writeJSON(rw, 202, result)
	case len(parts) == 2 && parts[1] == "refresh":
		if r.Method != http.MethodPost {
			writeError(rw, 405, "method not allowed")
			return
		}
		if !w.validOrigin(r) {
			writeError(rw, 403, "forbidden")
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(rw, r.Body, controlapi.MaxOperationBytes))
		var request controlapi.RefreshRequest
		if err != nil || controlapi.DecodeOperation(raw, &request) != nil || request.Validate() != nil {
			writeOperationError(rw, 400, "invalid_refresh", "invalid refresh request")
			return
		}
		result, err := w.service.CreateNodeRefresh(ctx, parts[0], request)
		if err != nil {
			browserRefreshError(rw, err)
			return
		}
		writeJSON(rw, 202, result)
	case len(parts) == 3 && parts[1] == "operations" && canonicalNodeUUID(parts[2]):
		if r.Method != http.MethodGet {
			writeError(rw, 405, "method not allowed")
			return
		}
		result, err := w.service.NodeRefreshStatus(ctx, parts[0], parts[2])
		if err != nil {
			browserRefreshError(rw, err)
			return
		}
		writeJSON(rw, 200, result)
	default:
		writeError(rw, 404, "not found")
	}
}
func browserRefreshError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeOperationError(w, 404, "operation_not_found", "refresh not found")
	case errors.Is(err, sqldb.ErrOperationFenced):
		writeOperationError(w, 409, "operation_fenced", "refresh conflicts with current node state")
	case errors.Is(err, sqldb.ErrOperationUnsupported):
		writeOperationError(w, 409, "refresh_unsupported", "node does not support refresh")
	case errors.Is(err, sqldb.ErrOperationOffline):
		writeOperationError(w, 409, "node_unavailable", "node is unavailable")
	case errors.Is(err, sqldb.ErrOperationLimit):
		w.Header().Set("Retry-After", "5")
		writeOperationError(w, 429, "operation_limit", "refresh limit reached")
	default:
		writeOperationError(w, 503, "control_unavailable", "refresh unavailable")
	}
}
func nodeRefreshPath(path string) ([]string, bool) {
	if !strings.HasPrefix(path, "/api/controller/nodes/") {
		return nil, false
	}
	parts := strings.Split(strings.TrimPrefix(path, "/api/controller/nodes/"), "/")
	return parts, len(parts) > 1
}
