package support

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
)

func TestGenerateRedactsSecrets(t *testing.T) {
	cfg := testConfig(t)
	cfg.Password = "secret-password"
	cfg.SessionSecret = "secret-session"
	svc := app.New(cfg)
	client, err := svc.AddClient("phone")
	if err != nil {
		t.Fatal(err)
	}
	warpConfig := `[Interface]
PrivateKey = warp-private-key
Address = 172.16.0.2/32
MTU = 1280

[Peer]
PublicKey = warp-peer-public-key
PresharedKey = warp-preshared-key
Endpoint = engage.cloudflareclient.com:2408
PersistentKeepalive = 25
`
	if _, err := svc.ImportWarpConfig(warpConfig); err != nil {
		t.Fatal(err)
	}
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := Generate(context.Background(), cfg, svc, Options{Now: time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	content := unzipText(t, bundle.Data)
	for _, secret := range []string{
		cfg.Password,
		cfg.SessionSecret,
		state.SessionSecret,
		state.Tunnels[0].ServerPrivateKey,
		client.PrivateKey,
		client.PresharedKey,
		"warp-private-key",
		"warp-preshared-key",
	} {
		if secret != "" && strings.Contains(content, secret) {
			t.Fatalf("support bundle leaked secret %q in:\n%s", secret, content)
		}
	}
	if !strings.Contains(content, `"password_set": true`) {
		t.Fatalf("support bundle should preserve password presence without value:\n%s", content)
	}
	if !strings.Contains(content, `"protocol_param_keys"`) {
		t.Fatalf("support bundle should include protocol parameter keys:\n%s", content)
	}
	if !strings.Contains(content, "database.json") || !strings.Contains(content, `"enabled": false`) {
		t.Fatalf("support bundle should include database metadata without requiring sqlite:\n%s", content)
	}
}

func TestSanitizeTextRedactsRuntimeKeys(t *testing.T) {
	got := sanitizeText(`interface: awg0
  public key: server-public-key
  private key: server-private-key

peer: client-public-key
  preshared key: client-psk
  jc: 8
  h1: 123456
  i1: <r 2><b 0x1234>
`)
	for _, secret := range []string{"server-public-key", "server-private-key", "client-public-key", "client-psk", "123456", "0x1234"} {
		if strings.Contains(got, secret) {
			t.Fatalf("sanitizeText leaked %q in:\n%s", secret, got)
		}
	}
	if !strings.Contains(got, "sha256:") {
		t.Fatalf("sanitizeText should keep key fingerprints:\n%s", got)
	}
}

func unzipText(t *testing.T, data []byte) string {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
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
		out.WriteString(file.Name)
		out.WriteByte('\n')
		out.Write(b)
		out.WriteByte('\n')
	}
	return out.String()
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
