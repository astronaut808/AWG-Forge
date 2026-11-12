package controlauth

import (
	"crypto/hmac"
	"encoding/base32"
	"errors"
	"strings"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

const totpPeriodSeconds int64 = 30

var (
	ErrInvalidTOTP = errors.New("invalid controller TOTP")
	ErrTOTPReplay  = errors.New("controller TOTP was already used")
)

// ValidateTOTP accepts the current or immediately previous 30-second step.
// It returns the matched step so the store can reject replay atomically.
func ValidateTOTP(secret, code string, now time.Time, lastUsedStep int64) (int64, error) {
	secret = strings.ToUpper(strings.TrimSpace(secret))
	code = strings.TrimSpace(code)
	if err := validateTOTPSecret(secret); err != nil || !sixDigits(code) || now.Unix() < 0 {
		return 0, ErrInvalidTOTP
	}
	current := now.UTC().Unix() / totpPeriodSeconds
	for _, step := range []int64{current, current - 1} {
		if step < 0 {
			continue
		}
		generated, err := totp.GenerateCodeCustom(secret, time.Unix(step*totpPeriodSeconds, 0).UTC(), totp.ValidateOpts{
			Period:    uint(totpPeriodSeconds),
			Skew:      0,
			Digits:    otp.DigitsSix,
			Algorithm: otp.AlgorithmSHA1,
		})
		if err != nil {
			return 0, ErrInvalidTOTP
		}
		if hmac.Equal([]byte(generated), []byte(code)) {
			if step <= lastUsedStep {
				return 0, ErrTOTPReplay
			}
			return step, nil
		}
	}
	return 0, ErrInvalidTOTP
}

func validateTOTPSecret(secret string) error {
	decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil || len(decoded) < 20 || len(decoded) > 64 {
		return ErrInvalidTOTP
	}
	return nil
}

func sixDigits(code string) bool {
	if len(code) != 6 {
		return false
	}
	for _, char := range code {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}
