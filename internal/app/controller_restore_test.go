package app

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptrace"
	"reflect"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlpki"
	"github.com/astronaut808/awg-forge/internal/controlserver"
	"github.com/astronaut808/awg-forge/internal/sqldb"
	"github.com/astronaut808/awg-forge/internal/storage"
)

func TestControllerRestoreReconciliationDeniesTLSKeepAliveAndPrivateRetries(t *testing.T) {
	ctx := context.Background()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("loopback required for restore gate: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	now := time.Now().UTC()
	issuedAt := now.Add(-20 * 24 * time.Hour)
	cfg, service, control := serverRotationFixture(t, issuedAt, port)
	const nodeID = "11111111-1111-4111-8111-111111111111"
	csr, key := renewalTestCSR(t)
	issued, err := service.issueInitialNodeCertificate(ctx, nodeID, csr, issuedAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	old := renewalTestCertificate(t, issued)
	newCSR, _ := renewalTestCSR(t)
	successor, err := service.renewNodeCertificate(ctx, old, newCSR, now)
	if err != nil {
		t.Fatal(err)
	}
	current := renewalTestCertificate(t, successor)
	store := storage.New(cfg.ConfigDir)
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	material, err := store.LoadControlIdentity(control.CAGeneration, control.ServerGeneration)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sqldb.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	authorizer, err := NewControlNodeAuthorizer(db, state.Controller.ControllerID, control, material, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	var handled atomic.Int64
	runtime, err := controlserver.New(material, controlpki.Endpoint{BindIP: control.BindIP, Advertised: control.Advertised, Port: control.Port}, control.CAPin, authorizer, []controlserver.Route{{
		ID: "node.restore-test", Method: http.MethodGet, Path: "/control/v1/restore-test",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity, ok := controlserver.IdentityFromContext(r.Context())
			if !ok || identity.NodeID != nodeID {
				t.Error("handler did not receive registry identity")
			}
			handled.Add(1)
			w.WriteHeader(http.StatusNoContent)
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	serveCtx, cancel := context.WithCancel(ctx)
	result := make(chan error, 1)
	go func() { result <- runtime.Serve(serveCtx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-result; err != nil {
			t.Errorf("runtime shutdown: %v", err)
		}
	})
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(material.CACert) {
		t.Fatal("invalid CA")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: control.Advertised,
		Certificates: []tls.Certificate{{Certificate: [][]byte{old.Raw}, PrivateKey: key}},
	}}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	url := "https://" + net.JoinHostPort(control.BindIP, strconv.Itoa(control.Port)) + "/control/v1/restore-test"
	waitServerRotationTLS(t, client, url)
	if handled.Load() != 1 {
		t.Fatal("positive-control node request did not reach handler")
	}
	stateLock, err := storage.AcquireStateLock(cfg.ConfigDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stateLock.Close() }()
	mutationLock, err := storage.AcquireStateMutationLock(cfg.ConfigDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mutationLock.Close() }()
	if err := store.BeginRestorePending(state.Controller.ControllerID); err != nil {
		t.Fatal(err)
	}
	// The isolated test runtime observes the reset on the installed database;
	// production cold restore never retains a serving listener across file moves.
	if err := ReconcileRestoredController(ctx, cfg, state, now); err != nil {
		t.Fatal(err)
	}
	if err := mutationLock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := stateLock.Close(); err != nil {
		t.Fatal(err)
	}
	reused := false
	request, _ := http.NewRequest(http.MethodGet, url, nil)
	request = request.WithContext(httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused }}))
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if !reused || response.StatusCode != http.StatusForbidden || handled.Load() != 1 {
		t.Fatalf("restored keep-alive authority: status=%d, reused=%v, handled=%d", response.StatusCode, reused, handled.Load())
	}
	for _, cert := range []*x509.Certificate{old, current} {
		if _, err := authorizer.Authorize(ctx, cert, "node.restore-test"); !errors.Is(err, sqldb.ErrNodeCertificateDenied) {
			t.Fatalf("restored renewal overlap admitted: %v", err)
		}
	}
	if _, err := service.issueInitialNodeCertificate(ctx, nodeID, csr, now); err == nil {
		t.Fatal("pending restore allowed initial retry")
	}
	if _, err := service.renewNodeCertificate(ctx, old, newCSR, now); err == nil {
		t.Fatal("pending restore allowed renewal retry")
	}
	if _, err := service.rebindRevokedNodeCertificate(ctx, nodeID, 1, newCSR, now); err == nil {
		t.Fatal("pending restore allowed rebind")
	}
	if _, err := newFastControllerTestService(cfg).Init(); !errors.Is(err, storage.ErrRestorePending) {
		t.Fatalf("pending restore startup: %v", err)
	}
	if err := store.ClearRestorePending(); err != nil {
		t.Fatal(err)
	}
	if certificate, err := newFastControllerTestService(cfg).issueInitialNodeCertificate(ctx, nodeID, csr, now); len(certificate) != 0 ||
		(!errors.Is(err, sqldb.ErrNodeCertificateDenied) && !errors.Is(err, sqldb.ErrNodeCertificateConflict)) {
		t.Fatalf("restored initial retry escaped revocation: %v", err)
	}
	if _, err := newFastControllerTestService(cfg).renewNodeCertificate(ctx, old, newCSR, now); !errors.Is(err, sqldb.ErrNodeCertificateDenied) {
		t.Fatalf("restored renewal retry escaped revocation: %v", err)
	}
	restored, err := store.Load()
	if err != nil || !reflect.DeepEqual(state, restored) {
		t.Fatal("reconciliation changed state or control generation", err)
	}
}

func TestControllerRestoreReconciliationFailureMatrix(t *testing.T) {
	for _, point := range []string{"after-installed-validation", "after-migration", "after-reset-commit", "after-reopened-validation", "after-denial-verification"} {
		t.Run(point, func(t *testing.T) {
			ctx := context.Background()
			now := time.Now().UTC()
			cfg, service, control := serverRotationFixture(t, now, 8443)
			csr, _ := renewalTestCSR(t)
			issued, err := service.issueInitialNodeCertificate(ctx, "11111111-1111-4111-8111-111111111111", csr, now)
			if err != nil {
				t.Fatal(err)
			}
			cert := renewalTestCertificate(t, issued)
			store := storage.New(cfg.ConfigDir)
			state, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			if err := store.BeginRestorePending(state.Controller.ControllerID); err != nil {
				t.Fatal(err)
			}
			fault := errors.New("injected reconciliation failure")
			err = reconcileRestoredController(ctx, cfg, state, now, func(step string) error {
				if step == point {
					return fault
				}
				return nil
			})
			if !errors.Is(err, fault) || !errors.Is(store.CheckRestorePending(), storage.ErrRestorePending) {
				t.Fatalf("failed reconciliation did not retain gate: %v", err)
			}
			db, err := sqldb.Open(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			_, err = db.FindActiveNodeCertificate(ctx, state.Controller.ControllerID, control.CAGeneration, cert, now)
			beforeCommit := point == "after-installed-validation" || point == "after-migration"
			if beforeCommit && err != nil || !beforeCommit && !errors.Is(err, sqldb.ErrNodeCertificateDenied) {
				t.Fatalf("unexpected transaction boundary: %v", err)
			}
			if _, err := newFastControllerTestService(cfg).Init(); !errors.Is(err, storage.ErrRestorePending) {
				t.Fatalf("failed reconciliation admitted startup: %v", err)
			}
			if err := ReconcileRestoredController(ctx, cfg, state, now); err != nil {
				t.Fatalf("explicit same-attempt reconciliation retry: %v", err)
			}
			if err := db.VerifyControllerRestoreReset(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}
