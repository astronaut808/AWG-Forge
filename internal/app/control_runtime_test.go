package app

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlauth"
	"github.com/astronaut808/awg-forge/internal/controlserver"
	"github.com/astronaut808/awg-forge/internal/sqldb"
)

type controlLifecycleFixture struct {
	service *Service
	cfg     config.Config
	control config.ControlIdentityState
	token   string
	client  *http.Client
	tls     *tls.Config
	address string
	url     string
}

func newControlLifecycleFixture(t *testing.T) controlLifecycleFixture {
	t.Helper()
	cfg := controllerTestConfig(t)
	s := newFastControllerTestService(cfg)
	if _, err := s.Init(); err != nil {
		t.Fatal(err)
	}
	activation, err := s.ActivateController(context.Background(), controllerTestActivationRequest(t, time.Now().Add(-time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	issuedAt := time.Now().Add(-20 * 24 * time.Hour)
	control, err := s.PrepareControlIdentity(context.Background(), ControlIdentityRequest{BindIP: "127.0.0.1", Advertised: "127.0.0.1", Port: port, Now: issuedAt})
	if err != nil {
		t.Fatal(err)
	}
	csr, key := renewalTestCSR(t)
	pem, err := s.issueInitialNodeCertificate(context.Background(), "11111111-1111-4111-8111-111111111111", csr, issuedAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	node := renewalTestCertificate(t, pem)
	// Keep node renewal due while isolating server lifecycle tests.
	control, err = s.rotateControlServerLeaf(context.Background(), control.ServerGeneration, time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	material, err := s.store.LoadControlIdentity(control.CAGeneration, control.ServerGeneration)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(material.CACert) {
		t.Fatal("CA")
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: control.Advertised, Certificates: []tls.Certificate{{Certificate: [][]byte{node.Raw}, PrivateKey: key}}}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfig}, Timeout: 2 * time.Second}
	address := net.JoinHostPort(control.BindIP, strconv.Itoa(control.Port))
	f := controlLifecycleFixture{s, cfg, control, activation.Authentication.Token, client, tlsConfig, address, "https://" + address + "/control/v1/ping"}
	t.Cleanup(func() {
		client.CloseIdleConnections()
		if err := s.shutdownControlLoopback(); err != nil {
			t.Error(err)
		}
	})
	return f
}

func (f controlLifecycleFixture) receipt(t *testing.T) *ControlEnableReceipt {
	t.Helper()
	calls := 0
	r, err := f.service.PrepareControlEnable(context.Background(), f.token, func(_ context.Context, state config.State) error {
		calls++
		if state.Controller == nil || *state.Controller.Control != f.control {
			return errors.New("wrong backup identity")
		}
		return nil // The encryption/Verify adapter is exercised in internal/backup.
	})
	if err != nil || calls != 1 {
		t.Fatalf("backup admission: calls=%d error=%v", calls, err)
	}
	return r
}

func lifecycleRoutes(s *Service) []controlserver.Route {
	return []controlserver.Route{{ID: "node.ping", Method: http.MethodGet, Path: "/control/v1/ping", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state, err := s.store.Load()
		if err != nil || state.Controller == nil || !state.Controller.Control.Enabled {
			http.Error(w, "uncommitted", 500)
			return
		}
		if _, ok := controlserver.IdentityFromContext(r.Context()); !ok {
			http.Error(w, "unauthorized", 500)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})}}
}

func assertLoopbackFree(t *testing.T, address string) {
	t.Helper()
	l, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("loopback socket leaked: %v", err)
	}
	_ = l.Close()
}

func TestControlLoopbackCommitServeDisableRestartAndRotation(t *testing.T) {
	f := newControlLifecycleFixture(t)
	before, err := f.service.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	f.service.controlRuntimeStep = func(point string) error {
		if point != "after-bind" {
			return nil
		}
		// The port is held, but no TLS handshake can finish before state commit.
		conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 80 * time.Millisecond}, "tcp", f.address, f.tls)
		if err == nil {
			_ = conn.Close()
			return errors.New("TLS admitted before commit")
		}
		return nil
	}
	routes := lifecycleRoutes(f.service)
	receipt := f.receipt(t)
	if err := f.service.enableControlLoopback(context.Background(), f.token, receipt, routes); err != nil {
		t.Fatal(err)
	}
	waitServerRotationTLS(t, f.client, f.url)
	if err := f.service.enableControlLoopback(context.Background(), f.token, receipt, routes); err == nil {
		t.Fatal("receipt reused")
	}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", f.address, f.tls)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	rotated, err := f.service.rotateControlLoopbackLeaf(context.Background(), f.control.ServerGeneration, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !rotated.Enabled || rotated.ServerGeneration == f.control.ServerGeneration {
		t.Fatal("owner rotation did not preserve enablement")
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("predecessor connection remained open")
	}
	waitServerRotationTLS(t, f.client, f.url)
	if _, err := newFastControllerTestService(f.cfg).rotateControlServerLeaf(context.Background(), rotated.ServerGeneration, time.Now(), nil); err == nil {
		t.Fatal("rotation bypassed owner")
	}
	if err := f.service.shutdownControlLoopback(); err != nil {
		t.Fatal(err)
	}
	assertLoopbackFree(t, f.address)
	// Init alone does not auto-enable the internal listener.
	restarted := newFastControllerTestService(f.cfg)
	if _, err := restarted.Init(); err != nil {
		t.Fatal(err)
	}
	assertLoopbackFree(t, f.address)
	t.Cleanup(func() {
		if err := restarted.shutdownControlLoopback(); err != nil {
			t.Error(err)
		}
	})
	if err := restarted.restartControlLoopback(context.Background(), lifecycleRoutes(restarted)); err != nil {
		t.Fatal(err)
	}
	waitServerRotationTLS(t, f.client, f.url)
	if err := restarted.disableControlLoopback(context.Background(), f.token); err != nil {
		t.Fatal(err)
	}
	assertLoopbackFree(t, f.address)
	after, err := restarted.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if after.Controller.Control.Enabled {
		t.Fatal("disable not persisted")
	}
	after.Controller.Control.ServerGeneration = f.control.ServerGeneration
	if !reflect.DeepEqual(before, after) {
		t.Fatal("lifecycle changed configuration beyond enablement and server generation")
	}
	if err := restarted.restartControlLoopback(context.Background(), nil); err == nil {
		t.Fatal("disabled identity restarted")
	}
}

func TestControlLoopbackFailuresReleaseSocketAndConsumeReceipt(t *testing.T) {
	for _, point := range []string{"after-bind", "before-state-save", "save-before-rename", "save-after-rename", "after-state-save", "after-state-sync", "before-serve"} {
		t.Run(point, func(t *testing.T) {
			f := newControlLifecycleFixture(t)
			receipt := f.receipt(t)
			save := f.service.saveState
			f.service.saveState = func(state config.State) error {
				if point == "save-before-rename" {
					return errors.New("injected save")
				}
				if err := save(state); err != nil {
					return err
				}
				if point == "save-after-rename" {
					return errors.New("injected uncertain save")
				}
				return nil
			}
			f.service.controlRuntimeStep = func(p string) error {
				if p == point {
					return errors.New("injected transition")
				}
				return nil
			}
			err := f.service.enableControlLoopback(context.Background(), f.token, receipt, lifecycleRoutes(f.service))
			if point == "before-serve" {
				if err != nil {
					t.Fatal(err)
				}
				select {
				case <-f.service.controlOwner.done:
				case <-time.After(time.Second):
					t.Fatal("serve failure not observed")
				}
				f.service.controlOwner.resultMu.Lock()
				result := f.service.controlOwner.result
				f.service.controlOwner.resultMu.Unlock()
				if result == nil {
					t.Fatal("serve failure invisible")
				}
			} else if err == nil {
				t.Fatal("fault did not stop enable")
			}
			assertLoopbackFree(t, f.address)
			if err := f.service.enableControlLoopback(context.Background(), f.token, receipt, nil); err == nil {
				t.Fatal("failed enable receipt reused")
			}
			state, err := f.service.store.Load()
			if err != nil {
				t.Fatal(err)
			}
			wantEnabled := point == "save-after-rename" || point == "after-state-save" || point == "after-state-sync" || point == "before-serve"
			if state.Controller.Control.Enabled != wantEnabled {
				t.Fatal("uncertain commit rolled back or uncommitted state enabled")
			}
			f.service.saveState = save
			f.service.controlRuntimeStep = nil
			if _, err := newFastControllerTestService(f.cfg).Init(); err != nil {
				t.Fatal("optional control failure blocked local init", err)
			}
			if wantEnabled {
				if err := f.service.restartControlLoopback(context.Background(), lifecycleRoutes(f.service)); err != nil {
					t.Fatal(err)
				}
				waitServerRotationTLS(t, f.client, f.url)
			}
		})
	}
	t.Run("occupied port", func(t *testing.T) {
		f := newControlLifecycleFixture(t)
		receipt := f.receipt(t)
		l, err := net.Listen("tcp", f.address)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = l.Close() }()
		if err := f.service.enableControlLoopback(context.Background(), f.token, receipt, nil); err == nil {
			t.Fatal("bound occupied port")
		}
		state, err := f.service.store.Load()
		if err != nil || state.Controller.Control.Enabled {
			t.Fatal("bind failure enabled identity", err)
		}
	})
}

func TestControlEnableReceiptSessionGenerationAndProcessFences(t *testing.T) {
	for _, kind := range []string{"other service", "new receipt", "expired", "rotated generation", "revoked session", "different session", "failed backup", "cancelled backup"} {
		t.Run(kind, func(t *testing.T) {
			f := newControlLifecycleFixture(t)
			receipt := f.receipt(t)
			s := f.service
			token := f.token
			switch kind {
			case "other service":
				s = newFastControllerTestService(f.cfg)
			case "new receipt":
				_ = f.receipt(t)
			case "expired":
				s.controlEnable.expiresAt = time.Now().Add(-time.Second)
			case "rotated generation":
				if _, err := s.rotateControlServerLeaf(context.Background(), f.control.ServerGeneration, time.Now(), nil); err != nil {
					t.Fatal(err)
				}
			case "revoked session", "different session":
				db, err := sqldb.Open(context.Background(), f.cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = db.Close() }()
				keys, err := controlauth.LoadKeys(filepath.Join(f.cfg.ConfigDir, controlauth.KeyFileName))
				if err != nil {
					t.Fatal(err)
				}
				auth, err := controlauth.NewService(db, keys, fastControllerAuthOptions())
				if err != nil {
					t.Fatal(err)
				}
				if kind == "revoked session" {
					if err := auth.RevokeSession(context.Background(), token, time.Now()); err != nil {
						t.Fatal(err)
					}
				} else {
					now := time.Now()
					a, err := auth.Authenticate(context.Background(), "admin", controllerTestPassword, controllerTestTOTPCode(t, now), "127.0.0.1", now)
					if err != nil {
						t.Fatal(err)
					}
					token = a.Token
				}
			case "failed backup", "cancelled backup":
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				_, err := s.PrepareControlEnable(ctx, token, func(context.Context, config.State) error {
					if kind == "cancelled backup" {
						cancel()
						return nil
					}
					return errors.New("verify failure")
				})
				if err == nil {
					t.Fatal("invalid backup created receipt")
				}
			}
			if err := s.enableControlLoopback(context.Background(), token, receipt, nil); err == nil {
				t.Fatal("stale capability accepted")
			}
			assertLoopbackFree(t, f.address)
		})
	}
}

func TestControlLoopbackConcurrentEnableAndDisableCancelsPoll(t *testing.T) {
	f := newControlLifecycleFixture(t)
	receipt := f.receipt(t)
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	cleanup := make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(cleanup) }) })
	routes := []controlserver.Route{{ID: "node.poll", Method: "GET", Path: "/control/v1/ping", Handler: http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(cancelled)
		<-cleanup
	})}}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- f.service.enableControlLoopback(context.Background(), f.token, receipt, routes)
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("concurrent enable success=%d", success)
	}
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		response, err := f.client.Get(f.url)
		if err == nil {
			_ = response.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("poll did not enter")
	}
	disabled := make(chan error, 1)
	go func() { disabled <- f.service.disableControlLoopback(context.Background(), f.token) }()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("disable did not cancel poll")
	}
	select {
	case err := <-disabled:
		t.Fatalf("disable returned before handler cleanup: %v", err)
	case <-time.After(40 * time.Millisecond):
	}
	release.Do(func() { close(cleanup) })
	select {
	case err := <-disabled:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("disable did not finish after handler cleanup")
	}
	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("client remained connected")
	}
	assertLoopbackFree(t, f.address)
}

func TestControlLoopbackOwnsLifetimeBeyondOperationContext(t *testing.T) {
	f := newControlLifecycleFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	if err := f.service.enableControlLoopback(ctx, f.token, f.receipt(t), lifecycleRoutes(f.service)); err != nil {
		t.Fatal(err)
	}
	cancel()
	waitServerRotationTLS(t, f.client, f.url)
	if err := f.service.disableControlLoopback(context.Background(), "forged"); err == nil {
		t.Fatal("unauthorized disable")
	}
	waitServerRotationTLS(t, f.client, f.url)
	if err := f.service.shutdownControlLoopback(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	if err := f.service.restartControlLoopback(ctx, lifecycleRoutes(f.service)); err != nil {
		t.Fatal(err)
	}
	cancel()
	waitServerRotationTLS(t, f.client, f.url)
}

func TestControlDisableCommitFailureClosesAdmission(t *testing.T) {
	f := newControlLifecycleFixture(t)
	if err := f.service.enableControlLoopback(context.Background(), f.token, f.receipt(t), lifecycleRoutes(f.service)); err != nil {
		t.Fatal(err)
	}
	waitServerRotationTLS(t, f.client, f.url)
	save := f.service.saveState
	f.service.saveState = func(config.State) error { return errors.New("injected disable save failure") }
	if err := f.service.disableControlLoopback(context.Background(), f.token); err == nil {
		t.Fatal("disable commit failure ignored")
	}
	assertLoopbackFree(t, f.address)
	state, err := f.service.store.Load()
	if err != nil || !state.Controller.Control.Enabled {
		t.Fatal("failed disable rolled back authoritative state", err)
	}
	f.service.saveState = save
	if err := f.service.disableControlLoopback(context.Background(), f.token); err != nil {
		t.Fatal(err)
	}
	state, err = f.service.store.Load()
	if err != nil || state.Controller.Control.Enabled {
		t.Fatal("disable retry failed", err)
	}
}

func TestControlEnableRequiresRecentAuthBeforeAndAfterBackup(t *testing.T) {
	f := newControlLifecycleFixture(t)
	f.service.controllerAuthOptions.RecentAuthTTL = time.Nanosecond
	called := false
	if receipt, err := f.service.PrepareControlEnable(context.Background(), f.token, func(context.Context, config.State) error { called = true; return nil }); err == nil || receipt != nil || called {
		t.Fatal("backup ran without recent auth")
	}
	f.service.controllerAuthOptions.RecentAuthTTL = 0
	db, err := sqldb.Open(context.Background(), f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	keys, err := controlauth.LoadKeys(filepath.Join(f.cfg.ConfigDir, controlauth.KeyFileName))
	if err != nil {
		t.Fatal(err)
	}
	auth, err := controlauth.NewService(db, keys, fastControllerAuthOptions())
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := f.service.PrepareControlEnable(context.Background(), f.token, func(context.Context, config.State) error {
		return auth.RevokeSession(context.Background(), f.token, time.Now())
	})
	if err == nil || receipt != nil {
		t.Fatal("session revoked during backup still authorized enable")
	}
}

func TestControlLoopbackRestartRequiresExistingIdentityAndRegistry(t *testing.T) {
	for _, kind := range []string{"missing database", "unsafe database", "missing auth keys", "disabled admin", "missing CA key", "missing server key"} {
		t.Run(kind, func(t *testing.T) {
			f := newControlLifecycleFixture(t)
			if err := f.service.enableControlLoopback(context.Background(), f.token, f.receipt(t), lifecycleRoutes(f.service)); err != nil {
				t.Fatal(err)
			}
			waitServerRotationTLS(t, f.client, f.url)
			if err := f.service.shutdownControlLoopback(); err != nil {
				t.Fatal(err)
			}
			missing := ""
			switch kind {
			case "missing database":
				missing = f.cfg.DatabasePath
			case "unsafe database":
				if err := os.Chmod(f.cfg.DatabasePath, 0644); err != nil {
					t.Fatal(err)
				}
			case "missing auth keys":
				missing = filepath.Join(f.cfg.ConfigDir, controlauth.KeyFileName)
			case "missing CA key":
				missing = filepath.Join(f.cfg.ConfigDir, "control", "ca", f.control.CAGeneration, "key.pem")
			case "missing server key":
				missing = filepath.Join(f.cfg.ConfigDir, "control", "server", f.control.ServerGeneration, "key.pem")
			case "disabled admin":
				db, err := sqldb.Open(context.Background(), f.cfg)
				if err != nil {
					t.Fatal(err)
				}
				err = db.DisableControllerAuthAfterRestore(context.Background(), time.Now())
				_ = db.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			if missing != "" {
				if err := os.Remove(missing); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.service.restartControlLoopback(context.Background(), nil); err == nil {
				t.Fatal("unusable control state started listener")
			}
			assertLoopbackFree(t, f.address)
			if _, err := newFastControllerTestService(f.cfg).Init(); err != nil {
				t.Fatal("optional control failure blocked local initialization", err)
			}
			if missing != "" {
				if _, err := os.Lstat(missing); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("missing identity or registry regenerated", err)
				}
			}
		})
	}
}
