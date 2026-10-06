package chat

import (
	"strings"
	"testing"
)

func TestInviteCodeValidation(t *testing.T) {
	generated, err := newInviteCode()
	if err != nil {
		t.Fatal(err)
	}
	if !validInviteCode(generated) {
		t.Fatalf("generated invite code %q must satisfy the protocol pattern", generated)
	}
	other, err := newInviteCode()
	if err != nil {
		t.Fatal(err)
	}
	if generated == other {
		t.Fatal("invite codes must be unique")
	}
	for _, invalid := range []string{"", "short", strings.Repeat("a", 65), "valid-looking-but/slash", "日本語日本語日本語日本語日本語日本語"} {
		if validInviteCode(invalid) {
			t.Fatalf("invalid invite code %q was accepted", invalid)
		}
	}
}

func TestNicknameNormalization(t *testing.T) {
	blank := "  "
	if value, err := normalizeNickname(&blank); err != nil || value != nil {
		t.Fatalf("blank nickname should clear it: value=%v err=%v", value, err)
	}
	padded := "  ボブ "
	if value, err := normalizeNickname(&padded); err != nil || value == nil || *value != "ボブ" {
		t.Fatalf("nickname should be trimmed: value=%v err=%v", value, err)
	}
	exact := strings.Repeat("星", 64)
	if _, err := normalizeNickname(&exact); err != nil {
		t.Fatalf("64 Unicode characters should be accepted: %v", err)
	}
	tooLong := strings.Repeat("星", 65)
	if _, err := normalizeNickname(&tooLong); err == nil {
		t.Fatal("65 Unicode characters should be rejected")
	}
}
