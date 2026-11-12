package app

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/astronaut808/awg-forge/internal/controlauth"
	"github.com/astronaut808/awg-forge/internal/render"
	"github.com/google/uuid"
)

func TestClientConfigExportPureNodeRenderAndFences(t *testing.T) {
	svc, _, tunnel, client := managedClientOperationService(t)
	state, err := svc.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	connection := config.NodeConnectionState{ControllerURL: "https://127.0.0.1:9443", CAPin: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", CredentialGeneration: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	state.NodeConnection = &connection
	if svc.store.Save(state) != nil {
		t.Fatal("fixture save")
	}
	tunnel, client, _ = findClient(state, client.ID)
	d := controlapi.ClientConfigExportDelivery{ClientConfigExportLocator: controlapi.ClientConfigExportLocator{ExportID: uuid.NewString()}, Request: controlapi.ClientConfigExportRequest{StateEpoch: state.ManagedNode.StateEpoch, BindingEpoch: 1, Generation: state.ManagedNode.DesiredGeneration, TunnelID: tunnel.ID, ClientID: client.ID, TunnelRevision: tunnel.ConfigRevision}, Authority: controlapi.OperationPoll{SessionID: uuid.NewString(), BootID: svc.managedBoot.BootID, BootSequence: svc.managedBoot.BootSequence, StateEpoch: svc.managedBoot.StateEpoch, BindingEpoch: 1, Generation: state.ManagedNode.DesiredGeneration}, ExpiresAt: time.Now().Add(50 * time.Second)}
	before, err := os.ReadFile(filepath.Join(svc.cfg.ConfigDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	svc.saveState = func(config.State) error { calls++; return errors.New("unexpected write") }
	svc.runtimeOps.applyTunnel = func(config.Tunnel) error { calls++; return errors.New("unexpected runtime apply") }
	expected, err := render.ClientConfig(state, tunnel, client)
	if err != nil {
		t.Fatal(err)
	}
	value, err := svc.RenderNodeClientConfig(context.Background(), connection, d)
	if err != nil || string(value) != expected {
		t.Fatal("pure renderer rejected valid input or changed config", err)
	}
	clear(value)
	after, err := os.ReadFile(filepath.Join(svc.cfg.ConfigDir, "state.json"))
	if err != nil || !bytes.Equal(before, after) || calls != 0 {
		t.Fatal("export mutated state/runtime")
	}
	for _, field := range []string{"epoch", "binding", "generation", "revision", "association", "client", "boot", "boot-sequence", "deadline", "connection"} {
		bad := d
		conn := connection
		switch field {
		case "epoch":
			bad.Request.StateEpoch = uuid.NewString()
			bad.Authority.StateEpoch = bad.Request.StateEpoch
		case "binding":
			bad.Request.BindingEpoch++
			bad.Authority.BindingEpoch++
		case "generation":
			bad.Request.Generation++
			bad.Authority.Generation++
		case "revision":
			bad.Request.TunnelRevision++
		case "association":
			bad.Request.TunnelID = "other"
		case "client":
			bad.Request.ClientID = "missing"
		case "boot":
			bad.Authority.BootID = uuid.NewString()
		case "boot-sequence":
			bad.Authority.BootSequence++
		case "deadline":
			bad.ExpiresAt = time.Now().Add(-time.Second)
		case "connection":
			conn.CredentialGeneration = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		}
		if value, err := svc.RenderNodeClientConfig(context.Background(), conn, bad); err == nil || len(value) != 0 {
			t.Fatal("node fence", field)
		}
	}
	next := New(svc.cfg)
	if _, err := next.StartManagedNodeBoot(); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RenderNodeClientConfig(context.Background(), connection, d); err == nil {
		t.Fatal("previous process accepted")
	}
}

func TestClientConfigExportRealMTLSOneUseCancelAndAuthority(t *testing.T) {
	f := newControlLifecycleFixture(t)
	ctx := context.Background()
	if err := f.service.EnableControl(ctx, f.token, f.receipt(t), true); err != nil {
		t.Fatal(err)
	}
	base := "https://" + f.address
	p := controlapi.Presence{BootID: uuid.NewString(), BootSequence: 1, ApplicationVersion: "test", ContractVersion: 1, StateEpoch: uuid.NewString(), BindingEpoch: 1, DesiredGeneration: 3, Capabilities: []string{"presence", "snapshot.v1", controlapi.ClientConfigExportCapability}, ObservedAt: time.Now()}
	var accepted controlapi.PresenceAccepted
	enrollmentHTTP(t, f.client, base, http.MethodPut, "/control/v1/node/presence", "", p, 200, &accepted)
	snapshot := testObservationSnapshot(p, accepted.SessionID)
	enrollmentHTTP(t, f.client, base, http.MethodPut, "/control/v1/node/snapshot", "", snapshot, 200, nil)
	inv, err := f.service.ControllerNodes(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	node := inv.Nodes[0].NodeID
	r := controlapi.ClientConfigExportRequest{StateEpoch: p.StateEpoch, BindingEpoch: 1, Generation: 3, TunnelID: snapshot.Desired.Tunnels[0].ID, ClientID: snapshot.Desired.Tunnels[0].Clients[0].ID, TunnelRevision: 7}
	metadata, err := f.service.PrepareClientConfigExport(ctx, f.token, node, r)
	if err != nil {
		t.Fatal(err)
	}
	// A different valid, recent administrator session cannot consume or cancel.
	registry, keys, err := f.service.openControlRegistry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ownerDigest, err := keys.SessionDigest(f.token)
	if err != nil {
		t.Fatal(err)
	}
	foreignToken, err := controlauth.NewSessionToken(nil)
	if err != nil {
		t.Fatal(err)
	}
	foreignDigest, err := keys.SessionDigest(foreignToken)
	if err != nil {
		t.Fatal(err)
	}
	rawDB, err := sql.Open("sqlite", f.cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = rawDB.Exec(`INSERT INTO controller_sessions(token_digest,user_id,created_at,expires_at,expires_at_unix_ms,authenticated_at,revoked_at) SELECT ?,user_id,created_at,expires_at,expires_at_unix_ms,authenticated_at,'' FROM controller_sessions WHERE token_digest=?`, foreignDigest[:], ownerDigest[:])
	if err != nil {
		t.Fatal(err)
	}
	_ = rawDB.Close()
	_ = registry.Close()
	if _, _, err := f.service.DownloadClientConfigExport(ctx, foreignToken, node, metadata.ExportID); err == nil {
		t.Fatal("foreign session consumed")
	}
	if err := f.service.CancelClientConfigExport(ctx, foreignToken, node, metadata.ExportID); err == nil {
		t.Fatal("foreign session canceled")
	}
	poll := controlapi.OperationPoll{SessionID: accepted.SessionID, BootID: p.BootID, BootSequence: 1, StateEpoch: p.StateEpoch, BindingEpoch: 1, Generation: 3}
	var delivery controlapi.ClientConfigExportDelivery
	enrollmentHTTP(t, f.client, base, http.MethodPost, "/control/v1/node/client-config-exports/poll", "", poll, 200, &delivery)
	enrollmentHTTP(t, f.client, base, http.MethodPost, "/control/v1/node/client-config-exports/poll", "", poll, 204, nil)
	noCert := f.tls.Clone()
	noCert.Certificates = nil
	foreign := &http.Client{Transport: &http.Transport{TLSClientConfig: noCert}, Timeout: time.Second}
	defer foreign.CloseIdleConnections()
	enrollmentHTTP(t, foreign, base, http.MethodPut, "/control/v1/node/client-config-exports/result", f.token, controlapi.ClientConfigExportResult{}, 403, nil)
	result := controlapi.ClientConfigExportResult{ClientConfigExportLocator: delivery.ClientConfigExportLocator, Authority: poll, Status: "ready", Config: "disposable-config-canary"}
	enrollmentHTTP(t, f.client, base, http.MethodPut, "/control/v1/node/client-config-exports/result", "", result, 204, nil)
	enrollmentHTTP(t, f.client, base, http.MethodPut, "/control/v1/node/client-config-exports/result", "", result, 409, nil)
	value, _, err := f.service.DownloadClientConfigExport(ctx, f.token, node, metadata.ExportID)
	if err != nil || string(value) != result.Config {
		t.Fatal("download")
	}
	clear(value)
	if _, _, err := f.service.DownloadClientConfigExport(ctx, f.token, node, metadata.ExportID); err == nil {
		t.Fatal("browser replay")
	}
	metadata, err = f.service.PrepareClientConfigExport(ctx, f.token, node, r)
	if err != nil {
		t.Fatal(err)
	}
	f.service.controllerAuthOptions.RecentAuthTTL = time.Nanosecond
	enrollmentHTTP(t, f.client, base, http.MethodPost, "/control/v1/node/client-config-exports/poll", "", poll, 409, nil)
	f.service.controllerAuthOptions.RecentAuthTTL = 0
	if _, err := f.service.clientConfigExports.snapshot(node, metadata.ExportID); err == nil {
		t.Fatal("lost recent MFA retained job")
	}
	metadata, err = f.service.PrepareClientConfigExport(ctx, f.token, node, r)
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := f.service.DownloadClientConfigExport(canceled, f.token, node, metadata.ExportID); err == nil {
		t.Fatal("canceled accepted")
	}
	if err := f.service.CancelClientConfigExport(ctx, f.token, node, metadata.ExportID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.clientConfigExports.snapshot(node, metadata.ExportID); err == nil {
		t.Fatal("canceled retained")
	}
	metadata, err = f.service.PrepareClientConfigExport(ctx, f.token, node, r)
	if err != nil {
		t.Fatal(err)
	}
	enrollmentHTTP(t, f.client, base, http.MethodPost, "/control/v1/node/client-config-exports/poll", "", poll, 200, &delivery)
	result.ClientConfigExportLocator = delivery.ClientConfigExportLocator
	enrollmentHTTP(t, f.client, base, http.MethodPut, "/control/v1/node/client-config-exports/result", "", result, 204, nil)
	p.BootSequence++
	p.BootID = uuid.NewString()
	enrollmentHTTP(t, f.client, base, http.MethodPut, "/control/v1/node/presence", "", p, 200, nil)
	if value, _, err := f.service.DownloadClientConfigExport(ctx, f.token, node, metadata.ExportID); err == nil || len(value) != 0 {
		t.Fatal("boot change did not fence consume")
	}
	if _, err := f.service.clientConfigExports.snapshot(node, metadata.ExportID); err == nil {
		t.Fatal("fenced config retained")
	}
}
