package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/google/uuid"
)

func TestBrowserClientConfigExportAdmission(t *testing.T) {
	auth, closeDB, now := controllerServerTestAuthAt(t, time.Now().Add(-time.Minute))
	defer closeDB()
	w := &web{controllerAuth: auth, sessions: []byte("test-session-secret")}
	handler := w.security(w.requireAuth(w.controllerNodesAPI))
	a, err := auth.Authenticate(context.Background(), "admin", "correct horse battery staple", controllerServerTOTPCode(t, now.Add(30*time.Second)), "127.0.0.1", now.Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	request := controlapi.ClientConfigExportRequest{StateEpoch: uuid.NewString(), BindingEpoch: 1, TunnelID: "aaaaaaaaaaaaaaaa", ClientID: "bbbbbbbbbbbbbbbb", TunnelRevision: 1}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"", "/download", "/cancel"} {
		valid := string(raw)
		if endpoint != "" {
			valid = `{"export_id":"` + uuid.NewString() + `"}`
		}
		for _, c := range []struct {
			name, body, origin string
			cookie             bool
			status             int
		}{
			{"unauthenticated", valid, "https://admin.example.test", false, 401},
			{"cross-origin", valid, "https://evil.example.test", true, 403},
			{"missing", `{}`, "https://admin.example.test", true, 400},
			{"unknown", valid[:len(valid)-1] + `,"config":"never-echo"}`, "https://admin.example.test", true, 400},
		} {
			r := httptest.NewRequest(http.MethodPost, "https://admin.example.test/api/controller/nodes/"+uuid.NewString()+"/client-config-exports"+endpoint, strings.NewReader(c.body))
			r.Header.Set("Origin", c.origin)
			if c.cookie {
				r.AddCookie(sessionCookie(r, a.Token, 0, false))
			}
			result := httptest.NewRecorder()
			handler.ServeHTTP(result, r)
			if result.Code != c.status || result.Header().Get("Cache-Control") != "no-store" || strings.Contains(result.Body.String(), "never-echo") {
				t.Fatal("browser export admission", endpoint, c.name, result.Code)
			}
		}
	}
	if clientConfigExportDownloadWait >= webWriteTimeout {
		t.Fatal("wait exceeds web write budget")
	}
}

func TestClientConfigExportHTTPDeadlineAfterSlowBody(t *testing.T) {
	// Real net/http deadlines, not a recorder: original write timeout expires
	// during the request body. The consuming response gets a new bounded budget.
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			t.Error(err)
			return
		}
		if err := clientConfigExportWriteDeadline(w); err != nil {
			t.Error(err)
			return
		}
		<-time.After(80 * time.Millisecond)
		w.Header().Set("Content-Type", "text/plain")
		if _, err := io.WriteString(w, "disposable-test-config"); err != nil {
			t.Error(err)
		}
	}))
	server.Config.WriteTimeout = 50 * time.Millisecond
	server.Start()
	defer server.Close()
	reader, writer := io.Pipe()
	go func() {
		defer func() { _ = writer.Close() }()
		_, _ = writer.Write([]byte("{"))
		<-time.After(100 * time.Millisecond)
		_, _ = writer.Write([]byte("}"))
	}()
	response, err := server.Client().Post(server.URL, "application/json", reader)
	if err != nil {
		t.Fatal("response lost after slow body", err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != 200 || string(raw) != "disposable-test-config" {
		t.Fatal("response deadline not renewed")
	}
}

func TestBrowserClientConfigExportRequiresRecentMFA(t *testing.T) {
	auth, closeDB, now := controllerServerTestAuthAt(t, time.Now().Add(-10*time.Minute))
	defer closeDB()
	a, err := auth.Authenticate(context.Background(), "admin", "correct horse battery staple", controllerServerTOTPCode(t, now), "127.0.0.1", now)
	if err != nil {
		t.Fatal(err)
	}
	w := &web{controllerAuth: auth}
	r := httptest.NewRequest(http.MethodPost, "https://admin.example.test/api/controller/nodes/"+uuid.NewString()+"/client-config-exports", strings.NewReader(`{}`))
	r.Header.Set("Origin", "https://admin.example.test")
	r.AddCookie(sessionCookie(r, a.Token, 0, false))
	result := httptest.NewRecorder()
	w.controllerNodesAPI(result, r)
	if result.Code != 403 || !strings.Contains(result.Body.String(), "recent_auth_required") {
		t.Fatal("missing recent MFA admitted")
	}
}
