package protocol

import "testing"

func TestAWG20DefaultsValidate(t *testing.T) {
	params, err := AWG20{}.GenerateDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if err := (AWG20{}).Validate(params); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"S3", "S4", "I1", "I2", "I3", "I4", "I5"} {
		if params[key] == "" {
			t.Fatalf("%s default is empty", key)
		}
	}
}

func TestAWG20RejectsOverlappingHeaderRanges(t *testing.T) {
	params, err := AWG20{}.GenerateDefaults()
	if err != nil {
		t.Fatal(err)
	}
	params["H1"] = "1000-1100"
	params["H2"] = "1099-1200"
	if err := (AWG20{}).Validate(params); err == nil {
		t.Fatal("expected overlapping H ranges to be rejected")
	}
}

func TestAWG20RejectsInvalidS4(t *testing.T) {
	params, err := AWG20{}.GenerateDefaults()
	if err != nil {
		t.Fatal(err)
	}
	params["S4"] = "33"
	if err := (AWG20{}).Validate(params); err == nil {
		t.Fatal("expected S4 > 32 to be rejected")
	}
}

func TestAWG20AcceptsSingleHeaderValues(t *testing.T) {
	params, err := AWG20{}.GenerateDefaults()
	if err != nil {
		t.Fatal(err)
	}
	params["H1"] = "1000"
	params["H2"] = "2000"
	params["H3"] = "3000"
	params["H4"] = "4000"
	if err := (AWG20{}).Validate(params); err != nil {
		t.Fatal(err)
	}
}
