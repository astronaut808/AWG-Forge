package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/base32"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlpki"
	"github.com/astronaut808/awg-forge/internal/storage"
	"github.com/pquerna/otp/totp"
)

func processServerValidity(t *testing.T, dir string, control config.ControlIdentityState, before, after time.Time) {
	t.Helper()
	m, err := storage.New(dir).LoadControlIdentity(control.CAGeneration, control.ServerGeneration)
	if err != nil {
		t.Fatal(err)
	}
	cert := func(body []byte) *x509.Certificate {
		block, _ := pem.Decode(body)
		if block == nil {
			t.Fatal("fixture certificate")
		}
		v, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	block, _ := pem.Decode(m.CAKey)
	if block == nil {
		t.Fatal("fixture key")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	ca, leaf := cert(m.CACert), cert(m.ServerCert)
	leaf.NotBefore, leaf.NotAfter = before, after
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, leaf.PublicKey, key.(ed25519.PrivateKey))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "control", "server", control.ServerGeneration, controlpki.ServerCertFile), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
}

func processEnabledController(t *testing.T, dir string, port, controlPort int, issuedAt time.Time) (config.Config, config.ControlIdentityState) {
	t.Helper()
	cfg := enrollmentProcessConfig(dir, port)
	svc := app.New(cfg)
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("server-lifecycle-totp"))
	now := time.Now().UTC()
	code, err := totp.GenerateCode(secret, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.ActivateController(context.Background(), app.ControllerActivationRequest{Username: "admin", Password: "correct horse battery staple", TOTPSecret: secret, TOTPConfirmation: code, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.PrepareControlIdentity(context.Background(), app.ControlIdentityRequest{BindIP: "127.0.0.1", Advertised: "127.0.0.1", Port: controlPort, Now: issuedAt})
	if err != nil {
		t.Fatal(err)
	}
	store := storage.New(dir)
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	state.Controller.Control.Enabled = true
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	return cfg, *state.Controller.Control
}

func TestServerRenewalStartupProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("process integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	binary := buildEnrollmentProcessBinary(ctx, t)
	for _, kind := range []string{"due", "expired", "disabled", "expired CA"} {
		t.Run(kind, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			browserPort, controlPort := enrollmentFreePort(t), enrollmentFreePort(t)
			age := -21 * 24 * time.Hour
			if kind == "expired" || kind == "disabled" {
				age = -31 * 24 * time.Hour
			}
			if kind == "expired CA" {
				age = -6 * 365 * 24 * time.Hour
			}
			_, control := processEnabledController(t, root, browserPort, controlPort, time.Now().Add(age))
			store := storage.New(root)
			before, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			if kind == "disabled" {
				before.Controller.Control.Enabled = false
				if err := store.Save(before); err != nil {
					t.Fatal(err)
				}
			}
			p := enrollmentStartProcess(ctx, t, binary, append(enrollmentEnv(root, browserPort), "EXTERNAL_INTERFACE=lo"), "serve")
			defer p.stop(t)
			enrollmentWaitHTTP(ctx, t, fmt.Sprintf("http://127.0.0.1:%d/", browserPort))
			after, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			changed := kind == "due" || kind == "expired"
			if (after.Controller.Control.ServerGeneration != control.ServerGeneration) != changed {
				t.Fatal("startup renewal did not match policy")
			}
			if changed {
				m, err := store.LoadControlIdentity(control.CAGeneration, after.Controller.Control.ServerGeneration)
				if err != nil {
					t.Fatal(err)
				}
				roots := x509.NewCertPool()
				if !roots.AppendCertsFromPEM(m.CACert) {
					t.Fatal("CA fixture")
				}
				conn, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", fmt.Sprintf("127.0.0.1:%d", controlPort), &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: "127.0.0.1"})
				if err != nil {
					t.Fatal(err)
				}
				_ = conn.Close()
				if err := store.CheckNoControlServerRotation(); err != nil {
					t.Fatal(err)
				}
			} else {
				conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", controlPort), time.Second)
				if err == nil {
					_ = conn.Close()
					t.Fatal("closed control admitted")
				}
			}
			after.Controller.Control.ServerGeneration = before.Controller.Control.ServerGeneration
			// Render timestamps belong to process startup; all configuration and
			// authority fields, including revisions/epochs, must otherwise match.
			before.UpdatedAt, after.UpdatedAt = time.Time{}, time.Time{}
			for i := range before.Tunnels {
				before.Tunnels[i].LastRenderAt, after.Tunnels[i].LastRenderAt = time.Time{}, time.Time{}
				before.Tunnels[i].UpdatedAt, after.Tunnels[i].UpdatedAt = time.Time{}, time.Time{}
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("startup changed local configuration/authority")
			}
		})
	}
}

// A SQLite exclusive transaction is a test barrier after Init/RenderAll but
// before auth loading completes. Then the parent holds the mutation lock and
// releases SQLite, proving SIGTERM reaches StartControl's cancellable wait.
func TestServeSignalCancelsControlStartupProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("process integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	binary := buildEnrollmentProcessBinary(ctx, t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	port, controlPort := enrollmentFreePort(t), enrollmentFreePort(t)
	cfg, control := processEnabledController(t, root, port, controlPort, time.Now().Add(-31*24*time.Hour))
	raw, err := sql.Open("sqlite", cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	conn, err := raw.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "PRAGMA journal_mode=DELETE"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN EXCLUSIVE"); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), "ROLLBACK") }()
	p := enrollmentStartProcess(ctx, t, binary, enrollmentEnv(root, port), "serve")
	defer p.stop(t)
	if err := p.waitFor("server.startup.local_ready", 5*time.Second); err != nil {
		t.Fatal("did not finish local Init/RenderAll")
	}
	lock, err := storage.AcquireStateMutationLock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		t.Fatal(err)
	}
	if err := p.waitFor("control.start.checking", 5*time.Second); err != nil {
		t.Fatal("did not reach control startup")
	}
	// Both ports must still be closed while StartControl waits on this process.
	for _, pnum := range []int{port, controlPort} {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", pnum), 100*time.Millisecond)
		if err == nil {
			_ = c.Close()
			t.Fatal("bound during blocked startup")
		}
	}
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	_ = p.wait(3 * time.Second)
	p.mu.Lock()
	waited := p.waited
	p.mu.Unlock()
	if !waited {
		t.Fatal("SIGTERM did not drain blocked startup")
	}
	state, err := storage.New(root).Load()
	if err != nil || state.Controller.Control.ServerGeneration != control.ServerGeneration {
		t.Fatal("cancelled startup rotated state")
	}
}
