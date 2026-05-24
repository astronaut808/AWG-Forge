package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

func TestVerifyReturnsReportWithoutWritingFiles(t *testing.T) {
	cfg := testConfig(t)
	svc := app.New(cfg)
	if _, err := svc.AddClient("phone"); err != nil {
		t.Fatal(err)
	}
	archive, err := Create(context.Background(), cfg, svc, testPassword, Options{})
	if err != nil {
		t.Fatal(err)
	}
	verifyCfg := cfg
	verifyCfg.ConfigDir = t.TempDir()
	report, err := Verify(context.Background(), verifyCfg, testPassword, writeTempArchive(t, archive.Data))
	if err != nil {
		t.Fatal(err)
	}
	if report.Format != formatVersion {
		t.Fatalf("format = %q, want %q", report.Format, formatVersion)
	}
	if report.SchemaVersion != config.CurrentStateSchemaVersion {
		t.Fatalf("schema = %d, want %d", report.SchemaVersion, config.CurrentStateSchemaVersion)
	}
	if report.ClientCount != 1 {
		t.Fatalf("clients = %d, want 1", report.ClientCount)
	}
	if len(report.Tunnels) != 1 {
		t.Fatalf("tunnels = %d, want 1", len(report.Tunnels))
	}
	if report.Tunnels[0].Name != "awg0" || report.Tunnels[0].Clients != 1 {
		t.Fatalf("tunnel report = %+v", report.Tunnels[0])
	}
	if _, err := os.Stat(filepath.Join(verifyCfg.ConfigDir, "state.json")); !os.IsNotExist(err) {
		t.Fatalf("verify wrote state.json or stat failed unexpectedly: %v", err)
	}
}

func TestVerifyRejectsWrongPassword(t *testing.T) {
	cfg := testConfig(t)
	svc := app.New(cfg)
	if _, err := svc.AddClient("phone"); err != nil {
		t.Fatal(err)
	}
	archive, err := Create(context.Background(), cfg, svc, testPassword, Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = Verify(context.Background(), cfg, "wrong password", writeTempArchive(t, archive.Data))
	if err == nil || !strings.Contains(err.Error(), "decrypt failed") {
		t.Fatalf("verify error = %v, want decrypt failed", err)
	}
}

func TestVerifyRejectsDuplicatedTunnelPorts(t *testing.T) {
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
	var mutatedStateJSON []byte
	mutated := mutatePlainZip(t, plain, func(name string, b []byte) []byte {
		switch name {
		case "state.json":
			var state config.State
			if err := json.Unmarshal(b, &state); err != nil {
				t.Fatal(err)
			}
			clone := state.Tunnels[0]
			clone.ID = "duplicate-port"
			clone.Name = "awg-copy"
			clone.InterfaceName = "awg-copy"
			clone.IPv4Subnet = "10.9.0.0/24"
			state.Tunnels = append(state.Tunnels, clone)
			out, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			mutatedStateJSON = out
			return out
		case "metadata.json":
			var metadata Metadata
			if err := json.Unmarshal(b, &metadata); err != nil {
				t.Fatal(err)
			}
			for i := range metadata.Files {
				if metadata.Files[i].Path == "state.json" {
					metadata.Files[i] = testFileMeta("state.json", mutatedStateJSON)
				}
			}
			return mustJSON(t, metadata)
		default:
			return b
		}
	})
	encrypted, err := encrypt(mutated, testPassword, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	_, err = Verify(context.Background(), cfg, testPassword, writeTempArchive(t, encrypted))
	if err == nil || !strings.Contains(err.Error(), "listen port 51820 is duplicated") {
		t.Fatalf("verify error = %v, want duplicate listen port", err)
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

func TestDecryptRejectsUnexpectedKDFParameters(t *testing.T) {
	archive := encryptedTestZip(t,
		testZipEntry{name: "metadata.json", data: mustJSON(t, testMetadata(nil))},
		testZipEntry{name: "state.json", data: []byte(`{"schema_version":2}`)},
	)
	var env encryptedArchive
	if err := json.Unmarshal(archive, &env); err != nil {
		t.Fatal(err)
	}
	env.KDF.MemoryKiB = 1024 * 1024
	mutated, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decrypt(mutated, testPassword); err == nil || !strings.Contains(err.Error(), "unsupported backup kdf") {
		t.Fatalf("decrypt error = %v, want unsupported backup kdf", err)
	}
}

func TestVerifyRejectsOversizedBackupFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "too-large.afbackup")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxEncryptedBackupBytes + 1); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t)
	_, err = Verify(context.Background(), cfg, testPassword, path)
	if err == nil || !strings.Contains(err.Error(), "backup file is too large") {
		t.Fatalf("verify error = %v, want backup file is too large", err)
	}
}

func TestRestoreRejectsFilesNotListedInMetadata(t *testing.T) {
	stateJSON := `{"schema_version":2}`
	metadata := testMetadata([]FileMeta{testFileMeta("state.json", []byte(stateJSON))})
	archive := encryptedTestZip(t,
		testZipEntry{name: "metadata.json", data: mustJSON(t, metadata)},
		testZipEntry{name: "state.json", data: []byte(stateJSON)},
		testZipEntry{name: "tunnels/awg0/server.conf", data: []byte("unexpected")},
	)
	cfg := testConfig(t)
	err := Restore(context.Background(), cfg, testPassword, writeTempArchive(t, archive))
	if err == nil || !strings.Contains(err.Error(), "not listed in metadata") {
		t.Fatalf("restore error = %v, want unlisted file rejection", err)
	}
}

func TestRestoreRejectsDuplicateArchiveFiles(t *testing.T) {
	stateJSON := `{"schema_version":2}`
	metadata := testMetadata([]FileMeta{testFileMeta("state.json", []byte(stateJSON))})
	archive := encryptedTestZip(t,
		testZipEntry{name: "metadata.json", data: mustJSON(t, metadata)},
		testZipEntry{name: "state.json", data: []byte(stateJSON)},
		testZipEntry{name: "state.json", data: []byte(stateJSON)},
	)
	cfg := testConfig(t)
	err := Restore(context.Background(), cfg, testPassword, writeTempArchive(t, archive))
	if err == nil || !strings.Contains(err.Error(), "state.json is duplicated") {
		t.Fatalf("restore error = %v, want duplicate file rejection", err)
	}
}

func TestRestoreRejectsDuplicateMetadataFiles(t *testing.T) {
	stateJSON := `{"schema_version":2}`
	meta := testFileMeta("state.json", []byte(stateJSON))
	metadata := testMetadata([]FileMeta{meta, meta})
	archive := encryptedTestZip(t,
		testZipEntry{name: "metadata.json", data: mustJSON(t, metadata)},
		testZipEntry{name: "state.json", data: []byte(stateJSON)},
	)
	cfg := testConfig(t)
	err := Restore(context.Background(), cfg, testPassword, writeTempArchive(t, archive))
	if err == nil || !strings.Contains(err.Error(), "metadata file state.json is duplicated") {
		t.Fatalf("restore error = %v, want duplicate metadata rejection", err)
	}
}

func TestRestoreRejectsZipSlipPaths(t *testing.T) {
	for _, name := range []string{"../escape", "tunnels/../../escape", `tunnels\..\escape`} {
		t.Run(name, func(t *testing.T) {
			var plain bytes.Buffer
			zw := zip.NewWriter(&plain)
			for path, content := range map[string]string{
				"metadata.json": `{"format":"awg-forge-backup-v1","schema_version":2,"created_at":"2026-01-01T00:00:00Z","files":[]}`,
				"state.json":    `{"schema_version":2}`,
				name:            "bad",
			} {
				w, err := zw.Create(path)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := w.Write([]byte(content)); err != nil {
					t.Fatal(err)
				}
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
			if err == nil || !strings.Contains(err.Error(), "invalid archive path") {
				t.Fatalf("restore error = %v, want invalid archive path", err)
			}
		})
	}
}

func TestSafeRestorePathStaysUnderRoot(t *testing.T) {
	root := t.TempDir()
	got, err := safeRestorePath(root, "tunnels/awg0/server.conf")
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(root, got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		t.Fatalf("restore path escaped root: %s", got)
	}
	if _, err := safeRestorePath(root, "../escape"); err == nil {
		t.Fatal("expected traversal path to be rejected")
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

type testZipEntry struct {
	name string
	data []byte
}

func encryptedTestZip(t *testing.T, entries ...testZipEntry) []byte {
	t.Helper()
	var plain bytes.Buffer
	zw := zip.NewWriter(&plain)
	for _, entry := range entries {
		w, err := zw.Create(entry.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	encrypted, err := encrypt(plain.Bytes(), testPassword, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return encrypted
}

func testMetadata(files []FileMeta) Metadata {
	return Metadata{
		Format:        formatVersion,
		SchemaVersion: config.CurrentStateSchemaVersion,
		CreatedAt:     "2026-01-01T00:00:00Z",
		Files:         files,
	}
}

func testFileMeta(path string, data []byte) FileMeta {
	sum := sha256.Sum256(data)
	return FileMeta{Path: path, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
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
