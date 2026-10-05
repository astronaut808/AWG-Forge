package nodeagent

import (
	"bytes"
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
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/astronaut808/awg-forge/internal/controlpki"
	"github.com/astronaut808/awg-forge/internal/storage"
	"github.com/google/uuid"
)

func TestRenewalWorkerRetriesDurableCSRAndSwitchesMTLS(t *testing.T) {
	for _, failure := range []string{"lost-response", "not-due", "revoked"} {
		t.Run(failure, func(t *testing.T) {
			material, pin, err := controlpki.Generate(controlpki.Endpoint{BindIP: "127.0.0.1", Advertised: "127.0.0.1", Port: 9443}, time.Now().Add(-21*24*time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			serverCertificate, err := tls.X509KeyPair(material.ServerCert, material.ServerKey)
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
			old, err := controlpki.IssueNodeCertificate(material, csr, time.Now().Add(-21*24*time.Hour+time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			keyDER, err := x509.MarshalPKCS8PrivateKey(key)
			if err != nil {
				t.Fatal(err)
			}
			controllerID := uuid.NewString()
			roots := x509.NewCertPool()
			if !roots.AppendCertsFromPEM(material.CACert) {
				t.Fatal("test CA")
			}
			var renewals, issuedCount atomic.Int64
			var issuanceMu sync.Mutex
			var firstCSR []byte
			var issued controlapi.IssuedCertificate
			observed := make(chan string, 4)
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
					w.WriteHeader(403)
					return
				}
				if r.URL.Path == "/control/v1/node/presence" {
					select {
					case observed <- r.TLS.PeerCertificates[0].SerialNumber.String():
					default:
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(controlapi.PresenceAccepted{ControllerID: controllerID, SessionID: uuid.NewString(), SessionExpiresAt: time.Now().Add(time.Minute), NextPollSeconds: 1})
					return
				}
				if r.URL.Path != "/control/v1/node/certificate-renewals" {
					w.WriteHeader(404)
					return
				}
				issuanceMu.Lock()
				defer issuanceMu.Unlock()
				var request controlapi.CertificateRenewalRequest
				if json.NewDecoder(r.Body).Decode(&request) != nil || request.CurrentSerial != old.Serial {
					w.WriteHeader(400)
					return
				}
				block, _ := pem.Decode([]byte(request.CSRPEM))
				if block == nil {
					w.WriteHeader(400)
					return
				}
				attempt := renewals.Add(1)
				if attempt == 1 {
					firstCSR = append([]byte(nil), block.Bytes...)
				} else if !bytes.Equal(firstCSR, block.Bytes) {
					w.WriteHeader(409)
					return
				}
				if failure == "revoked" {
					w.WriteHeader(403)
					return
				}
				if failure == "not-due" && attempt == 1 {
					w.WriteHeader(409)
					_, _ = w.Write([]byte(`{"code":"renewal_not_due"}`))
					return
				}
				if issuedCount.Load() == 0 {
					cert, err := controlpki.IssueNodeCertificate(material, block.Bytes, time.Now())
					if err != nil {
						w.WriteHeader(500)
						return
					}
					issued = controlapi.IssuedCertificate{CertificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.DER})), ChainPEM: string(material.CACert), Serial: cert.Serial, NotBefore: cert.NotBefore, NotAfter: cert.NotAfter}
					issuedCount.Add(1)
				}
				if failure == "lost-response" && attempt == 1 {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err == nil {
						_ = conn.Close()
					}
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(issued)
			}))
			server.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{serverCertificate}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots}
			server.StartTLS()
			defer server.Close()
			dir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			cfg := config.Config{ConfigDir: dir, ServerHost: "127.0.0.1", ExternalInterface: "lo"}
			service := app.New(cfg)
			invitation := controlapi.Invitation{ControllerURL: server.URL, CAPin: pin, CACertPEM: string(material.CACert)}
			approved := controlapi.EnrollmentStatus{Status: "approved", ContractVersion: 1, NodeID: uuid.NewString(), ControllerID: controllerID, BindingEpoch: 1, Certificate: &controlapi.IssuedCertificate{CertificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: old.DER})), ChainPEM: string(material.CACert), Serial: old.Serial, NotBefore: old.NotBefore, NotAfter: old.NotAfter}}
			if err := service.InstallNodeEnrollment(context.Background(), invitation, approved, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})); err != nil {
				t.Fatal(err)
			}
			before, err := storage.New(dir).Load()
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := service.PrepareNodeCertificateRenewal(context.Background(), *before.NodeConnection)
			if err != nil {
				t.Fatal(err)
			}
			// A fresh application instance emulates loss of all process memory.
			service = app.New(cfg)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- Run(ctx, service, cfg) }()
			if failure == "revoked" {
				select {
				case err := <-done:
					if err == nil {
						t.Fatal("revocation ignored")
					}
				case <-ctx.Done():
					t.Fatal("revocation did not stop worker")
				}
				state, err := service.State()
				if err != nil || state.NodeConnection.CredentialGeneration != before.NodeConnection.CredentialGeneration {
					t.Fatal("revocation interrupted local state")
				}
				return
			}
			for {
				select {
				case serial := <-observed:
					if serial == old.Serial {
						continue
					}
					cancel()
					if err := <-done; err != nil {
						t.Fatal("worker cancellation failed")
					}
					issuanceMu.Lock()
					sameCSR := bytes.Equal(firstCSR, prepared.CSRDER)
					issuanceMu.Unlock()
					if renewals.Load() != 2 || issuedCount.Load() != 1 || !sameCSR {
						t.Fatal("retry changed CSR or issued another certificate")
					}
					after, err := storage.New(dir).Load()
					if err != nil || after.NodeConnection.CredentialGeneration != prepared.Generation || !reflect.DeepEqual(before.Tunnels, after.Tunnels) || before.SessionSecret != after.SessionSecret || after.ManagedNode.BootSequence != 1 {
						t.Fatal("renewal changed local configuration or allocated extra boot")
					}
					return
				case err := <-done:
					t.Fatalf("worker stopped before renewed presence: %v", err)
				case <-ctx.Done():
					t.Fatal("renewed presence not observed")
				}
			}
		})
	}
}
