package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakeGoogle struct {
	key       *rsa.PrivateKey
	keyID     string
	server    *httptest.Server
	keyFetchs atomic.Int32
}

func newFakeGoogle(t *testing.T) *fakeGoogle {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeGoogle{key: key, keyID: "key-1"}
	fake.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fake.keyFetchs.Add(1)
		_ = json.NewEncoder(writer).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "alg": "RS256", "kid": fake.keyID,
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeGoogle) client(t *testing.T) *GoogleClient {
	t.Helper()
	client, err := NewGoogleClient(GoogleConfig{
		ClientID: "client-id", ClientSecret: "secret", RedirectURL: "https://aster.example/api/v1/auth/google/callback",
		JWKSEndpoint: f.server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func (f *fakeGoogle) token(t *testing.T, header map[string]any, claims map[string]any) string {
	t.Helper()
	headerJSON, _ := json.Marshal(header)
	claimsJSON, _ := json.Marshal(claims)
	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." + base64.RawURLEncoding.EncodeToString(claimsJSON)
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func validClaims() map[string]any {
	return map[string]any{
		"iss": "https://accounts.google.com", "aud": "client-id", "sub": "1234567890", "exp": time.Now().Add(time.Hour).Unix(),
		"nonce": "expected-nonce", "email": "user@example.com", "email_verified": true, "name": "User", "picture": "https://example.com/p.png",
	}
}

func TestVerifyIDTokenAcceptsAValidToken(t *testing.T) {
	fake := newFakeGoogle(t)
	client := fake.client(t)
	token := fake.token(t, map[string]any{"alg": "RS256", "kid": fake.keyID}, validClaims())
	identity, err := client.VerifyIDToken(context.Background(), token, "expected-nonce")
	if err != nil {
		t.Fatal(err)
	}
	if identity.Subject != "1234567890" || identity.Email != "user@example.com" || !identity.EmailVerified || identity.Name != "User" {
		t.Fatalf("unexpected identity: %+v", identity)
	}
	// The key set is cached, and email_verified may arrive as a string.
	claims := validClaims()
	claims["email_verified"] = "true"
	claims["aud"] = []string{"other", "client-id"}
	claims["iss"] = "accounts.google.com"
	token = fake.token(t, map[string]any{"alg": "RS256", "kid": fake.keyID}, claims)
	if identity, err = client.VerifyIDToken(context.Background(), token, "expected-nonce"); err != nil || !identity.EmailVerified {
		t.Fatalf("string email_verified and list audience must be accepted: %v %+v", err, identity)
	}
	if fetches := fake.keyFetchs.Load(); fetches != 1 {
		t.Fatalf("the key set must be cached, fetched %d times", fetches)
	}
}

func TestVerifyIDTokenRejectsInvalidTokens(t *testing.T) {
	fake := newFakeGoogle(t)
	client := fake.client(t)
	header := map[string]any{"alg": "RS256", "kid": fake.keyID}
	mutate := func(key string, value any) map[string]any {
		claims := validClaims()
		if value == nil {
			delete(claims, key)
		} else {
			claims[key] = value
		}
		return claims
	}
	cases := map[string]string{
		"wrong issuer":   fake.token(t, header, mutate("iss", "https://evil.example")),
		"wrong audience": fake.token(t, header, mutate("aud", "someone-else")),
		"expired":        fake.token(t, header, mutate("exp", time.Now().Add(-time.Hour).Unix())),
		"no expiry":      fake.token(t, header, mutate("exp", nil)),
		"not yet valid":  fake.token(t, header, mutate("nbf", time.Now().Add(time.Hour).Unix())),
		"wrong nonce":    fake.token(t, header, mutate("nonce", "other")),
		"no subject":     fake.token(t, header, mutate("sub", "")),
		"wrong alg":      fake.token(t, map[string]any{"alg": "none", "kid": fake.keyID}, validClaims()),
		"hs256 alg":      fake.token(t, map[string]any{"alg": "HS256", "kid": fake.keyID}, validClaims()),
		"no key id":      fake.token(t, map[string]any{"alg": "RS256"}, validClaims()),
		"malformed":      "a.b",
	}
	// A token whose payload was altered after signing.
	good := fake.token(t, header, validClaims())
	parts := strings.Split(good, ".")
	tampered := validClaims()
	tampered["email"] = "attacker@example.com"
	tamperedJSON, _ := json.Marshal(tampered)
	cases["tampered payload"] = parts[0] + "." + base64.RawURLEncoding.EncodeToString(tamperedJSON) + "." + parts[2]
	for name, token := range cases {
		if _, err := client.VerifyIDToken(context.Background(), token, "expected-nonce"); err == nil {
			t.Fatalf("%s must be rejected", name)
		}
	}
	if _, err := client.VerifyIDToken(context.Background(), good, ""); err == nil {
		t.Fatal("an empty expected nonce must never match")
	}
}

func TestSigningKeyRefetchesAreRateLimited(t *testing.T) {
	fake := newFakeGoogle(t)
	client := fake.client(t)
	header := map[string]any{"alg": "RS256", "kid": "rotated"}
	token := fake.token(t, header, validClaims())
	for index := 0; index < 3; index++ {
		if _, err := client.VerifyIDToken(context.Background(), token, "expected-nonce"); err == nil {
			t.Fatal("an unknown key ID must be rejected")
		}
	}
	if fetches := fake.keyFetchs.Load(); fetches != 1 {
		t.Fatalf("unknown key IDs must not trigger repeated fetches, got %d", fetches)
	}
}

func TestGoogleDisplayNameFallsBackToTheEmailLocalPart(t *testing.T) {
	if got := googleDisplayName(GoogleIdentity{Name: "  山田 太郎 ", Email: "a@example.com"}); got != "山田 太郎" {
		t.Fatalf("unexpected name: %q", got)
	}
	if got := googleDisplayName(GoogleIdentity{Email: "taro@example.com"}); got != "taro" {
		t.Fatalf("unexpected fallback: %q", got)
	}
	if got := googleDisplayName(GoogleIdentity{Name: strings.Repeat("あ", 80), Email: "a@example.com"}); len([]rune(got)) != 64 {
		t.Fatalf("the name must be truncated to 64 characters: %d", len([]rune(got)))
	}
}
