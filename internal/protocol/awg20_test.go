package protocol

import (
	"regexp"
	"strings"
	"testing"
)

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
	if params["I1"] == defaultDNSLikeI1 {
		t.Fatal("AWG 2.0 I1 should not reuse the AWG 1.5 DNS-like default")
	}
	assertQUICLikeI1(t, params["I1"])
}

func TestAWG20DefaultsUseNonOverlappingHeaderRanges(t *testing.T) {
	params, err := AWG20{}.GenerateDefaults()
	if err != nil {
		t.Fatal(err)
	}
	seen := make([]headerRange, 0, 4)
	for _, key := range []string{"H1", "H2", "H3", "H4"} {
		rng, err := parseHeaderRange(params[key])
		if err != nil {
			t.Fatalf("%s is not a valid range: %v", key, err)
		}
		if rng.start == rng.end {
			t.Fatalf("%s default should be a range, got single value %d", key, rng.start)
		}
		if rng.start < 1024 {
			t.Fatalf("%s range starts too close to reserved/default-looking values: %d", key, rng.start)
		}
		if rng.end-rng.start < 30000 {
			t.Fatalf("%s range is too narrow: %d-%d", key, rng.start, rng.end)
		}
		for _, prev := range seen {
			if rng.overlaps(prev) {
				t.Fatalf("%s overlaps previous generated range", key)
			}
		}
		seen = append(seen, rng)
	}
}

func TestAWG20DefaultsRandomizeQUICLikeI1(t *testing.T) {
	first, err := AWG20{}.GenerateDefaults()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		next, err := AWG20{}.GenerateDefaults()
		if err != nil {
			t.Fatal(err)
		}
		if first["I1"] != next["I1"] {
			return
		}
	}
	t.Fatal("expected AWG 2.0 I1 to change across generated defaults")
}

func TestAWG20DefaultsAvoidParserBoundaryRandomChunks(t *testing.T) {
	for i := 0; i < 20; i++ {
		params, err := AWG20{}.GenerateDefaults()
		if err != nil {
			t.Fatal(err)
		}
		for _, token := range signatureTokenRE.FindAllString(params["I1"], -1) {
			if !strings.HasPrefix(token, "<r ") {
				continue
			}
			size, err := signatureTokenSize(token)
			if err != nil {
				t.Fatal(err)
			}
			if size > randomSignatureChunkMax {
				t.Fatalf("generated random chunk crosses parser-safe boundary: %s", token)
			}
		}
	}
}

func TestAWG20AcceptsQUICLikeI1SizeRange(t *testing.T) {
	params, err := AWG20{}.GenerateDefaults()
	if err != nil {
		t.Fatal(err)
	}
	params["I1"] = "<b 0xcf000000010811223344556677880000449e><r 999><r 183>"
	if err := (AWG20{}).Validate(params); err != nil {
		t.Fatal(err)
	}
	params["I1"] = "<r 1000>"
	if err := (AWG20{}).Validate(params); err == nil {
		t.Fatal("expected parser-boundary random token to be rejected")
	}
	params["I1"] = "<r 1233>"
	if err := (AWG20{}).Validate(params); err == nil {
		t.Fatal("expected signature larger than 1232 bytes to be rejected")
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

func assertQUICLikeI1(t *testing.T, value string) {
	t.Helper()
	re := regexp.MustCompile(`^<b 0xc[0-9a-f]00000001(08|0c|10)[0-9a-f]+(00|08[0-9a-f]{16})00[0-9a-f]{4}>(<r [0-9]+>)+$`)
	if !re.MatchString(value) {
		t.Fatalf("unexpected QUIC-like I1 shape: %s", value)
	}
	if err := validateSignatureParam("I1", value); err != nil {
		t.Fatal(err)
	}
	size, err := signatureSize(value)
	if err != nil {
		t.Fatal(err)
	}
	if size < quicInitialMinPayloadSize || size > quicInitialMaxPayloadSize {
		t.Fatalf("unexpected QUIC-like I1 size: got %d, want %d..%d", size, quicInitialMinPayloadSize, quicInitialMaxPayloadSize)
	}
}

func signatureSize(value string) (int, error) {
	rest := value
	total := 0
	for rest != "" {
		loc := signatureTokenRE.FindStringIndex(rest)
		if loc == nil || loc[0] != 0 {
			return 0, validateSignatureParam("I1", value)
		}
		token := rest[loc[0]:loc[1]]
		size, err := signatureTokenSize(token)
		if err != nil {
			return 0, err
		}
		total += size
		rest = strings.TrimSpace(rest[loc[1]:])
	}
	return total, nil
}
