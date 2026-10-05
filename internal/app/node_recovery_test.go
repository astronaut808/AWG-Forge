package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/storage"
	"github.com/google/uuid"
)

func nodeRecoveryFixture(t *testing.T, age time.Duration) (*Service, config.State) {
	t.Helper()
	s := New(controllerTestConfig(t))
	state, err := s.Init()
	if err != nil {
		t.Fatal(err)
	}
	inv, status, key := nodeApprovalFixtureAt(t, time.Now().Add(-age))
	state.Mode = config.ModeNode
	state.ManagedNode = &config.ManagedNodeState{NodeID: status.NodeID, ControllerID: status.ControllerID, StateEpoch: uuid.NewString(), BindingEpoch: 1, DesiredGeneration: 7, BootSequence: 2}
	state.NodeConnection = &config.NodeConnectionState{ControllerURL: inv.ControllerURL, CAPin: inv.CAPin, CredentialGeneration: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	if err := s.store.SaveNodeIdentity(state.NodeConnection.CredentialGeneration, storage.NodeIdentityMaterial{CACert: []byte(inv.CACertPEM), Certificate: []byte(status.Certificate.CertificatePEM), PrivateKey: key}); err != nil {
		t.Fatal(err)
	}
	if err := s.store.Save(state); err != nil {
		t.Fatal(err)
	}
	return s, state
}

func recoverySession(t *testing.T, s *Service, state config.State) *NodeRecoverySession {
	t.Helper()
	r, err := s.BeginNodeRecovery(context.Background(), state.ManagedNode.NodeID, state.ManagedNode.ControllerID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})
	return r
}

func assertRecoveryLocalState(t *testing.T, before, after config.State) {
	t.Helper()
	after.Mode = before.Mode
	after.ManagedNode = before.ManagedNode
	after.NodeConnection = before.NodeConnection
	after.UpdatedAt = before.UpdatedAt
	if !reflect.DeepEqual(before, after) {
		t.Fatal("local configuration/auth/revisions changed")
	}
}

func TestNodeRecoveryDetachAndRebindPreserveLocalConfiguration(t *testing.T) {
	for _, detach := range []bool{true, false} {
		for _, age := range []time.Duration{0, 40 * 24 * time.Hour} {
			t.Run(map[bool]string{true: "detach", false: "rebind"}[detach]+age.String(), func(t *testing.T) {
				s, before := nodeRecoveryFixture(t, age)
				r := recoverySession(t, s, before)
				if detach {
					if err := r.Detach(context.Background()); err != nil {
						t.Fatal(err)
					}
				} else {
					inv, status, key := nodeApprovalFixture(t)
					if err := r.InstallNodeEnrollment(context.Background(), inv, status, key); err != nil {
						t.Fatal(err)
					}
				}
				after, err := s.store.Load()
				if err != nil {
					t.Fatal(err)
				}
				assertRecoveryLocalState(t, before, after)
				if detach {
					if after.EffectiveMode() != config.ModeStandalone || after.ManagedNode != nil || after.NodeConnection != nil {
						t.Fatal("detach retained authority")
					}
				} else {
					if after.ManagedNode.NodeID == before.ManagedNode.NodeID || after.ManagedNode.StateEpoch == before.ManagedNode.StateEpoch || after.ManagedNode.DesiredGeneration != 0 || after.ManagedNode.BootSequence != 0 || after.NodeConnection.CredentialGeneration == before.NodeConnection.CredentialGeneration {
						t.Fatal("rebind reused old identity/replay namespace")
					}
					m, err := s.store.LoadNodeIdentity(after.NodeConnection.CredentialGeneration)
					if err != nil || ValidateNodeIdentity(after.NodeConnection, m, false) != nil {
						t.Fatal("new binding has no valid credentials")
					}
				}
				if _, err := os.Lstat(filepath.Join(s.cfg.ConfigDir, "node", before.NodeConnection.CredentialGeneration)); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("active predecessor credentials retained")
				}
				if err := s.store.CheckNoNodeRecovery(); err != nil {
					t.Fatal(err)
				}
				if err := r.PreflightNodeEnrollment(context.Background()); err == nil {
					t.Fatal("consumed recovery authorization remained usable")
				}
			})
		}
	}
}

func TestNodeRecoveryRejectsLiveServerWrongConfirmationAndCancellation(t *testing.T) {
	s, before := nodeRecoveryFixture(t, 0)
	if _, err := s.BeginNodeRecovery(context.Background(), uuid.NewString(), before.ManagedNode.ControllerID); err == nil {
		t.Fatal("wrong local identity accepted")
	}
	lease, err := storage.AcquireStateLock(s.cfg.ConfigDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginNodeRecovery(context.Background(), before.ManagedNode.NodeID, before.ManagedNode.ControllerID); err == nil {
		t.Fatal("running server lease ignored")
	}
	_ = lease.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.BeginNodeRecovery(ctx, before.ManagedNode.NodeID, before.ManagedNode.ControllerID); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled root recovery admitted")
	}
	r := recoverySession(t, s, before)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Detach(context.Background()); err == nil {
		t.Fatal("closed authorization admitted")
	}
	after, _ := s.store.Load()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("rejected recovery changed state")
	}
}

func TestNodeRecoveryFailedApprovalKeepsOldBinding(t *testing.T) {
	s, before := nodeRecoveryFixture(t, 0)
	r := recoverySession(t, s, before)
	inv, status, key := nodeApprovalFixture(t)
	status.NodeID = before.ManagedNode.NodeID
	if err := r.InstallNodeEnrollment(context.Background(), inv, status, key); err == nil {
		t.Fatal("archived identity resurrected")
	}
	status.NodeID = uuid.NewString()
	inv.CAPin = "wrong"
	if err := r.InstallNodeEnrollment(context.Background(), inv, status, key); err == nil {
		t.Fatal("mismatched pin accepted")
	}
	after, _ := s.store.Load()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed approval changed old binding")
	}
	if err := s.store.CheckNoNodeRecovery(); err != nil {
		t.Fatal("failed approval published a journal")
	}
}

func TestNodeRecoveryCancellationWhileWaitingForMutation(t *testing.T) {
	s, before := nodeRecoveryFixture(t, 0)
	r := recoverySession(t, s, before)
	lock, err := storage.AcquireStateMutationLock(s.cfg.ConfigDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := r.Detach(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("blocked mutation did not cancel")
	}
	after, err := s.store.Load()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("cancelled detach changed state")
	}
	if err := s.store.CheckNoNodeRecovery(); err != nil {
		t.Fatal("cancelled lock wait published journal")
	}
}

func TestNodeRecoveryUnexpectedEvidenceFencesCleanup(t *testing.T) {
	for _, overlap := range []string{"enrollment", "renewal", "malformed-recovery"} {
		t.Run(overlap, func(t *testing.T) {
			s, before := nodeRecoveryFixture(t, 0)
			r := recoverySession(t, s, before)
			s.nodeRecoveryStep = func(point string) error {
				if point == "after-state-save" {
					return errors.New("crash")
				}
				return nil
			}
			if err := r.Detach(context.Background()); err == nil {
				t.Fatal("fault missed")
			}
			if err := r.Close(); err != nil {
				t.Fatal(err)
			}
			path := s.store.NodeIdentityJournalPath()
			switch overlap {
			case "renewal":
				path = s.store.NodeRenewalJournalPath()
			case "malformed-recovery":
				path = s.store.NodeRecoveryJournalPath()
			}
			if err := os.WriteFile(path, []byte("retain-evidence"), 0600); err != nil {
				t.Fatal(err)
			}
			fresh := New(s.cfg)
			if _, err := fresh.Init(); err == nil {
				t.Fatal("ambiguous recovery completed")
			}
			if _, err := os.Lstat(filepath.Join(s.cfg.ConfigDir, "node", before.NodeConnection.CredentialGeneration, "key.pem")); err != nil {
				t.Fatal("credentials removed before checking evidence")
			}
			if err := fresh.store.CheckNoNodeRecovery(); err == nil {
				t.Fatal("evidence deleted")
			}
			if _, err := fresh.NodeAgentState(context.Background()); err == nil {
				t.Fatal("agent admitted with pending recovery")
			}
			if _, err := fresh.StartManagedNodeBootContext(context.Background()); err == nil {
				t.Fatal("boot admitted with pending recovery")
			}
		})
	}
}

func TestNodeRecoveryCrashMatrix(t *testing.T) {
	for _, detach := range []bool{true, false} {
		for _, point := range []string{"journal-created", "journal-written", "journal-synced", "journal-published", "journal-durable", "staged", "before-state-save", "after-state-save", "committed", "before-retirement", "before-journal-cleanup"} {
			t.Run(map[bool]string{true: "detach/", false: "rebind/"}[detach]+point, func(t *testing.T) {
				s, before := nodeRecoveryFixture(t, 0)
				r := recoverySession(t, s, before)
				injected := errors.New("injected crash boundary")
				hit := false
				s.nodeRecoveryStep = func(p string) error {
					if p == point {
						hit = true
						return injected
					}
					return nil
				}
				var err error
				if detach {
					err = r.Detach(context.Background())
				} else {
					inv, status, key := nodeApprovalFixture(t)
					err = r.InstallNodeEnrollment(context.Background(), inv, status, key)
				}
				if err == nil || !hit {
					t.Fatal("crash boundary not exercised")
				}
				_ = r.Close()
				state, err := s.store.Load()
				if err != nil {
					t.Fatal(err)
				}
				assertRecoveryLocalState(t, before, state)
				committed := point == "after-state-save" || point == "committed" || point == "before-retirement" || point == "before-journal-cleanup"
				if !committed && !reflect.DeepEqual(before, state) {
					t.Fatal("precommit boundary changed authority")
				}
				fresh := New(s.cfg)
				if _, err := fresh.Init(); err != nil {
					t.Fatal(err)
				}
				after, err := fresh.store.Load()
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(state, after) {
					t.Fatal("recovery rewrote/rolled back committed authority")
				}
				if err := fresh.store.CheckNoNodeRecovery(); err != nil {
					t.Fatal(err)
				}
				_, err = os.Lstat(filepath.Join(s.cfg.ConfigDir, "node", before.NodeConnection.CredentialGeneration))
				if committed && !errors.Is(err, os.ErrNotExist) {
					t.Fatal("committed recovery retained predecessor")
				}
				if !committed && err != nil {
					t.Fatal("rollback deleted predecessor")
				}
			})
		}
	}
}

func TestNodeRecoveryUncertainSaveAndUnexpectedStateRetainEvidence(t *testing.T) {
	for _, tamper := range []bool{false, true} {
		t.Run(map[bool]string{false: "uncertain", true: "state-drift"}[tamper], func(t *testing.T) {
			s, before := nodeRecoveryFixture(t, 0)
			r := recoverySession(t, s, before)
			s.saveState = func(state config.State) error {
				if err := s.store.Save(state); err != nil {
					return err
				}
				return errors.New("save outcome uncertain")
			}
			inv, status, key := nodeApprovalFixture(t)
			if err := r.InstallNodeEnrollment(context.Background(), inv, status, key); err == nil {
				t.Fatal("uncertain save reported success")
			}
			_ = r.Close()
			committed, _ := s.store.Load()
			if tamper {
				committed.ServerHost = "unexpected-state-drift.example"
				if err := s.store.Save(committed); err != nil {
					t.Fatal(err)
				}
			}
			fresh := New(s.cfg)
			_, err := fresh.Init()
			if tamper {
				if err == nil || fresh.store.CheckNoNodeRecovery() == nil {
					t.Fatal("ambiguous evidence bypassed")
				}
				if _, err := fresh.store.LoadNodeIdentity(before.NodeConnection.CredentialGeneration); err != nil {
					t.Fatal("ambiguous recovery deleted predecessor")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				after, _ := fresh.store.Load()
				if !reflect.DeepEqual(committed, after) {
					t.Fatal("uncertain commit rolled back")
				}
			}
		})
	}
}

func TestNodeRecoveryRetiresPendingRenewalAndFencesStaleWrites(t *testing.T) {
	s, before := nodeRecoveryFixture(t, 21*24*time.Hour)
	renewal, err := s.PrepareNodeCertificateRenewal(context.Background(), *before.NodeConnection)
	if err != nil {
		t.Fatal(err)
	}
	r := recoverySession(t, s, before)
	inv, status, key := nodeApprovalFixture(t)
	if err := r.InstallNodeEnrollment(context.Background(), inv, status, key); err != nil {
		t.Fatal(err)
	}
	after, _ := s.store.Load()
	if _, err := s.PrepareNodeCertificateRenewal(context.Background(), *before.NodeConnection); err == nil {
		t.Fatal("stale renewal owner admitted")
	}
	if err := s.InstallNodeCertificateRenewal(context.Background(), *before.NodeConnection, renewal.Generation, *status.Certificate); err == nil {
		t.Fatal("stale response installed")
	}
	unchanged, _ := s.store.Load()
	if !reflect.DeepEqual(after, unchanged) {
		t.Fatal("stale worker changed new binding")
	}
	if _, err := os.Lstat(s.store.NodeRenewalJournalPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("pending renewal authority retained")
	}
	if _, err := os.Lstat(filepath.Join(s.cfg.ConfigDir, "node-renewal", renewal.Generation)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("pending candidate key retained")
	}
	boot, err := s.StartManagedNodeBootContext(context.Background())
	if err != nil || boot.StateEpoch != after.ManagedNode.StateEpoch || boot.BootSequence != 1 {
		t.Fatal("new worker reused old boot namespace")
	}
}
