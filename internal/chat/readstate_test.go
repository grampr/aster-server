package chat

import (
	"strings"
	"testing"
)

func TestExcerptIsCutAroundTheMatch(t *testing.T) {
	if got := makeExcerpt("Hello WORLD", "world"); got != "Hello WORLD" {
		t.Fatalf("short content must be returned whole: %q", got)
	}
	content := strings.Repeat("a", 300) + "NEEDLE" + strings.Repeat("b", 600)
	got := makeExcerpt(content, "needle")
	if !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "…") || !strings.Contains(got, "NEEDLE") {
		t.Fatalf("excerpt must be elided on both sides around the match: %q", got)
	}
	if length := len([]rune(got)); length > 500 {
		t.Fatalf("excerpt exceeds the protocol limit: %d", length)
	}
}

func TestEscapeLikeNeutralizesWildcards(t *testing.T) {
	if got := escapeLike(`100%_a\b`); got != `100\%\_a\\b` {
		t.Fatalf("unexpected escape: %q", got)
	}
}
