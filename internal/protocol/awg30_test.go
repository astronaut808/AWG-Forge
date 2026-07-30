package protocol

import (
	"strings"
	"testing"

	"github.com/astronaut808/awg-forge/internal/config"
)

func TestAWG30DefaultsAndSecretsValidate(t *testing.T) {
	p := AWG30{}
	params, err := p.GenerateDefaults()
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := p.GenerateSecrets()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Validate(params); err != nil {
		t.Fatal(err)
	}
	if err := p.ValidateSecrets(secrets); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"S1", "S2", "S3", "S4"} {
		value, err := intParam(params, key)
		if err != nil || value < 12 {
			t.Fatalf("%s = %q, want >= 12", key, params[key])
		}
	}
	if _, ok := params[headerProtectionKey]; ok {
		t.Fatal("HeaderProtectionKey must not be a public protocol parameter")
	}
	for key, want := range map[string]string{
		"ContentPaddingAddition": awg30DefaultContentPaddingAddition,
		"RekeyAfterTime":         awg30DefaultRekeyAfterTime,
		"RekeyTimeout":           awg30DefaultRekeyTimeout,
		"RejectAfterTime":        awg30DefaultRejectAfterTime,
		"KeepaliveTimeout":       awg30DefaultKeepaliveTimeout,
		"MaxHandshakeAttempts":   awg30DefaultMaxHandshakeAttempts,
	} {
		if got := params[key]; got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestAWG30RejectsInvalidSecretAndRange(t *testing.T) {
	p := AWG30{}
	params, err := p.GenerateDefaults()
	if err != nil {
		t.Fatal(err)
	}
	params["RekeyAfterTime"] = "20-10"
	if err := p.Validate(params); err == nil {
		t.Fatal("expected descending range to be rejected")
	}
	if err := p.ValidateSecrets(config.ProtocolSecrets{headerProtectionKey: "not-base64"}); err == nil {
		t.Fatal("expected invalid header protection key to be rejected")
	}
}

func TestAWG30PersistentKeepaliveAcceptsSingleValueAndRange(t *testing.T) {
	p := AWG30{}
	params, err := p.GenerateDefaults()
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"25", "20-30"} {
		params["PersistentKeepalive"] = value
		if err := p.Validate(params); err != nil {
			t.Fatalf("PersistentKeepalive=%q: %v", value, err)
		}
	}
	params["PersistentKeepalive"] = "30-20"
	if err := p.Validate(params); err == nil {
		t.Fatal("expected descending PersistentKeepalive range to be rejected")
	}
}

func TestAWG30ClientConfigUsesPersistentKeepaliveRange(t *testing.T) {
	p := AWG30{}
	params, err := p.GenerateDefaults()
	if err != nil {
		t.Fatal(err)
	}
	params["PersistentKeepalive"] = "20-30"
	lines, err := p.RenderClientPeer(RenderContext{Tunnel: config.Tunnel{
		ServerHost:      "vpn.example.test",
		ListenPort:      51820,
		ServerPublicKey: "server-public-key",
		AllowedIPs:      "0.0.0.0/0",
		Keepalive:       25,
		ProtocolParams:  params,
	}}, config.Client{PresharedKey: "preshared-key"})
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range lines {
		if line.Key == "PersistentKeepalive" {
			if line.Value != "20-30" {
				t.Fatalf("PersistentKeepalive = %q, want range", line.Value)
			}
			return
		}
	}
	t.Fatal("PersistentKeepalive was not rendered")
}

func TestAWG30ServerConfigOmitsClientOnlyParameters(t *testing.T) {
	p := AWG30{}
	params, err := p.GenerateDefaults()
	if err != nil {
		t.Fatal(err)
	}
	params["ContentPaddingAddition"] = "10-20"
	params["RekeyAfterTime"] = "300"
	params["PersistentKeepalive"] = "20-30"
	secrets, err := p.GenerateSecrets()
	if err != nil {
		t.Fatal(err)
	}
	lines, err := p.RenderServerInterface(RenderContext{Tunnel: config.Tunnel{
		IPv4Subnet:       "10.30.0.0/24",
		ServerAddress:    "10.30.0.1",
		ServerPrivateKey: "server-private-key",
		ListenPort:       51820,
		ProtocolParams:   params,
		ProtocolSecrets:  secrets,
	}})
	if err != nil {
		t.Fatal(err)
	}
	clientOnly := map[string]bool{}
	for _, key := range awg30Keys[len(awg20Keys):] {
		clientOnly[key] = true
	}
	for _, line := range lines {
		if clientOnly[line.Key] {
			t.Fatalf("server config contains client-only AWG3 parameter %s", line.Key)
		}
	}
}

func TestSanitizeRuntimeOutputRemovesHeaderProtectionKey(t *testing.T) {
	output := "interface: awg30\n  header protection key: secret-value\n  listening port: 51840\n"
	got := SanitizeRuntimeOutput(output)
	if strings.Contains(got, "secret-value") || !strings.Contains(got, "listening port: 51840") {
		t.Fatalf("unsafe runtime output: %q", got)
	}
}
