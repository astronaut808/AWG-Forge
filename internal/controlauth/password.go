package controlauth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	MinPasswordBytes           = 12
	MaxPasswordBytes           = 1024
	maxEncodedPasswordHashSize = 512
)

var (
	ErrInvalidPassword     = errors.New("invalid controller password")
	ErrInvalidPasswordHash = errors.New("invalid controller password hash")
)

// PasswordParams are encoded into every PHC string so parameters can be
// increased without invalidating existing controller credentials.
type PasswordParams struct {
	Memory      uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

func DefaultPasswordParams() PasswordParams {
	return PasswordParams{
		Memory:      64 * 1024,
		Iterations:  3,
		Parallelism: 4,
		SaltLength:  16,
		KeyLength:   32,
	}
}

func ValidateNewPassword(password string) error {
	if len(password) < MinPasswordBytes || len(password) > MaxPasswordBytes {
		return ErrInvalidPassword
	}
	return nil
}

func HashPassword(password string, params PasswordParams, random io.Reader) (string, error) {
	if err := ValidateNewPassword(password); err != nil {
		return "", err
	}
	if err := validatePasswordParams(params); err != nil {
		return "", err
	}
	if random == nil {
		random = rand.Reader
	}
	salt := make([]byte, params.SaltLength)
	if _, err := io.ReadFull(random, salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	hash := argon2.IDKey([]byte(password), salt, params.Iterations, params.Memory, params.Parallelism, params.KeyLength)
	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		params.Memory,
		params.Iterations,
		params.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

func VerifyPassword(password, encoded string) (bool, error) {
	params, salt, expected, err := parsePasswordHash(encoded)
	if err != nil {
		return false, err
	}
	actual := argon2.IDKey([]byte(password), salt, params.Iterations, params.Memory, params.Parallelism, uint32(len(expected)))
	return subtle.ConstantTimeCompare(actual, expected) == 1, nil
}

func PasswordNeedsRehash(encoded string, desired PasswordParams) (bool, error) {
	if err := validatePasswordParams(desired); err != nil {
		return false, err
	}
	current, _, _, err := parsePasswordHash(encoded)
	if err != nil {
		return false, err
	}
	return current != desired, nil
}

func validatePasswordParams(params PasswordParams) error {
	if params.Memory < 8*uint32(params.Parallelism) || params.Memory > 256*1024 ||
		params.Iterations < 1 || params.Iterations > 10 ||
		params.Parallelism < 1 || params.Parallelism > 16 ||
		params.SaltLength < 16 || params.SaltLength > 64 ||
		params.KeyLength < 16 || params.KeyLength > 64 {
		return ErrInvalidPasswordHash
	}
	return nil
}

func parsePasswordHash(encoded string) (PasswordParams, []byte, []byte, error) {
	if len(encoded) > maxEncodedPasswordHashSize {
		return PasswordParams{}, nil, nil, ErrInvalidPasswordHash
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return PasswordParams{}, nil, nil, ErrInvalidPasswordHash
	}
	params, err := parsePasswordParamList(parts[3])
	if err != nil {
		return PasswordParams{}, nil, nil, err
	}
	decoder := base64.RawStdEncoding.Strict()
	salt, err := decoder.DecodeString(parts[4])
	if err != nil {
		return PasswordParams{}, nil, nil, ErrInvalidPasswordHash
	}
	expected, err := decoder.DecodeString(parts[5])
	if err != nil {
		return PasswordParams{}, nil, nil, ErrInvalidPasswordHash
	}
	params.SaltLength = uint32(len(salt))
	params.KeyLength = uint32(len(expected))
	if err := validatePasswordParams(params); err != nil {
		return PasswordParams{}, nil, nil, err
	}
	return params, salt, expected, nil
}

func parsePasswordParamList(value string) (PasswordParams, error) {
	var params PasswordParams
	seen := map[string]bool{}
	items := strings.Split(value, ",")
	if len(items) != 3 {
		return PasswordParams{}, ErrInvalidPasswordHash
	}
	for _, item := range items {
		pair := strings.SplitN(item, "=", 2)
		if len(pair) != 2 || seen[pair[0]] {
			return PasswordParams{}, ErrInvalidPasswordHash
		}
		seen[pair[0]] = true
		switch pair[0] {
		case "m":
			n, err := strconv.ParseUint(pair[1], 10, 32)
			if err != nil {
				return PasswordParams{}, ErrInvalidPasswordHash
			}
			params.Memory = uint32(n)
		case "t":
			n, err := strconv.ParseUint(pair[1], 10, 32)
			if err != nil {
				return PasswordParams{}, ErrInvalidPasswordHash
			}
			params.Iterations = uint32(n)
		case "p":
			n, err := strconv.ParseUint(pair[1], 10, 8)
			if err != nil {
				return PasswordParams{}, ErrInvalidPasswordHash
			}
			params.Parallelism = uint8(n)
		default:
			return PasswordParams{}, ErrInvalidPasswordHash
		}
	}
	return params, nil
}
