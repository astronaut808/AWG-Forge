package protocol

import (
	"strconv"
	"testing"
)

func TestLegacyDefaultsValidateAndUseStrongGeneratedValues(t *testing.T) {
	params, err := Legacy10{}.GenerateDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if err := (Legacy10{}).Validate(params); err != nil {
		t.Fatal(err)
	}

	seen := map[uint64]string{}
	for _, key := range []string{"H1", "H2", "H3", "H4"} {
		value, err := strconv.ParseUint(params[key], 10, 32)
		if err != nil {
			t.Fatalf("%s is not uint32: %v", key, err)
		}
		if value < 5 {
			t.Fatalf("%s default is too close to reserved/default-looking values: %d", key, value)
		}
		if prev, ok := seen[value]; ok {
			t.Fatalf("%s duplicates %s with value %d", key, prev, value)
		}
		seen[value] = key
	}
}

func TestLegacyDefaultsAvoidS1Plus56EqualsS2(t *testing.T) {
	for i := 0; i < 100; i++ {
		params, err := Legacy10{}.GenerateDefaults()
		if err != nil {
			t.Fatal(err)
		}
		s1, _, err := validateJunkAndBasePadding(params)
		if err != nil {
			t.Fatal(err)
		}
		s2, err := strconv.Atoi(params["S2"])
		if err != nil {
			t.Fatal(err)
		}
		if s1+56 == s2 {
			t.Fatalf("generated weak S pair: S1=%d S2=%d", s1, s2)
		}
	}
}
