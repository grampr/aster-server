package chat

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCursorIsBoundToResourceKind(t *testing.T) {
	id := uuid.MustParse("0198b8f0-2d6e-7c45-9a3f-92e3f2f3c1a0")
	encoded, err := encodeCursor(pageCursor{Kind: cursorMessages, Time: time.Now().UTC(), ID: id})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeCursor(encoded, cursorMessages); err != nil {
		t.Fatalf("message cursor should decode: %v", err)
	}
	if _, err := decodeCursor(encoded, cursorGuilds); err == nil {
		t.Fatal("a message cursor must not be accepted for a guild page")
	}
}

func TestValidationUsesUnicodeCharacters(t *testing.T) {
	if err := validateContent(strings.Repeat("星", 4000)); err != nil {
		t.Fatalf("4000 Unicode characters should be accepted: %v", err)
	}
	if err := validateContent(strings.Repeat("星", 4001)); err == nil {
		t.Fatal("4001 Unicode characters should be rejected")
	} else {
		var validationError *ValidationError
		if !errors.As(err, &validationError) || validationError.Field != "content" {
			t.Fatalf("unexpected error: %v", err)
		}
	}
}

func TestOptionalTextNormalizesBlankToNull(t *testing.T) {
	blank := "  "
	value, err := normalizeOptionalText("description", &blank, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if value != nil {
		t.Fatalf("blank optional text should normalize to nil, got %q", *value)
	}
}

func TestReactionEmojiValidation(t *testing.T) {
	for _, emoji := range []string{"👍", "❤️", "👨‍👩‍👧‍👦"} {
		if err := validateReactionEmoji(emoji); err != nil {
			t.Fatalf("valid emoji %q was rejected: %v", emoji, err)
		}
	}
	for _, invalid := range []string{"", "plain", "👍 ok", "\n", strings.Repeat("👍", 65)} {
		if err := validateReactionEmoji(invalid); err == nil {
			t.Fatalf("invalid reaction value %q was accepted", invalid)
		}
	}
}
