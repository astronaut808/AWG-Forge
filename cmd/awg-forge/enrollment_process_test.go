package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/backup"
	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/astronaut808/awg-forge/internal/sqldb"
	"github.com/astronaut808/awg-forge/internal/storage"
	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"
)

// TestEnrollmentProcesses exercises the two independently started binaries.
// It deliberately keeps invitations and process logs in t.TempDir: neither is
// emitted on failure because both contain enrollment credentials.
func TestEnrollmentProcesses(t *testing.T) {
	runEnrollmentProcesses(t, "127.0.0.1")
}

func TestBootstrapEnrollmentProcesses(t *testing.T) {
	runEnrollmentProcessesWithBootstrap(t, "127.0.0.1", true)
}

func runEnrollmentProcesses(t *testing.T, bindIP string) {
	runEnrollmentProcessesWithBootstrap(t, bindIP, false)
}

func runEnrollmentProcessesWithBootstrap(t *testing.T, bindIP string, publicBootstrap bool) {
	if testing.Short() {
		t.Skip("process integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	binary := buildEnrollmentProcessBinary(ctx, t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	controllerDir := filepath.Join(root, "controller")
	nodeDir := filepath.Join(root, "node")
	browserPort, controlPort := enrollmentFreePort(t), enrollmentFreePort(t)
	cfg := enrollmentProcessConfig(controllerDir, browserPort)
	svc := app.New(cfg)
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("enrollment-process-totp"))
	now := time.Now().UTC()
	code, err := totp.GenerateCode(secret, now)
	if err != nil {
		t.Fatal(err)
	}
	activation, err := svc.ActivateController(ctx, app.ControllerActivationRequest{Username: "admin", Password: "correct horse battery staple", TOTPSecret: secret, TOTPConfirmation: code, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	controller := enrollmentStartProcess(ctx, t, binary, enrollmentEnv(controllerDir, browserPort), "serve")
	defer controller.stop(t)
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", browserPort)
	enrollmentWaitHTTP(ctx, t, baseURL+"/api/state")
	client := enrollmentBrowserClient(activation.Authentication.Token, baseURL)
	var prepared struct{}
	enrollmentJSON(ctx, t, client, http.MethodPost, baseURL+"/api/controller/control/prepare", map[string]any{"bind_ip": bindIP, "advertised": bindIP, "port": controlPort}, &prepared)
	preparedState, err := storage.New(controllerDir).Load()
	if err != nil {
		t.Fatal(err)
	}
	initialControl := *preparedState.Controller.Control
	// The disabled identity is a short, real-time fixture. Enable starts the
	// actual serving worker; enrollment and reconnect span its automatic renewal.
	clock := time.Now().UTC().Truncate(time.Second)
	processServerValidity(t, controllerDir, initialControl, clock.Add(-3*time.Second), clock.Add(27*time.Second))
	prepareBackup := func() string {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/controller/control/backup", bytes.NewBufferString(`{"password":"process backup password"}`))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, 32<<20))
		_ = response.Body.Close()
		if err != nil || response.StatusCode != http.StatusOK {
			t.Fatal("control backup failed")
		}
		archivePath := filepath.Join(root, "enable-controller.afbackup")
		if err := os.WriteFile(archivePath, body, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := backup.Verify(ctx, cfg, "process backup password", archivePath); err != nil {
			t.Fatal(err)
		}
		receipt := response.Header.Get("X-Control-Enable-Receipt")
		if receipt == "" {
			t.Fatal("control backup receipt missing")
		}
		return receipt
	}
	if bindIP != "127.0.0.1" {
		address := net.JoinHostPort(bindIP, fmt.Sprint(controlPort))
		assertDisabled := func() {
			state, err := storage.New(controllerDir).Load()
			if err != nil || state.Controller.Control.Enabled {
				t.Fatal("denial changed enablement")
			}
			listener, err := net.Listen("tcp", address)
			if err != nil {
				t.Fatal("listener opened without consent")
			}
			_ = listener.Close()
		}
		assertDisabled()
		for _, consent := range []any{nil, false} {
			receipt := prepareBackup()
			body := map[string]any{"receipt": receipt, "backup_saved": true}
			if consent != nil {
				body["allow_non_loopback"] = consent
			}
			if err := enrollmentJSONError(ctx, client, http.MethodPost, baseURL+"/api/controller/control/enable", body, &prepared); err == nil {
				t.Fatal("external listener enabled without consent")
			}
			assertDisabled()
			body["allow_non_loopback"] = true
			if err := enrollmentJSONError(ctx, client, http.MethodPost, baseURL+"/api/controller/control/enable", body, &prepared); err == nil {
				t.Fatal("denied receipt reused")
			}
		}
		// Both invalid consent forms reject before consuming the valid receipt.
		receipt := prepareBackup()
		for _, invalid := range []any{nil, "true"} {
			body, _ := json.Marshal(map[string]any{"receipt": receipt, "backup_saved": true, "allow_non_loopback": invalid})
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/controller/control/enable", bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.StatusCode != http.StatusBadRequest {
				t.Fatal("invalid consent accepted")
			}
		}
		assertDisabled()
	}
	receipt := prepareBackup()
	enrollmentJSON(ctx, t, client, http.MethodPost, baseURL+"/api/controller/control/enable", map[string]any{"receipt": receipt, "backup_saved": true, "allow_non_loopback": bindIP != "127.0.0.1"}, &prepared)
	var invitation controlapi.Invitation
	enrollmentJSON(ctx, t, client, http.MethodPost, baseURL+"/api/controller/enrollments/invite", map[string]any{}, &invitation)
	input, err := json.Marshal(invitation)
	if err != nil {
		t.Fatal(err)
	}
	inputPath := filepath.Join(root, "invitation.json")
	if err := os.WriteFile(inputPath, input, 0600); err != nil {
		t.Fatal(err)
	}
	nodeEnv := func(dir string, port int) []string {
		env := enrollmentEnv(dir, port)
		if publicBootstrap {
			env = append(env, "DATABASE_MODE=off", "DATABASE_PATH=")
		}
		return env
	}
	var node *enrollmentProcess
	if publicBootstrap {
		node = enrollmentStartProcessInput(ctx, t, binary, nodeEnv(nodeDir, enrollmentFreePort(t)), strings.NewReader(invitation.Secret+"\n"), "node", "enroll", "--controller-url", invitation.ControllerURL, "--ca-pin", invitation.CAPin, "--invitation-id", invitation.InvitationID, "--secret-fd", "0", "--name", "node")
	} else {
		node = enrollmentStartProcess(ctx, t, binary, nodeEnv(nodeDir, enrollmentFreePort(t)), "node", "enroll", "--input-file", inputPath, "--name", "node")
	}
	defer node.stop(t)
	var review app.EnrollmentReview
	deadline := time.Now().Add(20 * time.Second)
	for {
		err = enrollmentJSONError(ctx, client, http.MethodPost, baseURL+"/api/controller/enrollments/review", map[string]any{"invitation_id": invitation.InvitationID}, &review)
		if err == nil && review.Status == "pending" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pending enrollment was not observed")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := node.waitFor("Enrollment verification code: "+review.VerificationCode, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		fmt.Sprintf(`{"enrollment_id":%q,"verification_code":%q}`, review.EnrollmentID, review.VerificationCode),
		fmt.Sprintf(`{"enrollment_id":%q,"verification_code":%q,"approve":null}`, review.EnrollmentID, review.VerificationCode),
	} {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/controller/enrollments/decide", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("implicit decision status: %d", response.StatusCode)
		}
		enrollmentJSON(ctx, t, client, http.MethodPost, baseURL+"/api/controller/enrollments/review", map[string]any{"invitation_id": invitation.InvitationID}, &review)
		if review.Status != "pending" {
			t.Fatal("implicit decision rejected pending enrollment")
		}
	}
	enrollmentJSON(ctx, t, client, http.MethodPost, baseURL+"/api/controller/enrollments/decide", map[string]any{"enrollment_id": review.EnrollmentID, "verification_code": review.VerificationCode, "approve": true}, &prepared)
	if err := node.wait(20 * time.Second); err != nil {
		t.Fatal(err)
	}
	state, err := storage.New(nodeDir).Load()
	if err != nil || state.EffectiveMode() != config.ModeNode || state.ManagedNode == nil {
		t.Fatalf("node enrollment state unavailable: %v", err)
	}
	if len(state.Tunnels) != 0 {
		t.Fatal("fresh join created a default tunnel")
	}
	var onboarding app.EnrollmentOnboardingStatus
	enrollmentJSON(ctx, t, client, http.MethodPost, baseURL+"/api/controller/enrollments/status", map[string]any{"invitation_id": invitation.InvitationID}, &onboarding)
	if onboarding.Connected || onboarding.Status != "approved" {
		t.Fatal("certificate issuance counted as connected")
	}

	// A separate `serve` process owns the persistent boot sequence and sends
	// mTLS presence. Its first request proves the registry record is durable.
	nodePort := enrollmentFreePort(t)
	nodeServe := enrollmentStartProcess(ctx, t, binary, nodeEnv(nodeDir, nodePort), "serve")
	defer nodeServe.stop(t)
	firstSequence := enrollmentWaitPresence(ctx, t, cfg.DatabasePath, state.ManagedNode.NodeID)
	enrollmentJSON(ctx, t, client, http.MethodPost, baseURL+"/api/controller/enrollments/status", map[string]any{"invitation_id": invitation.InvitationID}, &onboarding)
	if !onboarding.Connected || onboarding.NodeID != state.ManagedNode.NodeID {
		t.Fatal("authenticated first presence did not confirm new enrollment")
	}
	nodeServe.stop(t)
	nodeServe = enrollmentStartProcess(ctx, t, binary, nodeEnv(nodeDir, nodePort), "serve")
	defer nodeServe.stop(t)
	secondSequence := enrollmentWaitPresenceAfter(ctx, t, cfg.DatabasePath, state.ManagedNode.NodeID, firstSequence)
	if secondSequence != firstSequence+1 {
		t.Fatalf("node boot sequence = %d, want %d", secondSequence, firstSequence+1)
	}

	controllerBeforeRenewal, err := storage.New(controllerDir).Load()
	if err != nil {
		t.Fatal(err)
	}
	beforeServerRenewal := enrollmentPresenceExpiry(ctx, t, cfg.DatabasePath, state.ManagedNode.NodeID)
	serverDeadline := time.Now().Add(35 * time.Second)
	for {
		current, err := storage.New(controllerDir).Load()
		if err != nil {
			t.Fatal(err)
		}
		if current.Controller.Control.ServerGeneration != initialControl.ServerGeneration {
			break
		}
		if time.Now().After(serverDeadline) {
			t.Fatal("live controller process did not renew server leaf")
		}
		time.Sleep(25 * time.Millisecond)
	}
	controllerAfterRenewal, err := storage.New(controllerDir).Load()
	if err != nil {
		t.Fatal(err)
	}
	controllerAfterRenewal.Controller.Control.ServerGeneration = controllerBeforeRenewal.Controller.Control.ServerGeneration
	if !reflect.DeepEqual(controllerBeforeRenewal, controllerAfterRenewal) {
		t.Fatal("live server renewal changed controller/local state")
	}
	enrollmentWaitPresenceRefresh(ctx, t, cfg.DatabasePath, state.ManagedNode.NodeID, beforeServerRenewal)
	if got := enrollmentWaitPresenceAfter(ctx, t, cfg.DatabasePath, state.ManagedNode.NodeID, secondSequence-1); got != secondSequence {
		t.Fatal("server renewal changed node boot identity")
	}

	// Controller restart must accept a still-running node with the same boot
	// identity; this is the durable equal-sequence refresh path.
	beforeRefresh := enrollmentPresenceExpiry(ctx, t, cfg.DatabasePath, state.ManagedNode.NodeID)
	controller.stop(t)
	controller = enrollmentStartProcess(ctx, t, binary, enrollmentEnv(controllerDir, browserPort), "serve")
	defer controller.stop(t)
	enrollmentWaitHTTP(ctx, t, baseURL+"/api/state")
	enrollmentWaitPresenceRefresh(ctx, t, cfg.DatabasePath, state.ManagedNode.NodeID, beforeRefresh)
	if got := enrollmentWaitPresenceAfter(ctx, t, cfg.DatabasePath, state.ManagedNode.NodeID, secondSequence-1); got != secondSequence {
		t.Fatalf("controller restart changed node boot sequence: %d", got)
	}
	archivePath := filepath.Join(root, "approved-controller.afbackup")
	if _, err := backup.WriteFile(ctx, cfg, app.New(cfg), "process backup password", archivePath); err != nil {
		t.Fatal(err)
	}

	// A pin failure is local: it must terminate before a claim and must not
	// create node state in its distinct configuration directory.
	wrong := invitation
	wrong.CAPin = strings.Repeat("0", len(wrong.CAPin))
	wrongBody, err := json.Marshal(wrong)
	if err != nil {
		t.Fatal(err)
	}
	wrongPath := filepath.Join(root, "wrong-pin.json")
	if err := os.WriteFile(wrongPath, wrongBody, 0600); err != nil {
		t.Fatal(err)
	}
	wrongDir := filepath.Join(root, "wrong-node")
	claimsBefore := enrollmentClaimCount(ctx, t, cfg.DatabasePath)
	wrongNode := enrollmentStartProcess(ctx, t, binary, enrollmentEnv(wrongDir, enrollmentFreePort(t)), "node", "enroll", "--input-file", wrongPath)
	if err := wrongNode.wait(15 * time.Second); err == nil {
		t.Fatal("wrong pin enrollment succeeded")
	}
	if _, err := os.Stat(storage.New(wrongDir).StatePath()); !os.IsNotExist(err) {
		t.Fatalf("wrong pin created node state: %v", err)
	}
	if got := enrollmentClaimCount(ctx, t, cfg.DatabasePath); got != claimsBefore {
		t.Fatalf("wrong pin created a claim: before=%d after=%d", claimsBefore, got)
	}

	// A revoked binding rejects the already issued mTLS certificate and causes
	// the live node worker to stop rather than retrying with stale authority.
	db, err := sqldb.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RevokeNodeBinding(ctx, sqldb.NodeIdentity{ControllerID: state.ManagedNode.ControllerID, NodeID: state.ManagedNode.NodeID, BindingEpoch: state.ManagedNode.BindingEpoch}, time.Now().UTC()); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	enrollmentJSON(ctx, t, client, http.MethodPost, baseURL+"/api/controller/enrollments/status", map[string]any{"invitation_id": invitation.InvitationID}, &onboarding)
	if onboarding.Connected {
		t.Fatal("revoked node remained connected in onboarding")
	}
	if status := enrollmentMTLSPresenceStatus(t, nodeDir, state); status != http.StatusForbidden {
		t.Fatalf("revoked node presence status = %d, want %d", status, http.StatusForbidden)
	}
	if err := nodeServe.waitFor("node.agent.unavailable", 25*time.Second); err != nil {
		t.Fatal("revoked node worker did not report stopped admission")
	}
	enrollmentWaitHTTP(ctx, t, fmt.Sprintf("http://127.0.0.1:%d/api/state", nodePort))
	nodeServe.stop(t)

	// Restoring a stale archive over the same stopped controller must retain
	// public history while forcing every authentication and node credential into
	// denial. The archive intentionally lives outside CONFIG_DIR.
	controller.stop(t)
	if err := backup.Restore(ctx, cfg, "process backup password", archivePath); err != nil {
		t.Fatal(err)
	}
	restored, err := app.New(cfg).State()
	if err != nil || restored.Controller == nil || restored.Controller.Control == nil || restored.Controller.Control.Enabled {
		t.Fatalf("restored controller enablement = %#v, %v", restored.Controller, err)
	}
	enrollmentAssertRestoreDenied(ctx, t, cfg.DatabasePath)
}

type enrollmentOutput struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *enrollmentOutput) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.buffer.Len() > 256<<10 {
		b.buffer.Reset()
	}
	return b.buffer.Write(value)
}
func (b *enrollmentOutput) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

type enrollmentProcess struct {
	cmd    *exec.Cmd
	output *enrollmentOutput
	done   chan error
	mu     sync.Mutex
	waited bool
	result error
}

func buildEnrollmentProcessBinary(ctx context.Context, t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "awg-forge")
	cmd := exec.CommandContext(ctx, "go", "build", "-o", path, ".")
	cmd.Dir = "."
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build process binary: %v: %s", err, output)
	}
	return path
}
func enrollmentProcessConfig(dir string, port int) config.Config {
	return config.Config{ConfigDir: dir, TunnelName: "awg0", ServerHost: "127.0.0.1", WebUIHost: "127.0.0.1", WebUIPort: port, ExternalInterface: "lo", IPv4Subnet: "10.8.0.0/24", DNS: "1.1.1.1", AllowedIPs: "0.0.0.0/0", ProtocolProfile: "awg_legacy_1_0", DatabaseMode: sqldb.ModeSQLite, DatabasePath: filepath.Join(dir, "awg-forge.db"), DatabaseBusyTimeout: time.Second, DatabaseQueryTimeout: 5 * time.Second, DatabaseMaxOpenConns: 1, DatabaseMaxIdleConns: 1}
}
func enrollmentEnv(dir string, port int) []string {
	return append(os.Environ(), "CONFIG_DIR="+dir, fmt.Sprintf("WEBUI_PORT=%d", port), "WEBUI_HOST=127.0.0.1", "DATABASE_MODE=sqlite", "DATABASE_PATH="+filepath.Join(dir, "awg-forge.db"), "APPLY_CONFIG=false", "AUDIT_LOG_ENABLED=false", "SESSION_COOKIE_SECURE=false")
}
func enrollmentStartProcess(ctx context.Context, t *testing.T, binary string, env []string, args ...string) *enrollmentProcess {
	return enrollmentStartProcessInput(ctx, t, binary, env, nil, args...)
}

func enrollmentStartProcessInput(ctx context.Context, t *testing.T, binary string, env []string, input io.Reader, args ...string) *enrollmentProcess {
	t.Helper()
	output := new(enrollmentOutput)
	logFile, err := os.CreateTemp(t.TempDir(), "process-*.log")
	if err != nil {
		t.Fatal(err)
	}
	if err := logFile.Chmod(0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, "./awg-forge", args...)
	cmd.Dir = filepath.Dir(binary)
	cmd.Env = env
	cmd.Stdin = input
	cmd.Stdout = io.MultiWriter(logFile, output)
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &enrollmentProcess{cmd: cmd, output: output, done: make(chan error, 1)}
	go func() { result := cmd.Wait(); _ = logFile.Close(); p.done <- result }()
	return p
}
func (p *enrollmentProcess) wait(timeout time.Duration) error {
	p.mu.Lock()
	if p.waited {
		err := p.result
		p.mu.Unlock()
		return err
	}
	p.mu.Unlock()
	select {
	case err := <-p.done:
		p.mu.Lock()
		p.waited, p.result = true, err
		p.mu.Unlock()
		return err
	case <-time.After(timeout):
		return fmt.Errorf("process timeout")
	}
}
func (p *enrollmentProcess) waitFor(text string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(p.output.String(), text) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("process did not emit verification code")
}
func (p *enrollmentProcess) stop(t *testing.T) {
	t.Helper()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Signal(os.Interrupt)
		if err := p.wait(5 * time.Second); err != nil {
			_ = p.cmd.Process.Kill()
			_ = p.wait(5 * time.Second)
		}
	}
}

func enrollmentWaitPresence(ctx context.Context, t *testing.T, path, nodeID string) uint64 {
	t.Helper()
	return enrollmentWaitPresenceAfter(ctx, t, path, nodeID, 0)
}

func enrollmentWaitPresenceAfter(ctx context.Context, t *testing.T, path, nodeID string, after uint64) uint64 {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		var sequence uint64
		err := db.QueryRowContext(ctx, "SELECT boot_sequence FROM control_node_presence WHERE node_id = ?", nodeID).Scan(&sequence)
		if err == nil && sequence > after {
			return sequence
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("node presence was not persisted")
	return 0
}

func enrollmentMTLSPresenceStatus(t *testing.T, nodeDir string, state config.State) int {
	t.Helper()
	identity, err := storage.New(nodeDir).LoadNodeIdentity(state.NodeConnection.CredentialGeneration)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := tls.X509KeyPair(identity.Certificate, identity.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(identity.CACert) {
		t.Fatal("node CA is invalid")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{certificate}}, Proxy: nil}
	defer transport.CloseIdleConnections()
	body, err := json.Marshal(controlapi.Presence{BootID: uuid.NewString(), BootSequence: state.ManagedNode.BootSequence, ApplicationVersion: "process-test", ContractVersion: 1, StateEpoch: state.ManagedNode.StateEpoch, BindingEpoch: state.ManagedNode.BindingEpoch, DesiredGeneration: state.ManagedNode.DesiredGeneration, Capabilities: []string{"presence"}, ObservedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPut, state.NodeConnection.ControllerURL+"/control/v1/node/presence", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Transport: transport, Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	return response.StatusCode
}

func enrollmentAssertRestoreDenied(ctx context.Context, t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var credentials int
	err = db.QueryRowContext(ctx, `SELECT
(SELECT count(*) FROM controller_users WHERE disabled_at = '') +
(SELECT count(*) FROM control_enrollment_invitations) +
(SELECT count(*) FROM control_enrollments) +
(SELECT count(*) FROM control_node_certificates WHERE revoked_at_unix_ms IS NULL)`).Scan(&credentials)
	if err != nil || credentials != 0 {
		t.Fatalf("restored controller retained authority: count=%d error=%v", credentials, err)
	}
}

func enrollmentClaimCount(ctx context.Context, t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM control_enrollments").Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
func enrollmentFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}
func enrollmentBrowserClient(token, origin string) *http.Client {
	return &http.Client{Transport: enrollmentTransport{token: token, origin: origin}}
}

type enrollmentTransport struct{ token, origin string }

func (t enrollmentTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("Origin", t.origin)
	r.Header.Set("Cookie", "awg_forge_session="+t.token)
	return http.DefaultTransport.RoundTrip(r)
}
func enrollmentWaitHTTP(ctx context.Context, t *testing.T, url string) {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		r, e := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if e == nil {
			res, e := http.DefaultClient.Do(r)
			if e == nil {
				_ = res.Body.Close()
				if res.StatusCode < 500 {
					return
				}
			}
		}
	}
	t.Fatal("browser server did not start")
}
func enrollmentJSON(ctx context.Context, t *testing.T, c *http.Client, method, url string, in, out any) {
	t.Helper()
	if err := enrollmentJSONError(ctx, c, method, url, in, out); err != nil {
		t.Fatal(err)
	}
}
func enrollmentJSONError(ctx context.Context, c *http.Client, method, url string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	r, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json")
	res, err := c.Do(r)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", res.StatusCode)
	}
	return json.NewDecoder(res.Body).Decode(out)
}

func enrollmentPresenceExpiry(ctx context.Context, t *testing.T, path, nodeID string) int64 {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var expires int64
	if err := db.QueryRowContext(ctx, "SELECT expires_at_unix_ms FROM control_node_presence WHERE node_id = ?", nodeID).Scan(&expires); err != nil {
		t.Fatal(err)
	}
	return expires
}
func enrollmentWaitPresenceRefresh(ctx context.Context, t *testing.T, path, nodeID string, previous int64) {
	t.Helper()
	deadline := time.Now().Add(35 * time.Second)
	for time.Now().Before(deadline) {
		if enrollmentPresenceExpiry(ctx, t, path, nodeID) > previous {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("node did not refresh presence after controller restart")
}
