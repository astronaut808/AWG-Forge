package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlpki"
	"github.com/astronaut808/awg-forge/internal/controlserver"
	"github.com/astronaut808/awg-forge/internal/sqldb"
	"github.com/astronaut808/awg-forge/internal/storage"
)

func serverRotationFixture(t *testing.T, issuedAt time.Time, port int) (config.Config, *Service, config.ControlIdentityState) {
	t.Helper()
	cfg := controllerTestConfig(t)
	svc := newFastControllerTestService(cfg)
	if _, err := svc.Init(); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ActivateController(context.Background(), controllerTestActivationRequest(t, issuedAt)); err != nil {
		t.Fatal(err)
	}
	control, err := svc.PrepareControlIdentity(context.Background(), ControlIdentityRequest{BindIP: "127.0.0.1", Advertised: "127.0.0.1", Port: port, Now: issuedAt})
	if err != nil {
		t.Fatal(err)
	}
	return cfg, svc, control
}

func TestInternalServerRotationOnlyChangesActiveGeneration(t *testing.T) {
	now := time.Now().UTC()
	cfg, svc, old := serverRotationFixture(t, now.Add(-31*24*time.Hour), 8443)
	store := storage.New(cfg.ConfigDir)
	before, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	beforeBytes, err := os.ReadFile(store.StatePath())
	if err != nil {
		t.Fatal(err)
	}
	material, err := store.LoadControlIdentity(old.CAGeneration, old.ServerGeneration)
	if err != nil {
		t.Fatal(err)
	}
	database, err := os.ReadFile(cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newFastControllerTestService(cfg).Init(); err != nil {
		t.Fatal(err)
	}
	afterInit, err := os.ReadFile(store.StatePath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeBytes, afterInit) {
		t.Fatal("startup mutated expired control identity")
	}
	rotated, err := svc.rotateControlServerLeaf(context.Background(), old.ServerGeneration, now, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.ServerGeneration == old.ServerGeneration || rotated.Enabled {
		t.Fatal("rotation did not switch a disabled generation")
	}
	after, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	after.Controller.Control.ServerGeneration = old.ServerGeneration
	if !reflect.DeepEqual(before, after) {
		t.Fatal("rotation changed state beyond server generation")
	}
	databaseAfter, err := os.ReadFile(cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(database) != sha256.Sum256(databaseAfter) {
		t.Fatal("rotation modified SQLite")
	}
	active, err := store.LoadControlIdentity(rotated.CAGeneration, rotated.ServerGeneration)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(active.CAKey, material.CAKey) || !bytes.Equal(active.CACert, material.CACert) {
		t.Fatal("CA changed")
	}
	if err := controlpki.Validate(active, controlpki.Endpoint{BindIP: rotated.BindIP, Advertised: rotated.Advertised, Port: rotated.Port}, rotated.CAPin, now, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(cfg.ConfigDir, "control", "server", old.ServerGeneration)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("old server generation was not retired")
	}
	if err := store.CheckNoControlServerRotation(); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.rotateControlServerLeaf(context.Background(), old.ServerGeneration, now, nil); !errors.Is(err, errStaleControlServerGeneration) {
		t.Fatalf("stale retry: %v", err)
	}
}

func TestInternalServerRotationSerializesTwoServices(t *testing.T) {
	now := time.Now().UTC()
	cfg, _, old := serverRotationFixture(t, now, 8443)
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := newFastControllerTestService(cfg).rotateControlServerLeaf(context.Background(), old.ServerGeneration, now, nil)
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	success, stale := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, errStaleControlServerGeneration) {
			stale++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || stale != 1 {
		t.Fatalf("success=%d stale=%d", success, stale)
	}
	entries, err := os.ReadDir(filepath.Join(cfg.ConfigDir, "control", "server"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatal("multiple successors remain")
	}
}

func TestInternalServerRotationCrashMatrix(t *testing.T) {
	pre := []string{"after-create:journal", "after-partial-write:journal", "before-close:journal", "before-publication:journal", "after-publication:journal", "before-directory-sync:journal", "before-write:journal", "after-write:journal", "before-sync:journal", "after-sync:journal", "after-directory-sync:journal", "before-generation", "after-generation", "after-generation-sync", "before-write:key.pem", "after-write:key.pem", "before-sync:key.pem", "after-sync:key.pem", "after-directory-sync:key.pem", "before-write:cert.pem", "after-write:cert.pem", "before-sync:cert.pem", "after-sync:cert.pem", "after-directory-sync:cert.pem", "before-staged-validation", "after-staged-validation", "before-state-save"}
	post := []string{"after-state-save", "before-state-sync", "after-state-sync", "before-committed-validation", "after-committed-validation", "before-retire:key.pem", "after-retire:key.pem", "before-retire:cert.pem", "after-retire:cert.pem", "before-retire-directory", "after-retire-directory", "after-retire-sync", "before-journal-delete", "after-journal-delete", "after-journal-delete-sync"}
	for _, point := range append(pre, post...) {
		t.Run(point, func(t *testing.T) {
			now := time.Now().UTC()
			cfg, svc, old := serverRotationFixture(t, now, 8443)
			store := storage.New(cfg.ConfigDir)
			hit := false
			svc.controlRotationStep = func(step string) error {
				if step == point {
					hit = true
					panic("simulated process loss")
				}
				return nil
			}
			func() {
				defer func() {
					if p := recover(); p != nil && p != "simulated process loss" {
						panic(p)
					}
				}()
				_, _ = svc.rotateControlServerLeaf(context.Background(), old.ServerGeneration, now, nil)
			}()
			if !hit {
				t.Fatal("fault point not reached")
			}
			state, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			committed := false
			for _, p := range post {
				if point == p {
					committed = true
				}
			}
			selected := state.Controller.Control.ServerGeneration
			if (selected != old.ServerGeneration) != committed {
				t.Fatal("wrong committed generation at crash")
			}
			if _, err := newFastControllerTestService(cfg).Init(); err != nil {
				t.Fatal(err)
			}
			recovered, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			if recovered.Controller.Control.ServerGeneration != selected {
				t.Fatal("recovery rolled committed state back")
			}
			if err := store.CheckNoControlServerRotation(); err != nil {
				t.Fatal(err)
			}
			if err := svc.validateControlIdentityLocked(recovered.Controller.Control, now, false); err != nil {
				t.Fatal(err)
			}
			paths, _ := storage.ControlIdentityRelativePaths(old.CAGeneration, selected)
			for _, p := range paths {
				assertControlMode(t, filepath.Join(cfg.ConfigDir, p), 0600)
			}
			assertControlMode(t, filepath.Join(cfg.ConfigDir, "control", "server", selected), 0700)
			entries, err := os.ReadDir(filepath.Join(cfg.ConfigDir, "control", "server"))
			if err != nil || len(entries) != 1 {
				t.Fatal("recovery left an owned orphan", err)
			}
			svc.controlRotationStep = nil
			_, err = svc.rotateControlServerLeaf(context.Background(), old.ServerGeneration, now, nil)
			if committed {
				if !errors.Is(err, errStaleControlServerGeneration) {
					t.Fatalf("uncertain commit retry: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestInternalServerRotationStateSaveUncertainty(t *testing.T) {
	for _, renamed := range []bool{false, true} {
		t.Run(strconv.FormatBool(renamed), func(t *testing.T) {
			now := time.Now().UTC()
			cfg, svc, old := serverRotationFixture(t, now, 8443)
			store := storage.New(cfg.ConfigDir)
			svc.saveState = func(state config.State) error {
				if renamed {
					if err := store.Save(state); err != nil {
						return err
					}
				}
				return errors.New("injected state save failure")
			}
			if _, err := svc.rotateControlServerLeaf(context.Background(), old.ServerGeneration, now, nil); err == nil {
				t.Fatal("save error ignored")
			}
			state, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			if (state.Controller.Control.ServerGeneration != old.ServerGeneration) != renamed {
				t.Fatal("unexpected state after save error")
			}
			if _, err := newFastControllerTestService(cfg).Init(); err != nil {
				t.Fatal(err)
			}
			if err := store.CheckNoControlServerRotation(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestInternalServerRotationInvalidMaterialNeverRegenerates(t *testing.T) {
	for _, kind := range []string{"missing-ca-key", "corrupt-ca-cert", "missing-leaf", "wrong-server-key", "wrong-pin", "expired-ca", "staged-corrupt"} {
		t.Run(kind, func(t *testing.T) {
			now := time.Now().UTC()
			cfg, svc, old := serverRotationFixture(t, now, 8443)
			store := storage.New(cfg.ConfigDir)
			at := now
			switch kind {
			case "missing-ca-key":
				if err := os.Remove(filepath.Join(cfg.ConfigDir, "control", "ca", old.CAGeneration, "key.pem")); err != nil {
					t.Fatal(err)
				}
			case "corrupt-ca-cert":
				if err := os.WriteFile(filepath.Join(cfg.ConfigDir, "control", "ca", old.CAGeneration, "cert.pem"), []byte("broken"), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing-leaf":
				if err := os.Remove(filepath.Join(cfg.ConfigDir, "control", "server", old.ServerGeneration, "cert.pem")); err != nil {
					t.Fatal(err)
				}
			case "wrong-server-key":
				m, _, err := controlpki.Generate(controlpki.Endpoint{BindIP: old.BindIP, Advertised: old.Advertised, Port: old.Port}, now)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(cfg.ConfigDir, "control", "server", old.ServerGeneration, "key.pem"), m.ServerKey, 0600); err != nil {
					t.Fatal(err)
				}
			case "wrong-pin":
				state, err := store.Load()
				if err != nil {
					t.Fatal(err)
				}
				state.Controller.Control.CAPin = "sha256:" + strings.Repeat("a", 64)
				if err := store.Save(state); err != nil {
					t.Fatal(err)
				}
			case "expired-ca":
				at = now.AddDate(6, 0, 0)
			case "staged-corrupt":
				svc.controlRotationStep = func(step string) error {
					if step == "before-staged-validation" {
						j, err := store.LoadControlServerRotationJournal()
						if err != nil {
							return err
						}
						return os.WriteFile(filepath.Join(cfg.ConfigDir, "control", "server", j.NewServerGeneration, "cert.pem"), []byte("broken"), 0600)
					}
					return nil
				}
			}
			if _, err := svc.rotateControlServerLeaf(context.Background(), old.ServerGeneration, at, nil); err == nil {
				t.Fatal("unsafe material rotated")
			}
			state, err := store.Load()
			if err != nil || state.Controller.Control.ServerGeneration != old.ServerGeneration {
				t.Fatal("unsafe rotation changed state", err)
			}
			if _, err := newFastControllerTestService(cfg).Init(); err != nil {
				t.Fatal("optional control failure blocked local initialization", err)
			}
			state, err = store.Load()
			if err != nil || state.Controller.Control.ServerGeneration != old.ServerGeneration {
				t.Fatal("startup regenerated identity", err)
			}
		})
	}
}

func TestInternalServerRotationFencesDirectPrivateCalls(t *testing.T) {
	for _, kind := range []string{"rotation", "malformed-rotation", "restore", "desired", "preparation", "activation"} {
		t.Run(kind, func(t *testing.T) {
			now := time.Now().UTC()
			issuedAt := now.Add(-20 * 24 * time.Hour)
			cfg, svc, old := serverRotationFixture(t, issuedAt, 8443)
			store := storage.New(cfg.ConfigDir)
			const renewID = "11111111-1111-4111-8111-111111111111"
			const rebindID = "22222222-2222-4222-8222-222222222222"
			initialCSR, _ := renewalTestCSR(t)
			predecessorPEM, err := svc.issueInitialNodeCertificate(context.Background(), renewID, initialCSR, issuedAt.Add(time.Second))
			if err != nil {
				t.Fatal(err)
			}
			predecessor := renewalTestCertificate(t, predecessorPEM)
			rebindInitial, _ := renewalTestCSR(t)
			if _, err := svc.issueInitialNodeCertificate(context.Background(), rebindID, rebindInitial, issuedAt.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			db, err := sqldb.Open(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.RevokeNodeBinding(context.Background(), sqldb.NodeIdentity{ControllerID: controllerIDFromStore(t, cfg.ConfigDir), NodeID: rebindID, BindingEpoch: 1}, now); err != nil {
				_ = db.Close()
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			registryBefore, err := os.ReadFile(cfg.DatabasePath)
			if err != nil {
				t.Fatal(err)
			}

			switch kind {
			case "rotation":
				j := storage.ControlServerRotationJournal{Version: 1, ControllerID: controllerIDFromStore(t, cfg.ConfigDir), CAGeneration: old.CAGeneration, OldServerGeneration: old.ServerGeneration, NewServerGeneration: strings.Repeat("c", 32), StartedAt: now}
				if err := store.SaveControlServerRotationJournal(j, nil); err != nil {
					t.Fatal(err)
				}
				dir := filepath.Join(cfg.ConfigDir, "control", "server", j.NewServerGeneration)
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "unexpected"), []byte("preserve"), 0600); err != nil {
					t.Fatal(err)
				}
			case "malformed-rotation":
				if err := os.WriteFile(store.ControlServerRotationJournalPath(), []byte("broken"), 0600); err != nil {
					t.Fatal(err)
				}
			case "restore":
				if err := store.BeginRestorePending(controllerIDFromStore(t, cfg.ConfigDir)); err != nil {
					t.Fatal(err)
				}
			case "desired":
				if err := store.SavePendingDesiredStateCommit(storage.PendingDesiredStateCommit{OperationID: "pending"}); err != nil {
					t.Fatal(err)
				}
			case "preparation":
				if err := os.WriteFile(store.ControlIdentityJournalPath(), []byte("broken"), 0600); err != nil {
					t.Fatal(err)
				}
			case "activation":
				if err := os.WriteFile(store.ControllerActivationJournalPath(), []byte("broken"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			csr, _ := renewalTestCSR(t)
			for index, call := range []func() error{
				func() error {
					_, err := newFastControllerTestService(cfg).issueInitialNodeCertificate(context.Background(), "33333333-3333-4333-8333-333333333333", csr, now)
					return err
				},
				func() error {
					_, err := newFastControllerTestService(cfg).renewNodeCertificate(context.Background(), predecessor, csr, now)
					return err
				},
				func() error {
					_, err := newFastControllerTestService(cfg).rebindRevokedNodeCertificate(context.Background(), rebindID, 1, csr, now)
					return err
				},
				func() error {
					_, err := svc.PrepareControlIdentity(context.Background(), ControlIdentityRequest{BindIP: old.BindIP, Advertised: old.Advertised, Port: old.Port})
					return err
				},
				func() error {
					_, err := svc.rotateControlServerLeaf(context.Background(), old.ServerGeneration, now, nil)
					return err
				},
			} {
				if err := call(); err == nil {
					t.Fatal("private call bypassed crash gate")
				} else if index < 3 {
					fence := map[string]string{"rotation": "control server rotation", "malformed-rotation": "control server rotation", "restore": "controller restore", "desired": "desired-state commit", "preparation": "control identity transition", "activation": "controller activation"}[kind]
					if !strings.Contains(err.Error(), fence) {
						t.Fatalf("private call denied for an unrelated reason: %v", err)
					}
				}
			}

			registryAfter, err := os.ReadFile(cfg.DatabasePath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(registryBefore, registryAfter) {
				t.Fatal("private call modified the registry through a crash gate")
			}
			state, err := store.Load()
			if err != nil || *state.Controller.Control != old {
				t.Fatal("crash gate changed state", err)
			}
		})
	}
}

func TestInternalServerRotationJointRuntimeCommitFailureAndRestart(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(strconv.FormatBool(failure), func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := listener.Addr().(*net.TCPAddr).Port
			_ = listener.Close()
			now := time.Now().UTC()
			cfg, svc, old := serverRotationFixture(t, now, port)
			store := storage.New(cfg.ConfigDir)
			csr, key := renewalTestCSR(t)
			nodePEM, err := svc.issueInitialNodeCertificate(context.Background(), "11111111-1111-4111-8111-111111111111", csr, now)
			if err != nil {
				t.Fatal(err)
			}
			node := renewalTestCertificate(t, nodePEM)
			material, err := store.LoadControlIdentity(old.CAGeneration, old.ServerGeneration)
			if err != nil {
				t.Fatal(err)
			}
			db, err := sqldb.Open(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			auth, err := NewControlNodeAuthorizer(db, controllerIDFromStore(t, cfg.ConfigDir), old, material, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			endpoint := controlpki.Endpoint{BindIP: old.BindIP, Advertised: old.Advertised, Port: old.Port}
			routes := []controlserver.Route{{ID: "node.ping", Method: "GET", Path: "/control/v1/ping", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				identity, ok := controlserver.IdentityFromContext(r.Context())
				if !ok || identity.BindingEpoch != 1 {
					t.Error("lost node authorization")
				}
				w.WriteHeader(http.StatusNoContent)
			})}}
			live, err := controlserver.New(material, endpoint, old.CAPin, auth, routes)
			if err != nil {
				t.Fatal(err)
			}
			roots := x509.NewCertPool()
			if !roots.AppendCertsFromPEM(material.CACert) {
				t.Fatal("CA")
			}
			clientTLS := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: old.Advertised, Certificates: []tls.Certificate{{Certificate: [][]byte{node.Raw}, PrivateKey: key}}, VerifyConnection: func(cs tls.ConnectionState) error {
				if controlpki.Pin(cs.VerifiedChains[0][len(cs.VerifiedChains[0])-1]) != old.CAPin {
					return errors.New("wrong pin")
				}
				return nil
			}}
			address := net.JoinHostPort(old.BindIP, strconv.Itoa(old.Port))
			url := "https://" + address + "/control/v1/ping"
			start := func(r *controlserver.Runtime) (context.CancelFunc, chan error) {
				ctx, cancel := context.WithCancel(context.Background())
				ch := make(chan error, 1)
				go func() { ch <- r.Serve(ctx) }()
				return cancel, ch
			}
			cancel, done := start(live)
			client := &http.Client{Transport: &http.Transport{TLSClientConfig: clientTLS.Clone()}, Timeout: 2 * time.Second}
			defer client.CloseIdleConnections()
			waitServerRotationTLS(t, client, url)
			conn, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", address, clientTLS)
			if err != nil {
				cancel()
				live.Close()
				<-done
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			if failure {
				svc.controlRotationStep = func(step string) error {
					if step == "before-runtime-publication" {
						return errors.New("injected committed publication failure")
					}
					return nil
				}
			}
			rotated, err := svc.rotateControlServerLeaf(context.Background(), old.ServerGeneration, time.Now(), live)
			if failure != (err != nil) {
				t.Fatalf("rotation error=%v", err)
			}
			committed, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			if committed.Controller.Control.ServerGeneration == old.ServerGeneration {
				t.Fatal("state not committed")
			}
			assertRotationConnectionClosed(t, conn)
			if failure {
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("failed runtime did not close")
				}
				cancel()
				if conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond); err == nil {
					_ = conn.Close()
					t.Fatal("failed publication left admission open")
				}
				svc.controlRotationStep = nil
				if _, err := newFastControllerTestService(cfg).Init(); err != nil {
					t.Fatal(err)
				}
				material, err = store.LoadControlIdentity(old.CAGeneration, committed.Controller.Control.ServerGeneration)
				if err != nil {
					t.Fatal(err)
				}
				live, err = controlserver.New(material, endpoint, old.CAPin, auth, routes)
				if err != nil {
					t.Fatal(err)
				}
				cancel, done = start(live)
				waitServerRotationTLS(t, client, url)
			} else if rotated != *committed.Controller.Control {
				t.Fatal("wrong rotation result")
			}
			response, err := client.Get(url)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			active, err := store.LoadControlIdentity(old.CAGeneration, committed.Controller.Control.ServerGeneration)
			if err != nil {
				t.Fatal(err)
			}
			block, _ := pem.Decode(active.ServerCert)
			if response.StatusCode != http.StatusNoContent || !bytes.Equal(response.TLS.PeerCertificates[0].Raw, block.Bytes) {
				t.Fatal("runtime did not serve committed new identity")
			}
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func waitServerRotationTLS(t *testing.T, client *http.Client, url string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get(url)
		if err == nil {
			_ = response.Body.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("TLS did not start")
}
func assertRotationConnectionClosed(t *testing.T, conn net.Conn) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var buf [1]byte
	_, err := conn.Read(buf[:])
	var timeout net.Error
	if err == nil || (errors.As(err, &timeout) && timeout.Timeout()) {
		t.Fatal("old connection stayed open")
	}
}

func TestInternalServerRotationRecoveryRejectsJournalStateMismatch(t *testing.T) {
	for _, kind := range []string{"controller", "ca", "generation", "unsafe-retirement", "overlapping-preparation", "corrupt-committed"} {
		t.Run(kind, func(t *testing.T) {
			now := time.Now().UTC()
			cfg, svc, old := serverRotationFixture(t, now, 8443)
			store := storage.New(cfg.ConfigDir)
			svc.controlRotationStep = func(step string) error {
				if step == "after-state-sync" {
					return errors.New("simulated crash gate")
				}
				return nil
			}
			if _, err := svc.rotateControlServerLeaf(context.Background(), old.ServerGeneration, now, nil); err == nil {
				t.Fatal("fault not reached")
			}
			j, err := store.LoadControlServerRotationJournal()
			if err != nil {
				t.Fatal(err)
			}
			state, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "controller":
				state.Controller.ControllerID = "11111111-1111-4111-8111-111111111111"
			case "ca":
				state.Controller.Control.CAGeneration = strings.Repeat("d", 32)
			case "generation":
				state.Controller.Control.ServerGeneration = strings.Repeat("e", 32)
			case "unsafe-retirement":
				if err := os.Chmod(filepath.Join(cfg.ConfigDir, "control", "server", j.OldServerGeneration, "key.pem"), 0644); err != nil {
					t.Fatal(err)
				}
			case "overlapping-preparation":
				if err := store.SaveControlIdentityJournal(storage.ControlIdentityJournal{ControllerID: j.ControllerID, CAGeneration: j.CAGeneration, ServerGeneration: j.NewServerGeneration, StartedAt: now}); err != nil {
					t.Fatal(err)
				}
			case "corrupt-committed":
				if err := os.WriteFile(filepath.Join(cfg.ConfigDir, "control", "server", j.NewServerGeneration, "cert.pem"), []byte("broken"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.Save(state); err != nil {
				t.Fatal(err)
			}
			if _, err := newFastControllerTestService(cfg).Init(); err != nil {
				t.Fatal("optional control gate blocked local runtime", err)
			}
			retained, err := store.LoadControlServerRotationJournal()
			if err != nil || retained != j {
				t.Fatal("unsafe recovery lost journal", err)
			}
			after, err := store.Load()
			if err != nil || after.Controller.Control.ServerGeneration != state.Controller.Control.ServerGeneration {
				t.Fatal("unsafe recovery changed state", err)
			}
			if _, err := newFastControllerTestService(cfg).rotateControlServerLeaf(context.Background(), state.Controller.Control.ServerGeneration, now, nil); err == nil {
				t.Fatal("rotation bypassed recovery gate")
			}
			if kind == "overlapping-preparation" {
				if _, err := store.LoadControlIdentityJournal(); err != nil {
					t.Fatal("recovery discarded overlapping preparation journal", err)
				}
			}
		})
	}
}
