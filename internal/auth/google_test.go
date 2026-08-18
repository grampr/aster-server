package auth

import (
	"net/url"
	"testing"

	"golang.org/x/oauth2"
)

func TestGoogleAuthorizationURLUsesOIDCAndProviderPKCE(t *testing.T) {
	provider, err := NewGoogleOIDCProvider("client-id", "client-secret", "https://aster.example/api/v1/auth/google/callback")
	if err != nil {
		t.Fatal(err)
	}
	verifier := oauth2.GenerateVerifier()
	authorizationURL, err := url.Parse(provider.AuthorizationURL("oauth-state", "oidc-nonce", verifier))
	if err != nil {
		t.Fatal(err)
	}
	query := authorizationURL.Query()
	if authorizationURL.String() == "" || query.Get("state") != "oauth-state" || query.Get("nonce") != "oidc-nonce" {
		t.Fatalf("missing state or nonce: %s", authorizationURL)
	}
	if query.Get("code_challenge_method") != "S256" || query.Get("code_challenge") != oauth2.S256ChallengeFromVerifier(verifier) {
		t.Fatalf("provider PKCE was not configured: %s", authorizationURL)
	}
	if query.Get("redirect_uri") != "https://aster.example/api/v1/auth/google/callback" || query.Get("scope") != "openid email profile" {
		t.Fatalf("unexpected Google authorization URL: %s", authorizationURL)
	}
}
