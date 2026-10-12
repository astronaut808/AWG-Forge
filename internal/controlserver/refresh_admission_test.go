package controlserver

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlpki"
)

type refreshAdmissionAuthorizer struct{}

func (refreshAdmissionAuthorizer) Authorize(_ context.Context, cert *x509.Certificate, _ string) (NodeIdentity, error) {
	return NodeIdentity{ControllerID: "controller", NodeID: cert.Subject.CommonName, BindingEpoch: 1}, nil
}

func TestOperationPollAdmissionBoundsAndReleasesSlots(t *testing.T) {
	endpoint := loopbackEndpoint(t)
	material, pin, err := controlpki.Generate(endpoint, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan string, 9)
	runtime, err := New(material, endpoint, pin, refreshAdmissionAuthorizer{}, []Route{
		{ID: "node.operation-poll", Method: http.MethodPost, Path: "/control/v1/node/operations/poll", Handler: http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			identity, ok := IdentityFromContext(r.Context())
			if !ok {
				t.Error("poll handler missing identity")
				return
			}
			started <- identity.NodeID
			<-r.Context().Done()
		})},
		{ID: "node.presence", Method: http.MethodPut, Path: "/control/v1/node/presence", Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})},
	})
	if err != nil {
		t.Fatal(err)
	}

	defer runtime.Close()
	handler := runtime.handler()
	cert := func(node string) *x509.Certificate {
		return &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: node}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Minute)}
	}
	request := func(ctx context.Context, method, path, node string) *http.Request {
		r := httptest.NewRequestWithContext(ctx, method, "https://control.example.test"+path, nil)
		peer := cert(node)
		r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{peer}, VerifiedChains: [][]*x509.Certificate{{peer}}}
		return r
	}
	type response struct{ code int }
	poll := func(ctx context.Context, node string) <-chan response {
		done := make(chan response, 1)
		go func() {
			rw := httptest.NewRecorder()
			handler.ServeHTTP(rw, request(ctx, http.MethodPost, "/control/v1/node/operations/poll", node))
			done <- response{rw.Code}
		}()
		return done
	}
	waitStarted := func(node string) {
		t.Helper()
		select {
		case got := <-started:
			if got != node {
				t.Fatalf("started node = %q, want %q", got, node)
			}
		case <-time.After(time.Second):
			t.Fatalf("poll for %q was not admitted", node)
		}
	}

	firstCtx, cancelFirst := context.WithCancel(runtime.requestContext)
	first := poll(firstCtx, "node-0")
	waitStarted("node-0")
	duplicate := httptest.NewRecorder()
	handler.ServeHTTP(duplicate, request(context.Background(), http.MethodPost, "/control/v1/node/operations/poll", "node-0"))
	if duplicate.Code != http.StatusTooManyRequests {
		t.Fatalf("same-node poll status = %d, want %d", duplicate.Code, http.StatusTooManyRequests)
	}

	held := []<-chan response{first}
	for i := 1; i < 8; i++ {
		node := "node-" + strconv.Itoa(i)
		held = append(held, poll(runtime.requestContext, node))
		waitStarted(node)
	}
	global := httptest.NewRecorder()
	handler.ServeHTTP(global, request(context.Background(), http.MethodPost, "/control/v1/node/operations/poll", "node-8"))
	if global.Code != http.StatusTooManyRequests {
		t.Fatalf("ninth poll status = %d, want %d", global.Code, http.StatusTooManyRequests)
	}
	presence := httptest.NewRecorder()
	handler.ServeHTTP(presence, request(context.Background(), http.MethodPut, "/control/v1/node/presence", "node-0"))
	if presence.Code != http.StatusNoContent {
		t.Fatalf("presence while polls held = %d, want %d", presence.Code, http.StatusNoContent)
	}

	cancelFirst()
	select {
	case got := <-first:
		if got.code != http.StatusOK {
			t.Fatalf("cancelled poll status = %d, want %d", got.code, http.StatusOK)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled poll did not release")
	}
	replacement := poll(runtime.requestContext, "node-0")
	waitStarted("node-0")
	held[0] = replacement

	runtime.Close()
	var waits sync.WaitGroup
	for _, done := range held {
		waits.Add(1)
		go func(done <-chan response) {
			defer waits.Done()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("shutdown did not release held poll")
			}
		}(done)
	}
	waits.Wait()
	runtime.WaitForRequests()
}
