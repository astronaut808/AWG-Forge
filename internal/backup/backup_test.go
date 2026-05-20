package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
)

const testPassword = "correct horse battery staple"

func TestBackupRestoreRoundTripEncrypted(t *testing.T) {
	cfg := testConfig(t)
	svc := app.New(cfg)
	client, err := svc.AddClient("phone")
	if err != nil {
		t.Fatal(err)
	}
	archive, err := Create(context.Background(), cfg, svc, testPassword, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(archive.Data), client.PrivateKey) || strings.Contains(string(archive.Data), client.PresharedKey) {
		t.Fatal("encrypted archive contains plaintext client secrets")
	}
	restoreCfg := cfg
	restoreCfg.ConfigDir = t.TempDir()
	if err := Restore(context.Background(), restoreCfg, testPassword, writeTempArchive(t, archive.Data)); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(filepath.Join(restoreCfg.ConfigDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(restored), client.PrivateKey) {
		t.Fatal("restore did not preserve client private key")
	}
	assertMode(t, restoreCfg.ConfigDir, 0700)
	assertMode(t, filepath.Join(restoreCfg.ConfigDir, "state.json"), 0600)
}

func TestRestoreRejectsWrongPassword(t *testing.T) {
	cfg := testConfig(t)
	svc := app.New(cfg)
	if _, err := svc.AddClient("phone"); err != nil {
		t.Fatal(err)
	}
	archive, err := Create(context.Background(), cfg, svc, testPassword, Options{})
	if err != nil {
		t.Fatal(err)
	}
	restoreCfg := cfg
	restoreCfg.ConfigDir = t.TempDir()
	err = Restore(context.Background(), restoreCfg, "wrong password", writeTempArchive(t, archive.Data))
	if err == nil || !strings.Contains(err.Error(), "decrypt failed") {
		t.Fatalf("restore error = %v, want decrypt failed", err)
	}
}

func TestRestoreKeepsEncryptedPreRestoreBackup(t *testing.T) {
	cfg := testConfig(t)
	svc := app.New(cfg)
	if _, err := svc.AddClient("old"); err != nil {
		t.Fatal(err)
	}
	sourceCfg := testConfig(t)
	sourceSvc := app.New(sourceCfg)
	if _, err := sourceSvc.AddClient("new"); err != nil {
		t.Fatal(err)
	}
	archive, err := Create(context.Background(), sourceCfg, sourceSvc, testPassword, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := Restore(context.Background(), cfg, testPassword, writeTempArchive(t, archive.Data)); err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(filepath.Join(cfg.ConfigDir, "backups", "pre-restore-*.afbackup"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("pre-restore backups = %d, want 1", len(matches))
	}
	assertMode(t, matches[0], 0600)
}

func TestRestoreRejectsNewerSchema(t *testing.T) {
	cfg := testConfig(t)
	svc := app.New(cfg)
	if _, err := svc.AddClient("phone"); err != nil {
		t.Fatal(err)
	}
	archive, err := Create(context.Background(), cfg, svc, testPassword, Options{})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := decrypt(archive.Data, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	mutated := mutatePlainZip(t, plain, func(name string, b []byte) []byte {
		if name != "metadata.json" {
			return b
		}
		var raw map[string]any
		if err := json.Unmarshal(b, &raw); err != nil {
			t.Fatal(err)
		}
		raw["schema_version"] = float64(config.CurrentStateSchemaVersion + 1)
		out, err := json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		return out
	})
	encrypted, err := encrypt(mutated, testPassword, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	restoreCfg := cfg
	restoreCfg.ConfigDir = t.TempDir()
	err = Restore(context.Background(), restoreCfg, testPassword, writeTempArchive(t, encrypted))
	if err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("restore error = %v, want newer schema rejection", err)
	}
}

func TestRestoreRejectsMissingMetadata(t *testing.T) {
	var plain bytes.Buffer
	zw := zip.NewWriter(&plain)
	w, err := zw.Create("state.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(`{"schema_version":2}`)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	encrypted, err := encrypt(plain.Bytes(), testPassword, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t)
	err = Restore(context.Background(), cfg, testPassword, writeTempArchive(t, encrypted))
	if err == nil || !strings.Contains(err.Error(), "metadata.json is missing") {
		t.Fatalf("restore error = %v, want missing metadata", err)
	}
}

func TestWriteFileUsesPrivatePermissions(t *testing.T) {
	cfg := testConfig(t)
	svc := app.New(cfg)
	if _, err := svc.AddClient("phone"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "backup.afbackup")
	if _, err := WriteFile(context.Background(), cfg, svc, testPassword, path); err != nil {
		t.Fatal(err)
	}
	assertMode(t, path, 0600)
}

func writeTempArchive(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "backup.afbackup")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func mutatePlainZip(t *testing.T, data []byte, mutate func(string, []byte) []byte) []byte {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, file := range reader.File {
		rc, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		w, err := zw.Create(file.Name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(mutate(file.Name, b)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}

func testConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{
		ConfigDir:           t.TempDir(),
		TunnelName:          "awg0",
		ServerHost:          "vpn.example.com",
		ListenPort:          51820,
		WebUIHost:           "127.0.0.1",
		WebUIPort:           51821,
		ExternalInterface:   "eth0",
		IPv4Subnet:          "10.8.0.0/24",
		DNS:                 "1.1.1.1",
		AllowedIPs:          "0.0.0.0/0",
		PersistentKeepalive: 0,
		MTU:                 1280,
		ProtocolProfile:     "awg_legacy_1_0",
	}
}
