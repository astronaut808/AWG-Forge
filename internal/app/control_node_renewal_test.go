package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/astronaut808/awg-forge/internal/controlpki"
	"github.com/astronaut808/awg-forge/internal/controlserver"
	"github.com/astronaut808/awg-forge/internal/sqldb"
	"github.com/astronaut808/awg-forge/internal/storage"
	"github.com/google/uuid"
)

func TestNodeCertificateRenewalRouteEnabledMTLS(t *testing.T) {
	f := newControlLifecycleFixture(t)
	ctx := context.Background()
	if err := f.service.EnableControl(ctx, f.token, f.receipt(t), true); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.service.ShutdownControl() }()
	base := "https://" + f.address
	request := func(client *http.Client, value any, want int, output *controlapi.IssuedCertificate) string {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Post(base+"/control/v1/node/certificate-renewals", "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != want {
			t.Fatalf("renew status = %d, want %d", response.StatusCode, want)
		}
		if output != nil {
			if err := json.NewDecoder(response.Body).Decode(output); err != nil {
				t.Fatal("decode renewal response")
			}
			return ""
		}
		var problem struct {
			Code string `json:"code"`
		}
		_ = json.NewDecoder(response.Body).Decode(&problem)
		return problem.Code
	}
	csr, key := renewalTestCSR(t)
	valid := controlapi.CertificateRenewalRequest{BootID: uuid.NewString(), CurrentSerial: renewalPeerSerial(t, f), CSRPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr}))}
	request(f.client, controlapi.CertificateRenewalRequest{BootID: valid.BootID, CurrentSerial: "0", CSRPEM: valid.CSRPEM}, http.StatusBadRequest, nil)
	request(f.client, controlapi.CertificateRenewalRequest{BootID: "not-a-uuid", CurrentSerial: valid.CurrentSerial, CSRPEM: valid.CSRPEM}, http.StatusBadRequest, nil)
	request(f.client, controlapi.CertificateRenewalRequest{BootID: valid.BootID, CurrentSerial: valid.CurrentSerial, CSRPEM: "bad"}, http.StatusBadRequest, nil)
	sameKeyCSR, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, f.tls.Certificates[0].PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	request(f.client, controlapi.CertificateRenewalRequest{BootID: valid.BootID, CurrentSerial: valid.CurrentSerial, CSRPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: sameKeyCSR}))}, http.StatusBadRequest, nil)
	request(f.client, map[string]any{"boot_id": valid.BootID, "current_serial": valid.CurrentSerial, "csr_pem": valid.CSRPEM, "extra": true}, http.StatusBadRequest, nil)
	request(f.client, controlapi.CertificateRenewalRequest{BootID: valid.BootID, CurrentSerial: valid.CurrentSerial, CSRPEM: string(bytes.Repeat([]byte("x"), 17<<10))}, http.StatusBadRequest, nil)
	var issued controlapi.IssuedCertificate
	request(f.client, valid, http.StatusOK, &issued)
	if issued.CertificatePEM == "" || issued.ChainPEM == "" || issued.Serial == "" {
		t.Fatal("incomplete issued certificate")
	}
	var retry controlapi.IssuedCertificate
	request(f.client, valid, http.StatusOK, &retry)
	if retry.CertificatePEM != issued.CertificatePEM {
		t.Fatal("exact retry changed certificate")
	}
	other, _ := renewalTestCSR(t)
	conflict := valid
	conflict.CSRPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: other}))
	request(f.client, conflict, http.StatusConflict, nil)
	newCertificate := renewalTestCertificate(t, []byte(issued.CertificatePEM))
	newClient := &http.Client{Transport: &http.Transport{TLSClientConfig: f.tls.Clone()}, Timeout: 2 * time.Second}
	newClient.Transport.(*http.Transport).TLSClientConfig.Certificates = []tls.Certificate{{Certificate: [][]byte{newCertificate.Raw}, PrivateKey: key}}
	defer newClient.CloseIdleConnections()
	earlyCSR, _ := renewalTestCSR(t)
	early := controlapi.CertificateRenewalRequest{BootID: uuid.NewString(), CurrentSerial: newCertificate.SerialNumber.String(), CSRPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: earlyCSR}))}
	if code := request(newClient, early, http.StatusConflict, nil); code != "renewal_not_due" {
		t.Fatalf("early renewal code = %q", code)
	}
	if err := f.service.store.SavePendingDesiredStateCommit(storage.PendingDesiredStateCommit{OperationID: "pending"}); err != nil {
		t.Fatal(err)
	}
	request(f.client, valid, http.StatusServiceUnavailable, nil)
	if err := f.service.store.DeletePendingDesiredStateCommit(); err != nil {
		t.Fatal(err)
	}
	state, err := f.service.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.store.BeginRestorePending(state.Controller.ControllerID); err != nil {
		t.Fatal(err)
	}
	request(f.client, valid, http.StatusForbidden, nil)
	if err := f.service.store.ClearRestorePending(); err != nil {
		t.Fatal(err)
	}
	db, err := sqldb.Open(ctx, f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.RevokeNodeBinding(ctx, sqldb.NodeIdentity{ControllerID: state.Controller.ControllerID, NodeID: "11111111-1111-4111-8111-111111111111", BindingEpoch: 1}, time.Now()); err != nil {
		t.Fatal(err)
	}
	request(f.client, valid, http.StatusForbidden, nil)
}

func renewalPeerSerial(t *testing.T, f controlLifecycleFixture) string {
	t.Helper()
	cert := f.tls.Certificates[0].Certificate[0]
	parsed, err := x509.ParseCertificate(cert)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.SerialNumber.String()
}

func TestInternalNodeRenewalRealLoopbackMTLS(t *testing.T) {
	ctx := context.Background()
	cfg := controllerTestConfig(t)
	service := newFastControllerTestService(cfg)
	if _, err := service.Init(); err != nil {
		t.Fatal(err)
	}
	issuedAt := time.Now().UTC().Add(-20 * 24 * time.Hour)
	if _, err := service.ActivateController(ctx, controllerTestActivationRequest(t, issuedAt)); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback unavailable: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	control, err := service.PrepareControlIdentity(ctx, ControlIdentityRequest{BindIP: "127.0.0.1", Advertised: "127.0.0.1", Port: port, Now: issuedAt})
	if err != nil {
		t.Fatal(err)
	}
	const nodeID = "11111111-1111-4111-8111-111111111111"
	oldCSR, oldKey := renewalTestCSR(t)
	oldPEM, err := service.issueInitialNodeCertificate(ctx, nodeID, oldCSR, issuedAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	old := renewalTestCertificate(t, oldPEM)
	material, err := storage.New(cfg.ConfigDir).LoadControlIdentity(control.CAGeneration, control.ServerGeneration)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sqldb.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var clock atomic.Int64
	clock.Store(time.Now().UTC().UnixNano())
	readClock := func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	authorizer, err := NewControlNodeAuthorizer(db, controllerIDFromStore(t, cfg.ConfigDir), control, material, readClock)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := controlpki.Endpoint{BindIP: control.BindIP, Advertised: control.Advertised, Port: control.Port}
	runtime, err := controlserver.New(material, endpoint, control.CAPin, authorizer, []controlserver.Route{
		{ID: "node.renew", Method: http.MethodPost, Path: "/control/v1/node/certificate-renewals", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity, ok := controlserver.IdentityFromContext(r.Context())
			if !ok || identity.NodeID != nodeID {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			csr, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "invalid", http.StatusBadRequest)
				return
			}
			issued, err := service.renewNodeCertificate(r.Context(), r.TLS.PeerCertificates[0], csr, readClock())
			if err != nil {
				if errors.Is(err, sqldb.ErrNodeCertificateConflict) {
					http.Error(w, "conflict", http.StatusConflict)
					return
				}
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(struct {
				CertificatePEM string `json:"certificate_pem"`
			}{CertificatePEM: string(issued)})
		})},
		{ID: "node.ping", Method: http.MethodGet, Path: "/control/v1/node-ping", Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })},
	})
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
		t.Fatal("CA")
	}
	clientFor := func(cert *x509.Certificate, key ed25519.PrivateKey) *http.Client {
		return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: control.Advertised,
			Certificates: []tls.Certificate{{Certificate: [][]byte{cert.Raw}, PrivateKey: key}}}}, Timeout: 5 * time.Second}
	}
	oldClient := clientFor(old, oldKey)
	base := "https://" + net.JoinHostPort(control.BindIP, strconv.Itoa(control.Port))
	ping := func(client *http.Client, want int, reused *bool) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, base+"/control/v1/node-ping", nil)
		if err != nil {
			t.Fatal(err)
		}
		if reused != nil {
			req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { *reused = info.Reused }}))
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, response.Body); err != nil {
			_ = response.Body.Close()
			t.Fatal(err)
		}
		if err := response.Body.Close(); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != want {
			t.Fatalf("ping status = %d, want %d", response.StatusCode, want)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		response, err := oldClient.Get(base + "/control/v1/node-ping")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode != http.StatusNoContent {
				t.Fatalf("initial ping = %d", response.StatusCode)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	newCSR, newKey := renewalTestCSR(t)
	renew := func(client *http.Client, csr []byte, want int) []byte {
		t.Helper()
		response, err := client.Post(base+"/control/v1/node/certificate-renewals", "application/octet-stream", bytes.NewReader(csr))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != want {
			t.Fatalf("renew status = %d, want %d", response.StatusCode, want)
		}
		if want != http.StatusOK {
			return nil
		}
		var body struct {
			CertificatePEM string `json:"certificate_pem"`
		}
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return []byte(body.CertificatePEM)
	}
	newPEM := renew(oldClient, newCSR, http.StatusOK)
	if retry := renew(oldClient, newCSR, http.StatusOK); !bytes.Equal(retry, newPEM) {
		t.Fatal("exact mTLS retry changed certificate")
	}
	sameKeyCSR := renewalAlternateCSR(t, newCSR, newKey)
	if _, _, err := controlpki.ParseNodeCSR(sameKeyCSR); err != nil {
		t.Fatalf("same-key alternate CSR: %v", err)
	}
	renew(oldClient, sameKeyCSR, http.StatusConflict)
	renew(oldClient, func() []byte { csr, _ := renewalTestCSR(t); return csr }(), http.StatusConflict)
	newCert := renewalTestCertificate(t, newPEM)
	newClient := clientFor(newCert, newKey)
	ping(newClient, http.StatusNoContent, nil)
	caBlock, _ := pem.Decode(material.CACert)
	keyBlock, _ := pem.Decode(material.CAKey)
	if caBlock == nil || keyBlock == nil {
		t.Fatal("control CA")
	}
	ca, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	caKey, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	forgedPublic, forgedKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	forgedDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: old.SerialNumber, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}, ca, forgedPublic, caKey)
	if err != nil {
		t.Fatal(err)
	}
	forged, err := x509.ParseCertificate(forgedDER)
	if err != nil {
		t.Fatal(err)
	}
	ping(clientFor(forged, forgedKey), http.StatusForbidden, nil)
	otherCA, _, err := controlpki.Generate(endpoint, issuedAt)
	if err != nil {
		t.Fatal(err)
	}
	otherCSR, otherKey := renewalTestCSR(t)
	otherIssued, err := controlpki.IssueNodeCertificate(otherCA, otherCSR, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	otherCert, err := x509.ParseCertificate(otherIssued.DER)
	if err != nil {
		t.Fatal(err)
	}
	if response, err := clientFor(otherCert, otherKey).Get(base + "/control/v1/node-ping"); err == nil {
		_ = response.Body.Close()
		t.Fatal("wrong CA client authenticated")
	}
	cutoff := readClock().Add(24 * time.Hour)
	if old.NotAfter.Before(cutoff) {
		cutoff = old.NotAfter
	}
	clock.Store(cutoff.Add(-time.Millisecond).UnixNano())
	ping(oldClient, http.StatusNoContent, nil)
	clock.Store(cutoff.UnixNano())
	reused := false
	ping(oldClient, http.StatusForbidden, &reused)
	if !reused {
		t.Fatal("old certificate cutoff did not use the existing connection")
	}
	ping(newClient, http.StatusNoContent, nil)
	clock.Store(time.Now().UTC().UnixNano())
	if err := db.RevokeNodeBinding(ctx, sqldb.NodeIdentity{ControllerID: controllerIDFromStore(t, cfg.ConfigDir), NodeID: nodeID, BindingEpoch: 1}, readClock()); err != nil {
		t.Fatal(err)
	}
	ping(newClient, http.StatusForbidden, nil)
	ping(oldClient, http.StatusForbidden, nil)
	rebindCSR, rebindKey := renewalTestCSR(t)
	reboundPEM, err := service.rebindRevokedNodeCertificate(ctx, nodeID, 1, rebindCSR, readClock())
	if err != nil {
		t.Fatal(err)
	}
	if retry, err := service.rebindRevokedNodeCertificate(ctx, nodeID, 1, rebindCSR, readClock()); err != nil || !bytes.Equal(retry, reboundPEM) {
		t.Fatalf("rebind retry: %v", err)
	}
	reboundClient := clientFor(renewalTestCertificate(t, reboundPEM), rebindKey)
	ping(reboundClient, http.StatusNoContent, nil)
	reused = false
	ping(newClient, http.StatusForbidden, &reused)
	if !reused {
		t.Fatal("old binding was not fenced on its existing connection")
	}
	if err := db.RevokeNodeBinding(ctx, sqldb.NodeIdentity{ControllerID: controllerIDFromStore(t, cfg.ConfigDir), NodeID: nodeID, BindingEpoch: 2}, readClock()); err != nil {
		t.Fatal(err)
	}
	ping(reboundClient, http.StatusForbidden, nil)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	ping(reboundClient, http.StatusForbidden, nil)
}

func renewalTestCSR(t *testing.T) ([]byte, ed25519.PrivateKey) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	return csr, key
}

func renewalAlternateCSR(t *testing.T, original []byte, key ed25519.PrivateKey) []byte {
	t.Helper()
	var request struct {
		Info      asn1.RawValue
		Algorithm pkix.AlgorithmIdentifier
		Signature asn1.BitString
	}
	if _, err := asn1.Unmarshal(original, &request); err != nil {
		t.Fatal(err)
	}
	var info struct {
		Version    int
		Subject    asn1.RawValue
		PublicKey  asn1.RawValue
		Attributes asn1.RawValue
	}
	if _, err := asn1.Unmarshal(request.Info.FullBytes, &info); err != nil {
		t.Fatal(err)
	}
	valueDER, err := asn1.Marshal("different")
	if err != nil {
		t.Fatal(err)
	}
	attributeDER, err := asn1.Marshal(struct {
		Type   asn1.ObjectIdentifier
		Values asn1.RawValue
	}{Type: asn1.ObjectIdentifier{1, 2, 3, 4}, Values: asn1.RawValue{Tag: asn1.TagSet, IsCompound: true, Bytes: valueDER}})
	if err != nil {
		t.Fatal(err)
	}
	info.Attributes = asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: attributeDER}
	infoDER, err := asn1.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	request.Info = asn1.RawValue{FullBytes: infoDER}
	signature := ed25519.Sign(key, infoDER)
	request.Signature = asn1.BitString{Bytes: signature, BitLength: len(signature) * 8}
	alternate, err := asn1.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return alternate
}

func renewalTestCertificate(t *testing.T, data []byte) *x509.Certificate {
	t.Helper()
	block, rest := pem.Decode(data)
	if block == nil || len(rest) != 0 {
		t.Fatal("invalid certificate PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func TestInternalNodeRenewalFailsClosed(t *testing.T) {
	ctx := context.Background()
	cfg := controllerTestConfig(t)
	service := newFastControllerTestService(cfg)
	if _, err := service.Init(); err != nil {
		t.Fatal(err)
	}
	issuedAt := time.Now().UTC().Add(-20 * 24 * time.Hour)
	if _, err := service.ActivateController(ctx, controllerTestActivationRequest(t, issuedAt)); err != nil {
		t.Fatal(err)
	}
	control, err := service.PrepareControlIdentity(ctx, ControlIdentityRequest{BindIP: "127.0.0.1", Advertised: "127.0.0.1", Port: 18443, Now: issuedAt})
	if err != nil {
		t.Fatal(err)
	}
	oldCSR, _ := renewalTestCSR(t)
	oldPEM, err := service.issueInitialNodeCertificate(ctx, "11111111-1111-4111-8111-111111111111", oldCSR, issuedAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	old := renewalTestCertificate(t, oldPEM)
	csr, _ := renewalTestCSR(t)
	at := time.Now().UTC()
	denied := func(label string, candidate *x509.Certificate, request []byte, caller *Service, at time.Time) {
		t.Helper()
		if result, err := caller.renewNodeCertificate(ctx, candidate, request, at); err == nil || len(result) != 0 {
			t.Fatalf("%s renewed", label)
		}
	}
	denied("nil predecessor", nil, csr, service, at)
	denied("invalid CSR", old, []byte("invalid"), service, at)
	denied("same key", old, oldCSR, service, at)
	denied("before window", old, csr, service, old.NotBefore.Add(old.NotAfter.Sub(old.NotBefore)*2/3).Add(-time.Millisecond))
	denied("expired source", old, csr, service, old.NotAfter)
	store := storage.New(cfg.ConfigDir)
	controllerID := controllerIDFromStore(t, cfg.ConfigDir)
	if err := store.BeginRestorePending(controllerID); err != nil {
		t.Fatal(err)
	}
	denied("restore marker", old, csr, service, at)
	if err := store.ClearRestorePending(); err != nil {
		t.Fatal(err)
	}
	journal := storage.ControlIdentityJournal{ControllerID: controllerID, CAGeneration: control.CAGeneration, ServerGeneration: control.ServerGeneration, StartedAt: at}
	if err := store.SaveControlIdentityJournal(journal); err != nil {
		t.Fatal(err)
	}
	denied("identity journal", old, csr, service, at)
	if err := store.DeleteControlIdentityJournal(); err != nil {
		t.Fatal(err)
	}
	paths, err := storage.ControlIdentityRelativePaths(control.CAGeneration, control.ServerGeneration)
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(cfg.ConfigDir, paths[1])
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(caPath); err != nil {
		t.Fatal(err)
	}
	denied("missing CA", old, csr, service, at)
	if err := os.WriteFile(caPath, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	denied("corrupt CA", old, csr, service, at)
	if err := os.WriteFile(caPath, caPEM, 0600); err != nil {
		t.Fatal(err)
	}
	dbOff := cfg
	dbOff.DatabaseMode = sqldb.ModeOff
	denied("DB off", old, csr, newFastControllerTestService(dbOff), at)
	missingDB := cfg
	missingDB.DatabasePath = filepath.Join(t.TempDir(), "missing.db")
	denied("missing DB", old, csr, newFastControllerTestService(missingDB), at)
	db, err := sqldb.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ResetControllerAuth(ctx); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	denied("uninitialized auth", old, csr, service, at)
}

func TestInternalNodeRebindFailsClosed(t *testing.T) {
	ctx := context.Background()
	cfg := controllerTestConfig(t)
	service := newFastControllerTestService(cfg)
	if _, err := service.Init(); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	if _, err := service.ActivateController(ctx, controllerTestActivationRequest(t, at)); err != nil {
		t.Fatal(err)
	}
	control, err := service.PrepareControlIdentity(ctx, ControlIdentityRequest{
		BindIP: "127.0.0.1", Advertised: "127.0.0.1", Port: 18443, Now: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	const nodeID = "11111111-1111-4111-8111-111111111111"
	initialCSR, _ := renewalTestCSR(t)
	if _, err := service.issueInitialNodeCertificate(ctx, nodeID, initialCSR, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	csr, _ := renewalTestCSR(t)
	denied := func(label string, caller *Service) {
		t.Helper()
		if certificate, err := caller.rebindRevokedNodeCertificate(ctx, nodeID, 1, csr, at.Add(2*time.Second)); err == nil || len(certificate) != 0 {
			t.Fatalf("%s allowed rebind", label)
		}
	}
	denied("active binding", service)
	db, err := sqldb.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	controllerID := controllerIDFromStore(t, cfg.ConfigDir)
	if err := db.RevokeNodeBinding(ctx, sqldb.NodeIdentity{ControllerID: controllerID, NodeID: nodeID, BindingEpoch: 1}, at.Add(time.Second)); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store := storage.New(cfg.ConfigDir)
	if err := store.BeginRestorePending(controllerID); err != nil {
		t.Fatal(err)
	}
	denied("restore marker", service)
	if err := store.ClearRestorePending(); err != nil {
		t.Fatal(err)
	}
	journal := storage.ControlIdentityJournal{ControllerID: controllerID,
		CAGeneration: control.CAGeneration, ServerGeneration: control.ServerGeneration, StartedAt: at}
	if err := store.SaveControlIdentityJournal(journal); err != nil {
		t.Fatal(err)
	}
	denied("identity journal", service)
	if err := store.DeleteControlIdentityJournal(); err != nil {
		t.Fatal(err)
	}
	dbOff := cfg
	dbOff.DatabaseMode = sqldb.ModeOff
	denied("DB off", newFastControllerTestService(dbOff))
	missingDB := cfg
	missingDB.DatabasePath = filepath.Join(t.TempDir(), "missing.db")
	denied("missing DB", newFastControllerTestService(missingDB))
	paths, err := storage.ControlIdentityRelativePaths(control.CAGeneration, control.ServerGeneration)
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(cfg.ConfigDir, paths[1])
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(caPath, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	denied("corrupt CA", service)
	if err := os.WriteFile(caPath, caPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.rebindRevokedNodeCertificate(ctx, nodeID, 1, csr, at.Add(2*time.Second)); err != nil {
		t.Fatalf("valid rebind after failures: %v", err)
	}
	db, err = sqldb.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RevokeNodeBinding(ctx, sqldb.NodeIdentity{ControllerID: controllerID, NodeID: nodeID, BindingEpoch: 2}, at.Add(3*time.Second)); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.ResetControllerAuth(ctx); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	denied("uninitialized auth", service)
}

func TestInternalNodeRenewalRetryAfterLostResponseNearCAExpiry(t *testing.T) {
	ctx := context.Background()
	cfg := controllerTestConfig(t)
	service := newFastControllerTestService(cfg)
	if _, err := service.Init(); err != nil {
		t.Fatal(err)
	}
	createdAt := time.Now().UTC().AddDate(-5, 0, 0).Add(26 * time.Hour)
	if _, err := service.ActivateController(ctx, controllerTestActivationRequest(t, createdAt)); err != nil {
		t.Fatal(err)
	}
	control, err := service.PrepareControlIdentity(ctx, ControlIdentityRequest{BindIP: "127.0.0.1", Advertised: "127.0.0.1", Port: 18443, Now: createdAt})
	if err != nil {
		t.Fatal(err)
	}
	store := storage.New(cfg.ConfigDir)
	material, err := store.LoadControlIdentity(control.CAGeneration, control.ServerGeneration)
	if err != nil {
		t.Fatal(err)
	}
	caBlock, _ := pem.Decode(material.CACert)
	keyBlock, _ := pem.Decode(material.CAKey)
	if caBlock == nil || keyBlock == nil {
		t.Fatal("CA material")
	}
	ca, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	parsedKey, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	caKey, ok := parsedKey.(ed25519.PrivateKey)
	if !ok {
		t.Fatal("CA key type")
	}
	serverPublic, serverKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serverSerial := big.NewInt(1)
	if serverSerial.Cmp(ca.SerialNumber) == 0 {
		serverSerial = big.NewInt(2)
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: serverSerial, NotBefore: ca.NotAfter.Add(-30 * 24 * time.Hour), NotAfter: ca.NotAfter.Add(-time.Second),
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}, ca, serverPublic, caKey)
	if err != nil {
		t.Fatal(err)
	}
	serverKeyDER, err := x509.MarshalPKCS8PrivateKey(serverKey)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := storage.ControlIdentityRelativePaths(control.CAGeneration, control.ServerGeneration)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.ConfigDir, paths[2]), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: serverKeyDER}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.ConfigDir, paths[3]), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER}), 0600); err != nil {
		t.Fatal(err)
	}
	initialAt := ca.NotAfter.Add(-4 * 24 * time.Hour)
	oldCSR, _ := renewalTestCSR(t)
	oldPEM, err := service.issueInitialNodeCertificate(ctx, "11111111-1111-4111-8111-111111111111", oldCSR, initialAt)
	if err != nil {
		t.Fatal(err)
	}
	old := renewalTestCertificate(t, oldPEM)
	csr, _ := renewalTestCSR(t)
	firstAt := ca.NotAfter.Add(-25 * time.Hour)
	issuedPEM, err := service.renewNodeCertificate(ctx, old, csr, firstAt)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate losing the committed response, then retry after the CA's
	// issuance guard starts refusing new signatures.
	retryAt := ca.NotAfter.Add(-23 * time.Hour)
	if _, err := controlpki.IssueNodeCertificate(material, csr, retryAt); err == nil {
		t.Fatal("CA unexpectedly signed")
	}
	recoveredPEM, err := newFastControllerTestService(cfg).renewNodeCertificate(ctx, old, csr, retryAt)
	if err != nil || !bytes.Equal(recoveredPEM, issuedPEM) {
		t.Fatalf("lost-response retry = %v", err)
	}
	// The same recovery guarantee applies to a rebind committed before the
	// CA stops signing. Use a second node so the renewal remains independent.
	const reboundNodeID = "33333333-3333-4333-8333-333333333333"
	initialCSR, _ := renewalTestCSR(t)
	if _, err := service.issueInitialNodeCertificate(ctx, reboundNodeID, initialCSR, initialAt); err != nil {
		t.Fatal(err)
	}
	db, err := sqldb.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RevokeNodeBinding(ctx, sqldb.NodeIdentity{
		ControllerID: controllerIDFromStore(t, cfg.ConfigDir), NodeID: reboundNodeID, BindingEpoch: 1,
	}, firstAt); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	rebindCSR, _ := renewalTestCSR(t)
	reboundPEM, err := service.rebindRevokedNodeCertificate(ctx, reboundNodeID, 1, rebindCSR, firstAt)
	if err != nil {
		t.Fatal(err)
	}
	recoveredRebindPEM, err := newFastControllerTestService(cfg).rebindRevokedNodeCertificate(ctx, reboundNodeID, 1, rebindCSR, retryAt)
	if err != nil || !bytes.Equal(recoveredRebindPEM, reboundPEM) {
		t.Fatalf("lost rebind response retry = %v", err)
	}
}
