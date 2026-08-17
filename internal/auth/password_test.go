package auth

import (
	"strings"
	"testing"
)

func TestPasswordHasher(t *testing.T) {
	hasher, err := NewPasswordHasher(PasswordParams{
		Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32,
	})
	if err != nil {
		t.Fatal(err)
	}

	first, err := hasher.Hash("a sufficiently long password")
	if err != nil {
		t.Fatal(err)
	}
	second, err := hasher.Hash("a sufficiently long password")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("password hashes must use unique salts")
	}
	valid, err := hasher.Verify("a sufficiently long password", first)
	if err != nil || !valid {
		t.Fatalf("correct password did not verify: valid=%v err=%v", valid, err)
	}
	valid, err = hasher.Verify("a different long password", first)
	if err != nil || valid {
		t.Fatalf("incorrect password unexpectedly verified: valid=%v err=%v", valid, err)
	}
}

func TestPasswordHasherRejectsUnsafeEncodedParameters(t *testing.T) {
	hasher, err := NewPasswordHasher(PasswordParams{
		Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32,
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded := "$argon2id$v=19$m=4294967295,t=1,p=1$" + strings.Repeat("A", 22) + "$" + strings.Repeat("A", 43)
	if _, err := hasher.Verify("a sufficiently long password", encoded); err == nil {
		t.Fatal("unsafe Argon2id parameters must be rejected before allocation")
	}
	encoded = "$argon2id$v=19$m=64,t=1,p=1junk$" + strings.Repeat("A", 22) + "$" + strings.Repeat("A", 43)
	if _, err := hasher.Verify("a sufficiently long password", encoded); err == nil {
		t.Fatal("non-canonical Argon2id parameters must be rejected")
	}
}

func TestPasswordValidationCountsUnicodeCharacters(t *testing.T) {
	if err := validatePassword(strings.Repeat("界", 15)); err != nil {
		t.Fatalf("15 Unicode characters should be valid: %v", err)
	}
	if err := validatePassword(strings.Repeat("界", 14)); err == nil {
		t.Fatal("14 Unicode characters should be rejected")
	}
}
