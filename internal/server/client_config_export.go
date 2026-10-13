package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/astronaut808/awg-forge/internal/controlauth"
)

// Waiting stays below the web server's 30-second write timeout. A fresh export
// is required after timeout or lost response; consuming downloads never retry.
const clientConfigExportDownloadWait = 20 * time.Second

func (w *web) clientConfigExportAPI(rw http.ResponseWriter, r *http.Request, parts []string) {
	noStore(rw)
	if !canonicalNodeUUID(parts[0]) || (len(parts) != 2 && (len(parts) != 3 || parts[2] != "download" && parts[2] != "cancel")) {
		writeError(rw, 404, "not found")
		return
	}
	if r.Method != http.MethodPost {
		writeError(rw, 405, "method not allowed")
		return
	}
	token, ok := w.controlSession(rw, r, http.MethodPost)
	if !ok {
		return
	}
	if r.URL.RawQuery != "" {
		writeOperationError(rw, 400, "invalid_export", "invalid export request")
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(rw, r.Body, controlapi.MaxOperationBytes))
	if err != nil {
		writeOperationError(rw, 400, "invalid_export", "invalid export request")
		return
	}
	wait := 5 * time.Second
	if len(parts) == 3 && parts[2] == "download" {
		wait = clientConfigExportDownloadWait
	}
	ctx, cancel := context.WithTimeout(r.Context(), wait)
	defer cancel()
	if len(parts) == 2 {
		var request controlapi.ClientConfigExportRequest
		if controlapi.DecodeClientConfigExport(raw, &request) != nil || request.Validate() != nil {
			writeOperationError(rw, 400, "invalid_export", "invalid export request")
			return
		}
		result, err := w.service.PrepareClientConfigExport(ctx, token, parts[0], request)
		if err != nil {
			browserClientConfigExportError(rw, err)
			return
		}
		writeJSON(rw, 202, result)
		return
	}
	var locator controlapi.ClientConfigExportLocator
	if controlapi.DecodeClientConfigExport(raw, &locator) != nil || locator.Validate() != nil {
		writeOperationError(rw, 400, "invalid_export", "invalid export request")
		return
	}
	if parts[2] == "cancel" {
		if err := w.service.CancelClientConfigExport(ctx, token, parts[0], locator.ExportID); err != nil {
			browserClientConfigExportError(rw, err)
			return
		}
		rw.WriteHeader(204)
		return
	}
	// net/http starts WriteTimeout before reading the body. Reset it only now,
	// so the complete bounded wait and write budget remain after a slow body.
	if err := clientConfigExportWriteDeadline(rw); err != nil {
		browserClientConfigExportError(rw, err)
		return
	}
	value, filename, err := w.service.DownloadClientConfigExport(ctx, token, parts[0], locator.ExportID)
	defer clear(value)
	if err != nil {
		browserClientConfigExportError(rw, err)
		return
	}
	rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
	rw.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	rw.Header().Set("X-Content-Type-Options", "nosniff")
	rw.WriteHeader(200)
	writeRawResponse(rw, value)
}

func clientConfigExportWriteDeadline(rw http.ResponseWriter) error {
	err := http.NewResponseController(rw).SetWriteDeadline(time.Now().Add(clientConfigExportDownloadWait + 10*time.Second))
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	} // In-memory HTTP test writers.
	return err
}

func browserClientConfigExportError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, controlauth.ErrRecentAuthRequired):
		writeOperationError(w, 403, "recent_auth_required", "recent authentication required")
	case errors.Is(err, app.ErrClientConfigExportUnavailable), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		writeOperationError(w, 409, "export_unavailable", "export unavailable; prepare a new export")
	default:
		browserRefreshError(w, err)
	}
}
