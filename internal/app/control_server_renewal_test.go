package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlpki"
	"github.com/astronaut808/awg-forge/internal/controlserver"
	"github.com/astronaut808/awg-forge/internal/observability"
	"github.com/astronaut808/awg-forge/internal/sqldb"
	"github.com/astronaut808/awg-forge/internal/storage"
)

func setRenewalFixtureValidity(t *testing.T, svc *Service, control config.ControlIdentityState, before, after time.Time, caEnd *time.Time) {
	t.Helper()
	material, err := svc.store.LoadControlIdentity(control.CAGeneration, control.ServerGeneration)
	if err != nil {
		t.Fatal(err)
	}
	parseCert := func(body []byte) *x509.Certificate {
		block, _ := pem.Decode(body)
		if block == nil {
			t.Fatal("fixture certificate")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		return cert
	}
	parseKey := func(body []byte) ed25519.PrivateKey {
		block, _ := pem.Decode(body)
		if block == nil {
			t.Fatal("fixture key")
		}
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		return key.(ed25519.PrivateKey)
	}
	ca, leaf := parseCert(material.CACert), parseCert(material.ServerCert)
	caKey, leafKey := parseKey(material.CAKey), parseKey(material.ServerKey)
	if caEnd != nil {
		ca.NotAfter = *caEnd
		der, err := x509.CreateCertificate(rand.Reader, ca, ca, caKey.Public(), caKey)
		if err != nil {
			t.Fatal(err)
		}
		material.CACert = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		if err := os.WriteFile(filepath.Join(svc.cfg.ConfigDir, "control", "ca", control.CAGeneration, controlpki.CACertFile), material.CACert, 0600); err != nil {
			t.Fatal(err)
		}
	}
	leaf.NotBefore, leaf.NotAfter = before, after
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, leafKey.Public(), caKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(svc.cfg.ConfigDir, "control", "server", control.ServerGeneration, controlpki.ServerCertFile), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestControlServerWorkerRenewsServingOwner(t *testing.T) {
	f := newControlLifecycleFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	setRenewalFixtureValidity(t, f.service, f.control, now.Add(-3*time.Second), now.Add(12*time.Second), nil)
	ctx, cancel := context.WithCancel(context.Background())
	if err := f.service.enableControlLoopback(ctx, f.token, f.receipt(t), lifecycleRoutes(f.service)); err != nil {
		t.Fatal(err)
	}
	cancel() // A successful request never owns the listener/worker lifetime.
	waitServerRotationTLS(t, f.client, f.url)
	oldConnection, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", f.address, f.tls)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = oldConnection.Close() }()
	before, err := f.service.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	oldMaterial, err := f.service.store.LoadControlIdentity(f.control.CAGeneration, f.control.ServerGeneration)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	var after config.State
	for {
		after, err = f.service.store.Load()
		if err != nil {
			t.Fatal(err)
		}
		if after.Controller.Control.ServerGeneration != f.control.ServerGeneration {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("serving worker did not renew")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Wait for durable cleanup/publication, rather than racing state rename.
	f.service.mu.Lock()
	owner := f.service.controlOwner
	f.service.mu.Unlock()
	waitServerRotationTLS(t, f.client, f.url)
	if err := f.service.store.CheckNoControlServerRotation(); err != nil {
		t.Fatal(err)
	}
	_ = oldConnection.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := oldConnection.Read(make([]byte, 1)); err == nil {
		t.Fatal("predecessor connection survived")
	} else if e := new(net.Error); errors.As(err, e) && (*e).Timeout() {
		t.Fatal("old connection was not closed")
	}
	generation := after.Controller.Control.ServerGeneration
	after.Controller.Control.ServerGeneration = before.Controller.Control.ServerGeneration
	if !reflect.DeepEqual(before, after) {
		t.Fatal("renewal changed state beyond server generation")
	}
	current, err := f.service.store.LoadControlIdentity(f.control.CAGeneration, generation)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current.CACert, oldMaterial.CACert) || !bytes.Equal(current.CAKey, oldMaterial.CAKey) || bytes.Equal(current.ServerKey, oldMaterial.ServerKey) {
		t.Fatal("renewal did not preserve CA/fresh leaf")
	}
	// StartControl is idempotent for an active owner.
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := f.service.StartControl(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	f.service.mu.Lock()
	if f.service.controlOwner != owner {
		t.Error("duplicate runtime owner")
	}
	f.service.mu.Unlock()
	db, err := sqldb.Open(context.Background(), f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	err = db.RevokeNodeBinding(context.Background(), sqldb.NodeIdentity{ControllerID: before.Controller.ControllerID, NodeID: "11111111-1111-4111-8111-111111111111", BindingEpoch: 1}, time.Now())
	_ = db.Close()
	if err != nil {
		t.Fatal(err)
	}
	response, err := f.client.Get(f.url)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatal("server renewal bypassed revocation")
	}
	if err := f.service.ShutdownControl(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-owner.done:
	default:
		t.Fatal("worker not drained")
	}
	assertLoopbackFree(t, f.address)
}

func TestControlServerStartupCatchupAndPrerequisites(t *testing.T) {
	for _, kind := range []string{"due", "expired", "disabled", "expired CA", "future leaf", "corrupt leaf", "missing leaf", "missing auth", "missing registry", "restore", "desired fence", "malformed journal"} {
		t.Run(kind, func(t *testing.T) {
			f := newControlLifecycleFixture(t)
			now := time.Now().UTC()
			before, after := now.Add(-31*24*time.Hour), now.Add(-time.Hour)
			if kind == "due" {
				before, after = now.Add(-21*24*time.Hour), now.Add(9*24*time.Hour)
			}
			var caEnd *time.Time
			if kind == "expired CA" {
				end := now.Add(-time.Minute)
				caEnd = &end
			}
			if kind == "future leaf" {
				before, after = now.Add(time.Hour), now.Add(24*time.Hour)
			}
			setRenewalFixtureValidity(t, f.service, f.control, before, after, caEnd)
			state, err := f.service.store.Load()
			if err != nil {
				t.Fatal(err)
			}
			state.Controller.Control.Enabled = kind != "disabled"
			if err := f.service.store.Save(state); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "corrupt leaf":
				if err := os.WriteFile(filepath.Join(f.cfg.ConfigDir, "control", "server", f.control.ServerGeneration, controlpki.ServerCertFile), []byte("broken"), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing leaf":
				if err := os.Remove(filepath.Join(f.cfg.ConfigDir, "control", "server", f.control.ServerGeneration, controlpki.ServerKeyFile)); err != nil {
					t.Fatal(err)
				}
			case "missing auth":
				if err := os.Remove(filepath.Join(f.cfg.ConfigDir, "controller-auth.keys")); err != nil {
					t.Fatal(err)
				}
			case "missing registry":
				if err := os.Remove(f.cfg.DatabasePath); err != nil {
					t.Fatal(err)
				}
			case "restore":
				if err := f.service.store.BeginRestorePending(state.Controller.ControllerID); err != nil {
					t.Fatal(err)
				}
			case "desired fence":
				if err := f.service.store.SavePendingDesiredStateCommit(storage.PendingDesiredStateCommit{OperationID: "pending"}); err != nil {
					t.Fatal(err)
				}
			case "malformed journal":
				if err := os.WriteFile(f.service.store.ControlServerRotationJournalPath(), []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err = f.service.StartControl(context.Background())
			success := kind == "due" || kind == "expired"
			if success && err != nil {
				t.Fatal(err)
			}
			if !success && kind != "disabled" && err == nil {
				t.Fatal("invalid prerequisite admitted")
			}
			committed, err := f.service.store.Load()
			if err != nil {
				t.Fatal(err)
			}
			if (committed.Controller.Control.ServerGeneration != f.control.ServerGeneration) != success {
				t.Fatal("wrong startup mutation")
			}
			if success {
				conn, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", f.address, f.tls)
				if err != nil {
					t.Fatal(err)
				}
				_ = conn.Close()
				committed.Controller.Control.ServerGeneration = state.Controller.Control.ServerGeneration
				if !reflect.DeepEqual(state, committed) {
					t.Fatal("startup changed authority")
				}
			} else {
				assertLoopbackFree(t, f.address)
			}
		})
	}
}

func TestControlServerWorkerCancellationAndStaleOwner(t *testing.T) {
	for _, kind := range []string{"mutex", "flock"} {
		t.Run(kind, func(t *testing.T) {
			f := newControlLifecycleFixture(t)
			reached := make(chan struct{})
			release := make(chan struct{})
			f.service.controlRenewalWait = func(ctx context.Context, _ time.Duration) bool {
				close(reached)
				select {
				case <-ctx.Done():
					return false
				case <-release:
					return true
				}
			}
			if err := f.service.enableControlLoopback(context.Background(), f.token, f.receipt(t), lifecycleRoutes(f.service)); err != nil {
				t.Fatal(err)
			}
			<-reached
			f.service.mu.Lock()
			owner := f.service.controlOwner
			f.service.mu.Unlock()
			var lock *storage.StateLock
			var err error
			if kind == "mutex" {
				f.service.mu.Lock()
			} else {
				lock, err = storage.AcquireStateMutationLock(f.cfg.ConfigDir)
				if err != nil {
					t.Fatal(err)
				}
			}
			close(release)
			// Give the scheduler the contention; its cancellation cannot require either lock.
			time.Sleep(30 * time.Millisecond)
			shutdown := make(chan error, 1)
			go func() { shutdown <- f.service.ShutdownControl() }()
			select {
			case <-owner.done:
			case <-time.After(2 * time.Second):
				t.Fatal("worker deadlocked at shutdown")
			}
			if kind == "mutex" {
				f.service.mu.Unlock()
			} else {
				_ = lock.Close()
			}
			select {
			case err := <-shutdown:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("public shutdown blocked")
			}
			f.service.controlRenewalWait = nil
			if err := f.service.StartControl(context.Background()); err != nil {
				t.Fatal(err)
			}
			f.service.mu.Lock()
			successor := f.service.controlOwner
			f.service.mu.Unlock()
			_, terminal, err := f.service.controlServerRenewalAttempt(context.Background(), owner)
			if !terminal || err == nil {
				t.Fatal("stale worker accepted")
			}
			if successor == owner {
				t.Fatal("no successor owner")
			}
			waitServerRotationTLS(t, f.client, f.url)
		})
	}
}

func TestControlServerRenewalTimingAndCACap(t *testing.T) {
	f := newControlLifecycleFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	end := now.Add(90 * time.Second)
	setRenewalFixtureValidity(t, f.service, f.control, now.Add(-5*time.Minute), end, &end)
	material, err := f.service.store.LoadControlIdentity(f.control.CAGeneration, f.control.ServerGeneration)
	if err != nil {
		t.Fatal(err)
	}
	due, expiry, caExpiry, err := controlServerRenewalTiming(material)
	if err != nil {
		t.Fatal(err)
	}
	if !due.Equal(now.Add(-40*time.Second)) || !expiry.Equal(caExpiry) || controlRenewalCanProgress(now, expiry, caExpiry) {
		t.Fatal("CA cap/backdating scheduling")
	}
	// Clock jumps remain bounded and capped leaf produces no successors.
	var clock atomic.Int64
	clock.Store(now.UnixNano())
	f.service.controlRenewalNow = func() time.Time { return time.Unix(0, clock.Load()) }
	if err := f.service.enableControlLoopback(context.Background(), f.token, f.receipt(t), lifecycleRoutes(f.service)); err != nil {
		t.Fatal(err)
	}
	f.service.mu.Lock()
	owner := f.service.controlOwner
	f.service.mu.Unlock()
	for range 3 {
		delay, terminal, err := f.service.controlServerRenewalAttempt(context.Background(), owner)
		if terminal || !errors.Is(err, errControlCAMaintenance) || delay != controlRenewalPoll {
			t.Fatal("CA cap storm", err)
		}
	}
	state, err := f.service.store.Load()
	if err != nil || state.Controller.Control.ServerGeneration != f.control.ServerGeneration {
		t.Fatal("CA cap mutated generation")
	}
	clock.Store(now.Add(-time.Second).UnixNano())
	delay, terminal, _ := f.service.controlServerRenewalAttempt(context.Background(), owner)
	if terminal || delay <= 0 {
		t.Fatal("wall clock jump not bounded")
	}
}

func TestControlServerWorkerRecoversPrecommitRetry(t *testing.T) {
	f := newControlLifecycleFixture(t)
	now := time.Now()
	setRenewalFixtureValidity(t, f.service, f.control, now.Add(-20*24*time.Hour), now.Add(9*24*time.Hour), nil)
	var failures atomic.Int32
	f.service.controlRotationStep = func(point string) error {
		if point == "after-publication:journal" && failures.Add(1) == 1 {
			return errors.New("precommit publication sync fault")
		}
		return nil
	}
	retries := make(chan time.Duration, 2)
	f.service.controlRenewalWait = func(ctx context.Context, delay time.Duration) bool {
		retries <- delay
		if delay < controlRenewalPoll {
			return true
		}
		<-ctx.Done()
		return false
	}
	if err := f.service.enableControlLoopback(context.Background(), f.token, f.receipt(t), lifecycleRoutes(f.service)); err != nil {
		t.Fatal(err)
	}
	select {
	case delay := <-retries:
		if delay < time.Second || delay > 1250*time.Millisecond {
			t.Fatal("unbounded retry interval")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no precommit retry")
	}
	select {
	case <-retries:
	case <-time.After(3 * time.Second):
		t.Fatal("recovery did not retry")
	}
	state, err := f.service.store.Load()
	if err != nil || state.Controller.Control.ServerGeneration == f.control.ServerGeneration {
		t.Fatal("retry did not commit")
	}
	if err := f.service.store.CheckNoControlServerRotation(); err != nil {
		t.Fatal(err)
	}
	waitServerRotationTLS(t, f.client, f.url)
}

func TestControlServerWorkerUncertainCommitClosesOwner(t *testing.T) {
	f := newControlLifecycleFixture(t)
	now := time.Now()
	setRenewalFixtureValidity(t, f.service, f.control, now.Add(-20*24*time.Hour), now.Add(9*24*time.Hour), nil)
	save := f.service.saveState
	f.service.saveState = func(state config.State) error {
		if err := save(state); err != nil {
			return err
		}
		if state.Controller.Control.ServerGeneration != f.control.ServerGeneration {
			return errors.New("state rename uncertain")
		}
		return nil
	}
	if err := f.service.enableControlLoopback(context.Background(), f.token, f.receipt(t), lifecycleRoutes(f.service)); err != nil {
		t.Fatal(err)
	}
	f.service.mu.Lock()
	owner := f.service.controlOwner
	f.service.mu.Unlock()
	select {
	case <-owner.done:
	case <-time.After(3 * time.Second):
		t.Fatal("uncertain committed worker kept owner alive")
	}
	assertLoopbackFree(t, f.address)
	committed, err := f.service.store.Load()
	if err != nil || committed.Controller.Control.ServerGeneration == f.control.ServerGeneration {
		t.Fatal("uncertain state rolled back")
	}
	f.service.saveState = save
	if err := f.service.StartControl(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, err := f.service.store.Load()
	if err != nil || after.Controller.Control.ServerGeneration != committed.Controller.Control.ServerGeneration {
		t.Fatal("restart duplicated successor")
	}
	waitServerRotationTLS(t, f.client, f.url)
}

func TestControlServerRotationCaptureIsCancellable(t *testing.T) {
	f := newControlLifecycleFixture(t)
	if err := f.service.enableControlLoopback(context.Background(), f.token, f.receipt(t), lifecycleRoutes(f.service)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.service.mu.Lock()
	defer f.service.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		_, err := f.service.rotateControlLoopbackLeaf(ctx, f.control.ServerGeneration, time.Now())
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("rotation capture ignored cancellation")
	}
}

func TestControlServerWorkerTerminalFencesSameOwnerRotation(t *testing.T) {
	f := newControlLifecycleFixture(t)
	start := make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(start) }) })
	f.service.controlRuntimeStep = func(point string) error {
		if point == "before-serve" {
			<-start
		}
		return nil
	}
	future := time.Now().Add(31 * 24 * time.Hour)
	f.service.controlRenewalNow = func() time.Time { return future }
	if err := f.service.enableControlLoopback(context.Background(), f.token, f.receipt(t), lifecycleRoutes(f.service)); err != nil {
		t.Fatal(err)
	}
	owner := f.service.controlLifetime.Load()
	_, terminal, err := f.service.controlServerRenewalAttempt(context.Background(), owner)
	if !terminal || err == nil || !owner.runtime.Closed() {
		t.Fatal("terminal decision released its fence before admission closed")
	}
	// The worker's terminal return used to leave a window for this same-owner
	// rotation to publish a successor that the worker subsequently closed.
	if _, err := f.service.rotateControlLoopbackLeaf(context.Background(), f.control.ServerGeneration, future); err == nil {
		t.Fatal("closed owner committed a successor")
	}
	state, err := f.service.store.Load()
	if err != nil || state.Controller.Control.ServerGeneration != f.control.ServerGeneration {
		t.Fatal("terminal race changed the committed generation")
	}
	release.Do(func() { close(start) })
}

func TestControlServerWorkerTerminalDiagnostic(t *testing.T) {
	f := newControlLifecycleFixture(t)
	start := make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(start) }) })
	f.service.controlRuntimeStep = func(point string) error {
		if point == "before-serve" {
			<-start
		}
		return nil
	}
	var output bytes.Buffer
	f.service.runtime = observability.NewWithWriter("debug", &output)
	if err := f.service.enableControlLoopback(context.Background(), f.token, f.receipt(t), lifecycleRoutes(f.service)); err != nil {
		t.Fatal(err)
	}
	owner := f.service.controlLifetime.Load()
	const sensitiveEvidence = "malformed-secret-evidence"
	if err := os.WriteFile(filepath.Join(f.cfg.ConfigDir, storage.ControlServerRotationJournalFileName), []byte(sensitiveEvidence), 0600); err != nil {
		t.Fatal(err)
	}
	release.Do(func() { close(start) })
	select {
	case <-owner.done:
	case <-time.After(3 * time.Second):
		t.Fatal("terminal worker did not stop")
	}
	if !bytes.Contains(output.Bytes(), []byte("control.server.renewal_stopped")) || bytes.Contains(output.Bytes(), []byte(sensitiveEvidence)) {
		t.Fatal("missing or unsafe terminal diagnostic")
	}
	if err := os.Remove(filepath.Join(f.cfg.ConfigDir, storage.ControlServerRotationJournalFileName)); err != nil {
		t.Fatal(err)
	}
}

func TestControlServerOwnerDrainTimeoutPreservesRegistryUntilHandlersFinish(t *testing.T) {
	f := newControlLifecycleFixture(t)
	now := time.Now()
	setRenewalFixtureValidity(t, f.service, f.control, now.Add(-time.Second), now.Add(4*time.Second), nil)
	// Exercise real transport expiry/drain without the renewal worker extending it.
	f.service.controlRenewalWait = func(ctx context.Context, _ time.Duration) bool { <-ctx.Done(); return false }
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	routes := []controlserver.Route{{ID: "node.hold", Method: http.MethodGet, Path: "/control/v1/ping", Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release // A handler that fails to obey request cancellation.
		w.WriteHeader(http.StatusNoContent)
	})}}
	if err := f.service.enableControlLoopback(context.Background(), f.token, f.receipt(t), routes); err != nil {
		t.Fatal(err)
	}
	owner := f.service.controlLifetime.Load()
	go func() {
		response, err := f.client.Get(f.url)
		if err == nil {
			_ = response.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not enter")
	}
	stopped := make(chan error, 1)
	go func() { stopped <- f.service.ShutdownControl() }()
	select {
	case err := <-stopped:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("incomplete drain reported success: %v", err)
		}
	case <-time.After(14 * time.Second):
		t.Fatal("owner shutdown was unbounded")
	}
	select {
	case <-owner.done:
		t.Fatal("owner completed while a registry user remained")
	default:
	}
	if initialized, err := owner.db.ControllerAuthInitialized(context.Background()); err != nil || !initialized {
		t.Fatal("registry closed while a handler remained")
	}
	if err := f.service.StartControl(context.Background()); err == nil {
		t.Fatal("incomplete owner drain reported a successful restart")
	}
	once.Do(func() { close(release) })
	select {
	case <-owner.done:
	case <-time.After(2 * time.Second):
		t.Fatal("owner did not release completed handlers")
	}
	if _, err := owner.db.ControllerAuthInitialized(context.Background()); err == nil {
		t.Fatal("completed owner retained registry")
	}
	if err := f.service.ShutdownControl(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("owner lost transport drain timeout")
	}
}
