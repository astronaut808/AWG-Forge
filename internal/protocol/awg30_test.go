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

func TestSanitizeRuntimeOutputRemovesHeaderProtectionKey(t *testing.T) {
	output := "interface: awg30\n  header protection key: secret-value\n  listening port: 51840\n"
	got := SanitizeRuntimeOutput(output)
	if strings.Contains(got, "secret-value") || !strings.Contains(got, "listening port: 51840") {
		t.Fatalf("unsafe runtime output: %q", got)
	}
}
