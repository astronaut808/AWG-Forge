package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testBrowserClientCRUDAdmission(t *testing.T, action string) {
	auth, closeDB, now := controllerServerTestAuth(t)
	defer closeDB()
	w := &web{controllerAuth: auth, sessions: []byte("test-session-secret")}
	handler := w.security(w.requireAuth(w.controllerNodesAPI))
	node := uuid.NewString()
	valid := `{"operation_id":"` + uuid.NewString() + `","idempotency_key":"` + uuid.NewString() + `","expected_state_epoch":"` + uuid.NewString() + `","expected_binding_epoch":1,"expected_desired_generation":1,"tunnel_id":"awg0","client_id":"0123456789abcdef","name":"phone"}`
	if action == "delete" {
		valid = strings.Replace(valid, `"name":"phone"`, `"confirmed":true`, 1)
	}
	authentication, err := auth.Authenticate(context.Background(), "admin", "correct horse battery staple", controllerServerTOTPCode(t, now.Add(30*time.Second)), "127.0.0.1", now.Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}

	request := func(body string, origin string, authenticated bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "https://admin.example.test/api/controller/nodes/"+node+"/client-"+action, strings.NewReader(body))
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if authenticated {
			r.AddCookie(sessionCookie(r, authentication.Token, 0, false))
		}
		rw := httptest.NewRecorder()
		handler.ServeHTTP(rw, r)
		if got := rw.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("cache control = %q, want no-store", got)
		}
		return rw
	}

	if got := request(valid, "https://admin.example.test", false).Code; got != http.StatusUnauthorized {
		t.Fatalf("unauthenticated client operation status = %d, want %d", got, http.StatusUnauthorized)
	}
	if got := request(valid, "https://evil.example.test", true).Code; got != http.StatusForbidden {
		t.Fatalf("cross-origin client operation status = %d, want %d", got, http.StatusForbidden)
	}
	if got := request(valid[:len(valid)-1]+`,"unexpected":true}`, "https://admin.example.test", true).Code; got != http.StatusBadRequest {
		t.Fatalf("same-origin unknown-field client operation status = %d, want %d", got, http.StatusBadRequest)
	}
	for _, field := range []string{"operation_id", "idempotency_key"} {
		body := `{"operation_id":"` + uuid.NewString() + `","idempotency_key":"` + uuid.NewString() + `","expected_state_epoch":"` + uuid.NewString() + `","expected_binding_epoch":1,"expected_desired_generation":0,"tunnel_id":"awg0","client_id":"0123456789abcdef","name":"phone"}`
		var fields map[string]any
		if err := json.Unmarshal([]byte(body), &fields); err != nil {
			t.Fatal(err)
		}
		fields[field] = uuid.Nil.String()
		raw, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		if got := request(string(raw), "https://admin.example.test", true).Code; got != http.StatusBadRequest {
			t.Fatal("nil intent admitted", field, got)
		}
	}
	invalidBodies := []string{
		strings.Replace(valid, `,"name":"phone"`, "", 1),
		strings.Replace(valid, `"name":"phone"`, `"name":null`, 1),
		strings.Replace(valid, `"name":"phone"`, `"name":false`, 1),
		strings.Replace(valid, `"client_id":"0123456789abcdef"`, `"client_id":"../state"`, 1),
		valid[:len(valid)-1] + `,"raw_config":"canary"}`,
		strings.Repeat(" ", 4097) + valid,
	}
	if action == "delete" {
		invalidBodies = []string{strings.Replace(valid, `,"confirmed":true`, "", 1), strings.Replace(valid, `"confirmed":true`, `"confirmed":null`, 1), strings.Replace(valid, `"confirmed":true`, `"confirmed":false`, 1), valid[:len(valid)-1] + `,"name":"phone"}`}
	}
	for _, body := range invalidBodies {
		if got := request(body, "https://admin.example.test", true).Code; got != http.StatusBadRequest {
			t.Fatalf("malformed client operation admitted: status=%d", got)
		}
	}
	if got := request(`{"operation_id":"`+uuid.NewString()+`"}`, "https://admin.example.test", true).Code; got != http.StatusBadRequest {
		t.Fatalf("same-origin missing-field client operation status = %d, want %d", got, http.StatusBadRequest)
	}
}

func TestBrowserClientCRUDAdmission(t *testing.T) {
	for _, action := range []string{"create", "delete"} {
		t.Run(action, func(t *testing.T) { testBrowserClientCRUDAdmission(t, action) })
	}
}
