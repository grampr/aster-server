package auth

import (
	"bytes"
	"strings"
	"testing"
)

func TestTokenIssuerCreatesOpaqueDistinctTokens(t *testing.T) {
	issuer := TokenIssuer{}
	first, firstHash, err := issuer.NewAccessToken()
	if err != nil {
		t.Fatal(err)
	}
	second, secondHash, err := issuer.NewAccessToken()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first, "aster_at_") || first == second {
		t.Fatal("access tokens must be prefixed and distinct")
	}
	if bytes.Equal(firstHash, secondHash) || !bytes.Equal(firstHash, HashToken(first)) {
		t.Fatal("token hashes must be deterministic without exposing the raw token")
	}
	if len(firstHash) != 32 {
		t.Fatalf("expected SHA-256 hash, got %d bytes", len(firstHash))
	}
}
