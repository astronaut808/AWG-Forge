package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestControllerInventoryRejectsInvalidCursorAndRequiresAuth(t *testing.T) {
	auth, closeDB, _ := controllerServerTestAuth(t)
	defer closeDB()
	w := &web{controllerAuth: auth}
	for _, cursor := range []string{"invalid", "00000000-0000-0000-0000-000000000000", "22222222222242228222222222222222"} {
		r := httptest.NewRequest(http.MethodGet, "/api/controller/nodes?after="+cursor, nil)
		result := httptest.NewRecorder()
		w.controllerNodesAPI(result, r)
		if result.Code != http.StatusBadRequest || result.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("cursor %q: status=%d headers=%v", cursor, result.Code, result.Header())
		}
		unauthorized := httptest.NewRecorder()
		w.requireAuth(w.controllerNodesAPI)(unauthorized, r)
		if unauthorized.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated page status=%d", unauthorized.Code)
		}
	}
}
