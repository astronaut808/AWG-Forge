package nodeagent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/astronaut808/awg-forge/internal/controlpki"
	"github.com/google/uuid"
)

func TestObservationWorkerRetriesPresenceThenSendsSnapshot(t *testing.T) {
	var presence atomic.Int32
	var controllerID string
	snapshotSeen := make(chan struct{}, 1)
	server, service, cfg, fixtureControllerID := observationWorkerFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/control/v1/node/presence":
			if presence.Add(1) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode(controlapi.PresenceAccepted{ControllerID: controllerID, SessionID: uuid.NewString(), SessionExpiresAt: time.Now().Add(time.Minute), NextPollSeconds: 1})
		case "/control/v1/node/snapshot":
			select {
			case snapshotSeen <- struct{}{}:
			default:
			}
			_ = json.NewEncoder(w).Encode(controlapi.SnapshotAccepted{Sequence: 1, ReceivedAt: time.Now().UTC()})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	controllerID = fixtureControllerID
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, service, cfg) }()
	select {
	case <-snapshotSeen:
		if presence.Load() < 2 {
			t.Fatalf("presence attempts = %d", presence.Load())
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("cancelled worker = %v", err)
		}
	case err := <-done:
		t.Fatalf("worker stopped before snapshot: %v", err)
	case <-ctx.Done():
		t.Fatal("snapshot was not sent after temporary presence failure")
	}
}

func TestObservationWorkerStopsOnPresenceAuthorizationFailures(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusConflict} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server, service, cfg, _ := observationWorkerFixture(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) })
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- Run(ctx, service, cfg) }()
			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), "authorization revoked") {
					t.Fatalf("Run error = %v", err)
				}
			case <-ctx.Done():
				t.Fatal("authorization failure retried")
			}
		})
	}
}

func TestObservationWorkerCancellationInterruptsUnresponsivePeer(t *testing.T) {
	started, release := make(chan struct{}, 1), make(chan struct{})
	server, service, cfg, _ := observationWorkerFixture(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release // Do not read a request or produce headers.
	})
	defer func() { close(release); server.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, service, cfg) }()
	select {
	case <-started:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("worker did not reach peer")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cancelled worker = %v", err)
		}
	case <-time.After(750 * time.Millisecond):
		t.Fatal("worker shutdown exceeded request cancellation budget")
	}
}

func observationWorkerFixture(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *app.Service, config.Config, string) {
	t.Helper()
	material, pin, err := controlpki.Generate(controlpki.Endpoint{BindIP: "127.0.0.1", Advertised: "127.0.0.1", Port: 9443}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	serverCert, err := tls.X509KeyPair(material.ServerCert, material.ServerKey)
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	issued, err := controlpki.IssueNodeCertificate(material, csr, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(material.CACert) {
		t.Fatal("test CA")
	}
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{serverCert}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots}
	server.StartTLS()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{ConfigDir: dir, ServerHost: "127.0.0.1", ExternalInterface: "lo"}
	service := app.New(cfg)
	controllerID := uuid.NewString()
	invitation := controlapi.Invitation{ControllerURL: server.URL, CAPin: pin, CACertPEM: string(material.CACert)}
	approved := controlapi.EnrollmentStatus{Status: "approved", ContractVersion: 1, NodeID: uuid.NewString(), ControllerID: controllerID, BindingEpoch: 1, Certificate: &controlapi.IssuedCertificate{CertificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: issued.DER})), ChainPEM: string(material.CACert), Serial: issued.Serial, NotBefore: issued.NotBefore, NotAfter: issued.NotAfter}}
	if err := service.InstallNodeEnrollment(context.Background(), invitation, approved, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})); err != nil {
		t.Fatal(err)
	}
	return server, service, cfg, controllerID
}
