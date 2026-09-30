package backup

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlpki"
	"github.com/astronaut808/awg-forge/internal/sqldb"
	"github.com/astronaut808/awg-forge/internal/storage"
)

type registryRestoreFixture struct {
	cfg      config.Config
	state    config.State
	material controlpki.Material
	identity sqldb.NodeIdentity
	initial  controlpki.NodeCertificate
	now      time.Time
}

func newRegistryRestoreFixture(t *testing.T, external bool) registryRestoreFixture {
	t.Helper()
	ctx := context.Background()
	cfg, _ := controllerBackupFixture(t, external)
	now := time.Now().UTC().Truncate(time.Second)
	issuedAt := now.Add(-20 * 24 * time.Hour)
	control, err := app.New(cfg).PrepareControlIdentity(ctx, app.ControlIdentityRequest{BindIP: "127.0.0.1", Advertised: "127.0.0.1", Port: 8443, Now: issuedAt})
	if err != nil {
		t.Fatal(err)
	}
	state, err := storage.New(cfg.ConfigDir).Load()
	if err != nil {
		t.Fatal(err)
	}
	material, err := storage.New(cfg.ConfigDir).LoadControlIdentity(control.CAGeneration, control.ServerGeneration)
	if err != nil {
		t.Fatal(err)
	}
	initial := issueRestoreTestCertificate(t, material, issuedAt.Add(time.Second))
	identity := sqldb.NodeIdentity{ControllerID: state.Controller.ControllerID, NodeID: "11111111-1111-4111-8111-111111111111", BindingEpoch: 1}
	db := openRegistryRestoreDB(t, cfg)
	if _, err := db.RegisterInitialNodeCertificate(ctx, identity, control.CAGeneration, initial, issuedAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return registryRestoreFixture{cfg: cfg, state: state, material: material, identity: identity, initial: initial, now: now}
}

func issueRestoreTestCertificate(t *testing.T, material controlpki.Material, now time.Time) controlpki.NodeCertificate {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := controlpki.IssueNodeCertificate(material, csr, now)
	if err != nil {
		t.Fatal(err)
	}
	return certificate
}

func openRegistryRestoreDB(t *testing.T, cfg config.Config) *sqldb.DB {
	t.Helper()
	db, err := sqldb.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func parsedRestoreCertificate(t *testing.T, certificate controlpki.NodeCertificate) *x509.Certificate {
	t.Helper()
	parsed, err := x509.ParseCertificate(certificate.DER)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func assertRegistryRestoreDenied(t *testing.T, f registryRestoreFixture, certificates []controlpki.NodeCertificate) {
	t.Helper()
	ctx := context.Background()
	db := openRegistryRestoreDB(t, f.cfg)
	authorizer, err := app.NewControlNodeAuthorizer(db, f.identity.ControllerID, *f.state.Controller.Control, f.material, func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	for _, certificate := range certificates {
		if _, err := authorizer.Authorize(ctx, parsedRestoreCertificate(t, certificate), "node.test"); !errors.Is(err, sqldb.ErrNodeCertificateDenied) {
			t.Fatalf("old or post-snapshot node authority admitted: %v", err)
		}
	}
	if err := db.VerifyControllerRestoreReset(ctx); err != nil {
		t.Fatal(err)
	}
	user, err := db.FindControllerAdmin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RecoverControllerAdmin(ctx, user, nil); err != nil {
		t.Fatal(err)
	}
	for _, certificate := range certificates {
		if _, err := authorizer.Authorize(ctx, parsedRestoreCertificate(t, certificate), "node.test"); !errors.Is(err, sqldb.ErrNodeCertificateDenied) {
			t.Fatalf("administrator recovery reactivated node authority: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := storage.New(f.cfg.ConfigDir).Load()
	if err != nil || !reflect.DeepEqual(restored, f.state) {
		t.Fatalf("security reconciliation changed archived state: %v", err)
	}
	if err := storage.New(f.cfg.ConfigDir).CheckRestorePending(); err != nil {
		t.Fatal(err)
	}
	paths, _ := storage.ControlIdentityRelativePaths(f.state.Controller.Control.CAGeneration, f.state.Controller.Control.ServerGeneration)
	for _, path := range append(paths, filepath.Base(f.cfg.DatabasePath)) {
		root := f.cfg.ConfigDir
		if path == filepath.Base(f.cfg.DatabasePath) {
			root = filepath.Dir(f.cfg.DatabasePath)
		}
		assertMode(t, filepath.Join(root, path), 0600)
	}
}

func TestControllerRegistryRestoreRejectsStaleNodeAuthority(t *testing.T) {
	for _, external := range []bool{false, true} {
		for _, scenario := range []string{"serial-revocation", "binding-revocation", "renewal-after-snapshot", "renewal-overlap", "superseded-history", "rebind-after-snapshot"} {
			name := "internal/" + scenario
			if external {
				name = "external/" + scenario
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				f := newRegistryRestoreFixture(t, external)
				generation := f.state.Controller.Control.CAGeneration
				certificates := []controlpki.NodeCertificate{f.initial}
				db := openRegistryRestoreDB(t, f.cfg)
				if scenario == "renewal-overlap" || scenario == "superseded-history" {
					at := f.now
					if scenario == "superseded-history" {
						at = at.Add(-2 * 24 * time.Hour)
					}
					successor := issueRestoreTestCertificate(t, f.material, at)
					// At -2 days the original is not yet eligible; use the exact
					// two-thirds boundary rather than shortening its validity.
					if scenario == "superseded-history" {
						old := parsedRestoreCertificate(t, f.initial)
						at = old.NotBefore.Add(old.NotAfter.Sub(old.NotBefore) * 2 / 3)
						successor = issueRestoreTestCertificate(t, f.material, at)
						f.now = at.Add(2 * 24 * time.Hour)
					}
					if _, err := db.RenewNodeCertificate(ctx, f.identity.ControllerID, generation, parsedRestoreCertificate(t, f.initial), successor, at); err != nil {
						t.Fatal(err)
					}
					certificates = append(certificates, successor)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				archive, err := Create(ctx, f.cfg, app.New(f.cfg), testPassword, Options{})
				if err != nil {
					t.Fatal(err)
				}
				path := writeTempArchive(t, archive.Data)
				db = openRegistryRestoreDB(t, f.cfg)
				switch scenario {
				case "serial-revocation":
					err = db.RevokeNodeCertificate(ctx, generation, f.initial.Serial, f.now)
				case "binding-revocation", "renewal-overlap", "superseded-history":
					err = db.RevokeNodeBinding(ctx, f.identity, f.now)
				case "renewal-after-snapshot":
					successor := issueRestoreTestCertificate(t, f.material, f.now)
					_, err = db.RenewNodeCertificate(ctx, f.identity.ControllerID, generation, parsedRestoreCertificate(t, f.initial), successor, f.now)
					certificates = append(certificates, successor)
				case "rebind-after-snapshot":
					if err := db.RevokeNodeBinding(ctx, f.identity, f.now); err != nil {
						t.Fatal(err)
					}
					successor := issueRestoreTestCertificate(t, f.material, f.now)
					_, err = db.RebindRevokedNodeCertificate(ctx, f.identity, generation, successor, f.now)
					certificates = append(certificates, successor)
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				if err := Restore(ctx, f.cfg, testPassword, path); err != nil {
					t.Fatal(err)
				}
				assertRegistryRestoreDenied(t, f, certificates)
				if scenario == "serial-revocation" {
					// Repeating both stale restore and pre-restore rollback must
					// apply the same denial policy, even after admin recovery.
					if err := Restore(ctx, f.cfg, testPassword, path); err != nil {
						t.Fatal(err)
					}
					assertRegistryRestoreDenied(t, f, certificates)
					pre, err := filepath.Glob(filepath.Join(f.cfg.ConfigDir, "backups", "pre-restore-*.afbackup"))
					if err != nil || len(pre) < 1 {
						t.Fatal("missing pre-restore backup", err)
					}
					body, err := os.ReadFile(pre[0])
					if err != nil {
						t.Fatal(err)
					}
					if err := Restore(ctx, f.cfg, testPassword, writeTempArchive(t, body)); err != nil {
						t.Fatal(err)
					}
					assertRegistryRestoreDenied(t, f, certificates)
				}
			})
		}
	}
}

func TestControllerRegistryArchiveRejectsMismatchBeforeTargetMutation(t *testing.T) {
	f := newRegistryRestoreFixture(t, false)
	ctx := context.Background()
	archive, err := Create(ctx, f.cfg, app.New(f.cfg), testPassword, Options{})
	if err != nil {
		t.Fatal(err)
	}
	validated, err := validateBackupData(ctx, testPassword, archive.Data)
	if err != nil {
		t.Fatal(err)
	}
	for i := range validated.Files {
		if validated.Files[i].Path != controllerSnapshotArchivePath {
			continue
		}
		path := filepath.Join(t.TempDir(), "snapshot.sqlite")
		if err := os.WriteFile(path, validated.Files[i].Data, 0600); err != nil {
			t.Fatal(err)
		}
		conn, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.ExecContext(ctx, "UPDATE control_node_certificates SET issuer_generation = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'"); err != nil {
			t.Fatal(err)
		}
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
		validated.Files[i].Data, err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.ReadFile(storage.New(f.cfg.ConfigDir).StatePath())
	if err != nil {
		t.Fatal(err)
	}
	if err := validateControllerArchive(ctx, validated.Files, validated.State); err == nil {
		t.Fatal("mixed registry and PKI accepted")
	}
	path := writeTempArchive(t, rebuildControlArchive(t, validated.Metadata, validated.Files))
	if _, err := Verify(ctx, f.cfg, testPassword, path); err == nil {
		t.Fatal("mixed archive passed public verification")
	}
	if err := Restore(ctx, f.cfg, testPassword, path); err == nil {
		t.Fatal("mixed archive passed cold restore")
	}
	after, err := os.ReadFile(storage.New(f.cfg.ConfigDir).StatePath())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("invalid archive changed target state", err)
	}
	if err := storage.New(f.cfg.ConfigDir).CheckRestorePending(); err != nil {
		t.Fatal("invalid archive created restore-pending", err)
	}
}

func TestControllerRegistryRestoreCrashMatrix(t *testing.T) {
	for _, external := range []bool{false, true} {
		points := []string{"after-marker-sync", "after-files-install", "after-reconciliation", "after-files-sync", "after-marker-clear"}
		if external {
			points = append(points, "after-external-install", "after-external-sync")
		}
		for _, point := range points {
			name := "internal/" + point
			if external {
				name = "external/" + point
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				f := newRegistryRestoreFixture(t, external)
				archive, err := Create(ctx, f.cfg, app.New(f.cfg), testPassword, Options{})
				if err != nil {
					t.Fatal(err)
				}
				validated, err := validateBackupData(ctx, testPassword, archive.Data)
				if err != nil {
					t.Fatal(err)
				}
				stateLock, err := storage.AcquireStateLock(f.cfg.ConfigDir)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = stateLock.Close() }()
				mutationLock, err := storage.AcquireStateMutationLock(f.cfg.ConfigDir)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = mutationLock.Close() }()
				fault := errors.New("injected restore crash")
				err = restoreControllerWithStep(ctx, f.cfg, testPassword, f.state, validated, restoreFiles, func(step string) error {
					if step == point {
						return fault
					}
					return nil
				})
				if !errors.Is(err, fault) {
					t.Fatalf("restore fault was not reached: %v", err)
				}
				if err := mutationLock.Close(); err != nil {
					t.Fatal(err)
				}
				if err := stateLock.Close(); err != nil {
					t.Fatal(err)
				}
				store := storage.New(f.cfg.ConfigDir)
				if point != "after-marker-clear" {
					if !errors.Is(store.CheckRestorePending(), storage.ErrRestorePending) {
						t.Fatal("interrupted restore lost startup gate")
					}
					if _, err := app.New(f.cfg).Init(); !errors.Is(err, storage.ErrRestorePending) {
						t.Fatalf("interrupted restore allowed startup: %v", err)
					}
				} else if err := store.CheckRestorePending(); err != nil {
					t.Fatal("post-clear fault does not imply a retained marker", err)
				}
				if point == "after-reconciliation" || point == "after-files-sync" || point == "after-external-sync" || point == "after-marker-clear" {
					db := openRegistryRestoreDB(t, f.cfg)
					if err := db.VerifyControllerRestoreReset(ctx); err != nil {
						t.Fatal("authority was not durable before marker clear", err)
					}
					if _, err := db.FindActiveNodeCertificate(ctx, f.identity.ControllerID, f.state.Controller.Control.CAGeneration, parsedRestoreCertificate(t, f.initial), f.now); !errors.Is(err, sqldb.ErrNodeCertificateDenied) {
						t.Fatalf("post-commit crash reactivated certificate: %v", err)
					}
				}
			})
		}
	}
}

func TestControllerRegistryRestoreEveryConfigRenameRetainsCrashGate(t *testing.T) {
	for _, external := range []bool{false, true} {
		for _, after := range []bool{false, true} {
			t.Run("renames", func(t *testing.T) {
				ctx := context.Background()
				f := newRegistryRestoreFixture(t, external)
				archive, err := Create(ctx, f.cfg, app.New(f.cfg), testPassword, Options{})
				if err != nil {
					t.Fatal(err)
				}
				validated, err := validateBackupData(ctx, testPassword, archive.Data)
				if err != nil {
					t.Fatal(err)
				}
				// First measure the complete move set without changing authority.
				stage := t.TempDir()
				for _, file := range validated.Files {
					if file.Path == controllerSnapshotArchivePath {
						continue
					}
					path := filepath.Join(stage, filepath.FromSlash(file.Path))
					if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, file.Data, 0600); err != nil {
						t.Fatal(err)
					}
				}
				files := append([]restoreFile(nil), validated.Files...)
				for i := range files {
					if files[i].Path == controllerSnapshotArchivePath {
						if external {
							files = append(files[:i], files[i+1:]...)
						} else {
							files[i].Path = filepath.Base(f.cfg.DatabasePath)
						}
						break
					}
				}
				if !external {
					for _, file := range files {
						if file.Path == filepath.Base(f.cfg.DatabasePath) {
							if err := os.WriteFile(filepath.Join(stage, file.Path), file.Data, 0600); err != nil {
								t.Fatal(err)
							}
						}
					}
				}
				count := 0
				if err := restoreFilesWithRename(stage, files, func(src, dst string) error {
					count++
					return os.Rename(src, dst)
				}); err != nil {
					t.Fatal(err)
				}
				if count == 0 {
					t.Fatal("positive control did not move restore files")
				}
				for index := 1; index <= count; index++ {
					t.Run(strconv.Itoa(index), func(t *testing.T) {
						// A separate installation with the same archive identity is
						// test-only staging for a crash at each measured rename.
						root := t.TempDir()
						for _, file := range files {
							path := filepath.Join(root, filepath.FromSlash(file.Path))
							if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
								t.Fatal(err)
							}
							if err := os.WriteFile(path, file.Data, 0600); err != nil {
								t.Fatal(err)
							}
						}
						store := storage.New(root)
						if err := store.BeginRestorePending(f.identity.ControllerID); err != nil {
							t.Fatal(err)
						}
						hit := false
						n := 0
						func() {
							defer func() {
								if value := recover(); value != nil && value != "restore crash" {
									panic(value)
								}
							}()
							_ = restoreFilesWithRename(root, files, func(src, dst string) error {
								n++
								if n == index && !after {
									hit = true
									panic("restore crash")
								}
								if err := os.Rename(src, dst); err != nil {
									return err
								}
								if n == index && after {
									hit = true
									panic("restore crash")
								}
								return nil
							})
						}()
						if !hit || !errors.Is(store.CheckRestorePending(), storage.ErrRestorePending) {
							t.Fatal("config rename crash escaped restore marker")
						}
					})
				}
			})
		}
	}
}

func TestControllerRegistryRestoreFailedRollbackKeepsGate(t *testing.T) {
	for _, external := range []bool{false, true} {
		t.Run(strconv.FormatBool(external), func(t *testing.T) {
			ctx := context.Background()
			f := newRegistryRestoreFixture(t, external)
			archive, err := Create(ctx, f.cfg, app.New(f.cfg), testPassword, Options{})
			if err != nil {
				t.Fatal(err)
			}
			validated, err := validateBackupData(ctx, testPassword, archive.Data)
			if err != nil {
				t.Fatal(err)
			}
			stateLock, err := storage.AcquireStateLock(f.cfg.ConfigDir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = stateLock.Close() }()
			mutationLock, err := storage.AcquireStateMutationLock(f.cfg.ConfigDir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = mutationLock.Close() }()
			failed := false
			err = restoreControllerWithFiles(ctx, f.cfg, testPassword, f.state, validated, func(root string, files []restoreFile) error {
				return restoreFilesWithRename(root, files, func(src, dst string) error {
					if failed || strings.Contains(src, ".restore-tmp-") {
						failed = true
						return errors.New("injected file switch and rollback failure")
					}
					return os.Rename(src, dst)
				})
			})
			if err == nil || !strings.Contains(err.Error(), "rollback failed") {
				t.Fatalf("rollback failure not observed: %v", err)
			}
			_ = mutationLock.Close()
			_ = stateLock.Close()
			if !errors.Is(storage.New(f.cfg.ConfigDir).CheckRestorePending(), storage.ErrRestorePending) {
				t.Fatal("failed rollback cleared restore gate")
			}
			if _, err := app.New(f.cfg).Init(); !errors.Is(err, storage.ErrRestorePending) {
				t.Fatalf("failed rollback allowed startup: %v", err)
			}
		})
	}
}

func TestControllerRegistryRestoreLegacySchemas(t *testing.T) {
	for _, version := range []int{5, 6, 7} {
		t.Run(strconv.Itoa(version), func(t *testing.T) {
			ctx := context.Background()
			f := newRegistryRestoreFixture(t, false)
			archive, err := Create(ctx, f.cfg, app.New(f.cfg), testPassword, Options{})
			if err != nil {
				t.Fatal(err)
			}
			validated, err := validateBackupData(ctx, testPassword, archive.Data)
			if err != nil {
				t.Fatal(err)
			}
			for i := range validated.Files {
				if validated.Files[i].Path != controllerSnapshotArchivePath {
					continue
				}
				path := filepath.Join(t.TempDir(), "legacy.sqlite")
				if err := os.WriteFile(path, validated.Files[i].Data, 0600); err != nil {
					t.Fatal(err)
				}
				conn, err := sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				statements := []string{"DROP INDEX control_node_certificates_one_initial_per_binding_idx"}
				if version <= 6 {
					statements = append(statements, "DROP INDEX control_node_certificates_one_successor_idx", "ALTER TABLE control_node_certificates DROP COLUMN predecessor_serial", "ALTER TABLE control_node_certificates DROP COLUMN predecessor_issuer_generation")
				}
				if version == 5 {
					statements = append(statements, "DROP TABLE control_node_certificates", "DROP TABLE control_node_bindings")
				}
				for _, statement := range statements {
					if _, err := conn.ExecContext(ctx, statement); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := conn.ExecContext(ctx, "DELETE FROM schema_migrations WHERE version > ?", version); err != nil {
					t.Fatal(err)
				}
				if err := conn.Close(); err != nil {
					t.Fatal(err)
				}
				validated.Files[i].Data, err = os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			path := writeTempArchive(t, rebuildControlArchive(t, validated.Metadata, validated.Files))
			if _, err := Verify(ctx, f.cfg, testPassword, path); err != nil {
				t.Fatalf("legacy schema %d verification: %v", version, err)
			}
			if err := Restore(ctx, f.cfg, testPassword, path); err != nil {
				t.Fatalf("legacy schema %d restore: %v", version, err)
			}
			assertRegistryRestoreDenied(t, f, []controlpki.NodeCertificate{f.initial})
			status, err := sqldb.Check(ctx, f.cfg)
			if err != nil || status.SchemaVersion != sqldb.CurrentSchemaVersion {
				t.Fatalf("legacy schema not migrated under restore gate: version=%d, error=%v", status.SchemaVersion, err)
			}
		})
	}
}
