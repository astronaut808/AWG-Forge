package protocol

import "testing"

func TestAWG15DefaultsIncludeFullSignatureChain(t *testing.T) {
	params, err := AWG15{}.GenerateDefaults()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"I1", "I2", "I3", "I4", "I5"} {
		if params[key] == "" {
			t.Fatalf("%s default is empty", key)
		}
	}
	if params["I1"] != defaultDNSLikeI1 {
		t.Fatal("AWG 1.5 should keep the DNS-like I1 default")
	}
	if err := (AWG15{}).Validate(params); err != nil {
		t.Fatal(err)
	}
}
