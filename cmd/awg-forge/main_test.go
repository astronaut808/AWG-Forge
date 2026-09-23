package main

import (
	"context"
	"encoding/base32"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlauth"
	"github.com/astronaut808/awg-forge/internal/sqldb"
	"github.com/pquerna/otp/totp"
)

func TestLoadControllerAuthFailsClosedAndLoadsPreparedRuntime(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{
		ConfigDir:            dir,
		DatabaseMode:         sqldb.ModeSQLite,
		DatabasePath:         filepath.Join(dir, "awg-forge.db"),
		DatabaseBusyTimeout:  time.Second,
		DatabaseQueryTimeout: time.Second,
		DatabaseMaxOpenConns: 1,
		DatabaseMaxIdleConns: 1,
	}
	standalone := config.State{Mode: config.ModeStandalone}
	auth, db, err := loadControllerAuth(cfg, standalone)
	if err != nil || auth != nil || db != nil {
		t.Fatalf("standalone controller auth = (%v, %v, %v)", auth, db, err)
	}

	controller := config.State{
		Mode: config.ModeController,
		Controller: &config.ControllerState{
			ControllerID: "11111111-1111-4111-8111-111111111111",
			ActivatedAt:  time.Unix(1_800_000_015, 0).UTC(),
		},
	}
	if _, _, err := loadControllerAuth(cfg, controller); err == nil || !strings.Contains(err.Error(), "no initialized administrator") {
		t.Fatalf("uninitialized controller error = %v", err)
	}

	setupDB, err := sqldb.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := setupDB.Migrate(context.Background()); err != nil {
		_ = setupDB.Close()
		t.Fatal(err)
	}
	keys, err := controlauth.LoadOrCreateKeys(filepath.Join(dir, controlauth.KeyFileName), nil)
	if err != nil {
		_ = setupDB.Close()
		t.Fatal(err)
	}
	setupAuth, err := controlauth.NewService(setupDB, keys, controlauth.Options{
		PasswordParams: controlauth.PasswordParams{Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32},
	})
	if err != nil {
		_ = setupDB.Close()
		t.Fatal(err)
	}
	now := controller.Controller.ActivatedAt
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("cmd-controller-auth-test"))
	code, err := totp.GenerateCode(secret, now)
	if err != nil {
		_ = setupDB.Close()
		t.Fatal(err)
	}
	if _, err := setupAuth.EnrollAdmin(context.Background(), "admin", "correct horse battery staple", secret, code, now); err != nil {
		_ = setupDB.Close()
		t.Fatal(err)
	}
	if err := setupDB.Close(); err != nil {
		t.Fatal(err)
	}

	auth, db, err = loadControllerAuth(cfg, controller)
	if err != nil {
		t.Fatal(err)
	}
	if auth == nil || db == nil {
		t.Fatal("controller authentication runtime was not loaded")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := controlauth.RemoveKeyFile(filepath.Join(dir, controlauth.KeyFileName)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadControllerAuth(cfg, controller); err == nil || !strings.Contains(err.Error(), "load controller authentication keys") {
		t.Fatalf("missing key error = %v", err)
	}
}

func TestRunClientEnableRejectsExceededTrafficLimit(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{
		ConfigDir:            dir,
		TunnelName:           "awg0",
		ServerHost:           "vpn.example.com",
		ListenPort:           51820,
		WebUIHost:            "127.0.0.1",
		WebUIPort:            51821,
		ExternalInterface:    "eth0",
		IPv4Subnet:           "10.8.0.0/24",
		DNS:                  "1.1.1.1",
		AllowedIPs:           "0.0.0.0/0",
		ProtocolProfile:      "awg_legacy_1_0",
		DatabaseMode:         sqldb.ModeSQLite,
		DatabasePath:         filepath.Join(dir, "awg-forge.db"),
		DatabaseBusyTimeout:  5 * time.Second,
		DatabaseQueryTimeout: 2 * time.Second,
		DatabaseMaxOpenConns: 1,
		DatabaseMaxIdleConns: 1,
	}
	if _, err := sqldb.Migrate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	svc := app.New(cfg)
	client, err := svc.AddClient("phone")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	if err := sqldb.RecordTrafficSamples(context.Background(), cfg, []sqldb.TrafficSample{
		{SampledAt: now.Add(-time.Minute), TunnelID: client.TunnelID, ClientID: client.ID, RxBytes: 0, TxBytes: 0, Present: true},
		{SampledAt: now, TunnelID: client.TunnelID, ClientID: client.ID, RxBytes: 6000, TxBytes: 0, Present: true},
	}); err != nil {
		t.Fatal(err)
	}
	limit := uint64(5000)
	if err := sqldb.SetClientTrafficLimit(context.Background(), cfg, client.TunnelID, client.ID, &limit); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetClientEnabled(client.ID, false); err != nil {
		t.Fatal(err)
	}

	err = runClient(cfg, svc, []string{"enable", client.ID})
	if err == nil || !strings.Contains(err.Error(), "traffic limit exceeded") {
		t.Fatalf("error = %v, want traffic limit exceeded", err)
	}
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if state.Tunnels[0].Clients[0].Enabled {
		t.Fatal("client should remain disabled when CLI enable is over traffic limit")
	}
}

func TestRunClientDisableKeepsClientEnabledWhenQuotaBlockCannotBeCleared(t *testing.T) {
	cfg := config.Config{
		ConfigDir:            t.TempDir(),
		TunnelName:           "awg0",
		ServerHost:           "vpn.example.com",
		ListenPort:           51820,
		WebUIHost:            "127.0.0.1",
		WebUIPort:            51821,
		ExternalInterface:    "eth0",
		IPv4Subnet:           "10.8.0.0/24",
		DNS:                  "1.1.1.1",
		AllowedIPs:           "0.0.0.0/0",
		ProtocolProfile:      "awg_legacy_1_0",
		DatabaseMode:         sqldb.ModeSQLite,
		DatabaseQueryTimeout: time.Second,
	}
	cfg.DatabasePath = filepath.Join(t.TempDir(), "not-a-database")
	if err := os.Mkdir(cfg.DatabasePath, 0700); err != nil {
		t.Fatal(err)
	}
	svc := app.New(cfg)
	client, err := svc.AddClient("phone")
	if err != nil {
		t.Fatal(err)
	}
	err = runClient(cfg, svc, []string{"disable", client.ID})
	if err == nil || !strings.Contains(err.Error(), "traffic limit marker unavailable") {
		t.Fatalf("error = %v, want traffic limit marker failure", err)
	}
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if !state.Tunnels[0].Clients[0].Enabled {
		t.Fatal("client must remain enabled when its quota block cannot be cleared")
	}
}
