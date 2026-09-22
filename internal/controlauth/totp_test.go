package controlauth

import (
	"encoding/base32"
	"errors"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

func TestValidateTOTPCurrentPreviousAndReplay(t *testing.T) {
	secret := testTOTPSecretValue()
	now := time.Unix(1_800_000_015, 0).UTC()
	currentCode, err := totp.GenerateCode(secret, now)
	if err != nil {
		t.Fatal(err)
	}
	currentStep, err := ValidateTOTP(secret, currentCode, now, -1)
	if err != nil {
		t.Fatal(err)
	}
	if currentStep != now.Unix()/totpPeriodSeconds {
		t.Fatalf("current step = %d", currentStep)
	}
	if _, err := ValidateTOTP(secret, currentCode, now, currentStep); !errors.Is(err, ErrTOTPReplay) {
		t.Fatalf("replayed TOTP error = %v", err)
	}
	previousCode, err := totp.GenerateCode(secret, now.Add(-30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	previousStep, err := ValidateTOTP(secret, previousCode, now, -1)
	if err != nil {
		t.Fatal(err)
	}
	if previousStep != currentStep-1 {
		t.Fatalf("previous step = %d, want %d", previousStep, currentStep-1)
	}
}

func TestValidateTOTPRejectsFutureAndMalformedCodes(t *testing.T) {
	secret := testTOTPSecretValue()
	now := time.Unix(1_800_000_015, 0).UTC()
	future, err := totp.GenerateCode(secret, now.Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{future, "12345", "abcdef"} {
		if _, err := ValidateTOTP(secret, code, now, -1); !errors.Is(err, ErrInvalidTOTP) {
			t.Fatalf("ValidateTOTP(%q) error = %v", code, err)
		}
	}
}

func testTOTPSecretValue() string {
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("test-only-totp-key-material"))
}
