package server

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"
)

func (w *web) controllerNodesAPI(rw http.ResponseWriter, r *http.Request) {
	noStore(rw)
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
		result, err := w.service.ControllerNodes(ctx)
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
