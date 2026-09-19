package controlauth

import (
	"errors"
	"strings"
	"testing"
)

func TestPasswordHashRoundTrip(t *testing.T) {
	params := testPasswordParams()
	hash, err := HashPassword("correct horse battery staple", params, nil)
	if err != nil {
		t.Fatal(err)
	}
	valid, err := VerifyPassword("correct horse battery staple", hash)
	if err != nil {
		t.Fatal(err)
	}
	if !valid {
		t.Fatal("valid password was rejected")
	}
	valid, err = VerifyPassword("incorrectbird", hash)
	if err != nil {
		t.Fatal(err)
	}
	if valid {
		t.Fatal("invalid password was accepted")
	}
	needsRehash, err := PasswordNeedsRehash(hash, params)
	if err != nil {
		t.Fatal(err)
	}
	if needsRehash {
		t.Fatal("fresh password hash needs rehash")
	}
}

func TestPasswordHashRejectsUnsafeEncodedParameters(t *testing.T) {
	for _, params := range []string{
		"m=1048576,t=3,p=4",
		"m=65536,t=3,p=257",
	} {
		encoded := "$argon2id$v=19$" + params + "$MDEyMzQ1Njc4OWFiY2RlZg$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
		if _, err := VerifyPassword("password", encoded); !errors.Is(err, ErrInvalidPasswordHash) {
			t.Fatalf("VerifyPassword parameters %q error = %v, want invalid hash", params, err)
		}
	}
}

func TestPasswordHashRejectsOversizedPHCBeforeDecode(t *testing.T) {
	encoded := "$argon2id$v=19$m=65536,t=3,p=4$" + strings.Repeat("A", maxEncodedPasswordHashSize)
	if _, err := VerifyPassword("password", encoded); !errors.Is(err, ErrInvalidPasswordHash) {
		t.Fatalf("oversized PHC error = %v", err)
	}
}

func TestPasswordValidationBoundsInput(t *testing.T) {
	if err := ValidateNewPassword("too-short"); !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("short password error = %v", err)
	}
	if err := ValidateNewPassword(strings.Repeat("a", MaxPasswordBytes+1)); !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("long password error = %v", err)
	}
}

func testPasswordParams() PasswordParams {
	return PasswordParams{
		Memory:      64,
		Iterations:  1,
		Parallelism: 1,
		SaltLength:  16,
		KeyLength:   32,
	}
}

func BenchmarkHashPasswordDefault(b *testing.B) {
	params := DefaultPasswordParams()
	for range b.N {
		if _, err := HashPassword("correct horse battery staple", params, nil); err != nil {
			b.Fatal(err)
		}
	}
}
