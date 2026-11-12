package server

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

func (w *web) controllerNodesAPI(rw http.ResponseWriter, r *http.Request) {
	noStore(rw)
	if parts, ok := nodeRefreshPath(r.URL.Path); ok {
		if len(parts) >= 2 && parts[1] == "client-config-exports" {
			w.clientConfigExportAPI(rw, r, parts)
			return
		}
		w.nodeRefreshAPI(rw, r, parts)
		return
	}
	if r.Method != http.MethodGet {
		writeError(rw, 405, "method not allowed")
		return
	}
	if w.controllerAuth == nil {
		writeOperationError(rw, 409, "controller_required", "active controller required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if r.URL.Path == "/api/controller/nodes" {
		after := r.URL.Query().Get("after")
		if after != "" {
			cursor, err := uuid.Parse(after)
			if err != nil || cursor == uuid.Nil || cursor.String() != after {
				writeOperationError(rw, 400, "invalid_cursor", "invalid inventory cursor")
				return
			}
		}
		result, err := w.service.ControllerNodes(ctx, after)
		if err != nil {
			writeError(rw, 503, "node inventory unavailable")
			return
		}
		writeJSON(rw, 200, result)
		return
	}
	nodeID := strings.TrimPrefix(r.URL.Path, "/api/controller/nodes/")
	result, err := w.service.ControllerNodeProjection(ctx, nodeID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(rw, 404, "node not found")
		return
	}
	if err != nil {
		writeError(rw, 503, "node snapshot unavailable")
		return
	}
	writeJSON(rw, 200, result)
}
