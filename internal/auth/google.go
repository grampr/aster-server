package auth

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	googleAuthorizationEndpoint = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenEndpoint         = "https://oauth2.googleapis.com/token"
	googleJWKSEndpoint          = "https://www.googleapis.com/oauth2/v3/certs"
	googleKeyCacheTTL           = time.Hour
	idTokenLeeway               = time.Minute
	maxProviderResponseBytes    = 1 << 20
)

// GoogleConfig configures Google OpenID Connect. The endpoint fields default to
// Google's production URLs and exist so tests can point at a fake provider.
type GoogleConfig struct {
	ClientID     string
	ClientSecret string
	// RedirectURL is this Server's callback, registered with Google.
	RedirectURL           string
	AuthorizationEndpoint string
	TokenEndpoint         string
	JWKSEndpoint          string
	// Issuers are the accepted `iss` values.
	Issuers []string
}

// GoogleIdentity is what the verified ID Token says about the signed-in Google user.
type GoogleIdentity struct {
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
	Picture       string
}

// GoogleClient talks to Google. Google's tokens never leave this type.
type GoogleClient struct {
	config GoogleConfig
	client *http.Client
	now    func() time.Time

	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	keysFetch time.Time
}

func NewGoogleClient(config GoogleConfig) (*GoogleClient, error) {
	if config.ClientID == "" || config.ClientSecret == "" {
		return nil, errors.New("google client ID and secret are required")
	}
	redirect, err := url.Parse(config.RedirectURL)
	if err != nil || (redirect.Scheme != "https" && redirect.Scheme != "http") || redirect.Host == "" {
		return nil, errors.New("google redirect URL must be an http(s) URL")
	}
	if config.AuthorizationEndpoint == "" {
		config.AuthorizationEndpoint = googleAuthorizationEndpoint
	}
	if config.TokenEndpoint == "" {
		config.TokenEndpoint = googleTokenEndpoint
	}
	if config.JWKSEndpoint == "" {
		config.JWKSEndpoint = googleJWKSEndpoint
	}
	if len(config.Issuers) == 0 {
		config.Issuers = []string{"https://accounts.google.com", "accounts.google.com"}
	}
	return &GoogleClient{config: config, client: &http.Client{Timeout: 10 * time.Second}, now: time.Now}, nil
}

// AuthorizationURL builds the URL the System Browser opens.
func (g *GoogleClient) AuthorizationURL(state, nonce string) string {
	query := url.Values{
		"client_id": {g.config.ClientID}, "redirect_uri": {g.config.RedirectURL}, "response_type": {"code"},
		"scope": {"openid email profile"}, "state": {state}, "nonce": {nonce}, "prompt": {"select_account"},
	}
	return g.config.AuthorizationEndpoint + "?" + query.Encode()
}

// Exchange trades the authorization code for an ID Token and returns the verified identity.
func (g *GoogleClient) Exchange(ctx context.Context, code, nonce string) (GoogleIdentity, error) {
	form := url.Values{
		"code": {code}, "client_id": {g.config.ClientID}, "client_secret": {g.config.ClientSecret},
		"redirect_uri": {g.config.RedirectURL}, "grant_type": {"authorization_code"},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, g.config.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return GoogleIdentity{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := g.client.Do(request)
	if err != nil {
		return GoogleIdentity{}, fmt.Errorf("exchange google code: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxProviderResponseBytes))
	if err != nil {
		return GoogleIdentity{}, fmt.Errorf("read google token response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return GoogleIdentity{}, fmt.Errorf("exchange google code: status %d", response.StatusCode)
	}
	var tokens struct {
		IDToken string `json:"id_token"`
	}
	if err := json.Unmarshal(body, &tokens); err != nil || tokens.IDToken == "" {
		return GoogleIdentity{}, errors.New("google token response has no id_token")
	}
	return g.VerifyIDToken(ctx, tokens.IDToken, nonce)
}

type idTokenClaims struct {
	Issuer        string `json:"iss"`
	Audience      any    `json:"aud"`
	Subject       string `json:"sub"`
	Expires       int64  `json:"exp"`
	NotBefore     int64  `json:"nbf"`
	Nonce         string `json:"nonce"`
	Email         string `json:"email"`
	EmailVerified any    `json:"email_verified"`
	Name          string `json:"name"`
	Picture       string `json:"picture"`
}

// VerifyIDToken checks the signature, issuer, audience, expiry and nonce.
func (g *GoogleClient) VerifyIDToken(ctx context.Context, token, expectedNonce string) (GoogleIdentity, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return GoogleIdentity{}, errors.New("id token is malformed")
	}
	var header struct {
		Algorithm string `json:"alg"`
		KeyID     string `json:"kid"`
	}
	if err := decodeSegment(parts[0], &header); err != nil || header.Algorithm != "RS256" || header.KeyID == "" {
		return GoogleIdentity{}, errors.New("id token header is not RS256 with a key ID")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return GoogleIdentity{}, errors.New("id token signature is malformed")
	}
	key, err := g.signingKey(ctx, header.KeyID)
	if err != nil {
		return GoogleIdentity{}, err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature); err != nil {
		return GoogleIdentity{}, errors.New("id token signature is invalid")
	}

	var claims idTokenClaims
	if err := decodeSegment(parts[1], &claims); err != nil {
		return GoogleIdentity{}, errors.New("id token claims are malformed")
	}
	now := g.now()
	switch {
	case !contains(g.config.Issuers, claims.Issuer):
		return GoogleIdentity{}, errors.New("id token issuer is not Google")
	case !audienceMatches(claims.Audience, g.config.ClientID):
		return GoogleIdentity{}, errors.New("id token audience is not this client")
	case claims.Expires == 0 || !now.Before(time.Unix(claims.Expires, 0).Add(idTokenLeeway)):
		return GoogleIdentity{}, errors.New("id token is expired")
	case claims.NotBefore != 0 && now.Add(idTokenLeeway).Before(time.Unix(claims.NotBefore, 0)):
		return GoogleIdentity{}, errors.New("id token is not yet valid")
	case expectedNonce == "" || claims.Nonce != expectedNonce:
		return GoogleIdentity{}, errors.New("id token nonce does not match")
	case claims.Subject == "" || len(claims.Subject) > 255:
		return GoogleIdentity{}, errors.New("id token has no usable subject")
	}
	return GoogleIdentity{
		Subject: claims.Subject, Email: claims.Email, EmailVerified: truthy(claims.EmailVerified),
		Name: claims.Name, Picture: claims.Picture,
	}, nil
}

// signingKey returns Google's public key, refetching the key set once on a cache miss.
func (g *GoogleClient) signingKey(ctx context.Context, keyID string) (*rsa.PublicKey, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	fresh := g.keys != nil && g.now().Sub(g.keysFetch) < googleKeyCacheTTL
	if key, ok := g.keys[keyID]; ok && fresh {
		return key, nil
	}
	// A rotated key may be missing from a fresh cache, but refetching on every unknown
	// ID would let anyone force requests to Google, so require a minute between fetches.
	if g.keys != nil && g.now().Sub(g.keysFetch) < time.Minute {
		if key, ok := g.keys[keyID]; ok {
			return key, nil
		}
		return nil, errors.New("id token signing key is unknown")
	}
	keys, err := g.fetchKeys(ctx)
	if err != nil {
		return nil, err
	}
	g.keys, g.keysFetch = keys, g.now()
	if key, ok := keys[keyID]; ok {
		return key, nil
	}
	return nil, errors.New("id token signing key is unknown")
}

func (g *GoogleClient) fetchKeys(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, g.config.JWKSEndpoint, nil)
	if err != nil {
		return nil, err
	}
	response, err := g.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch google keys: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch google keys: status %d", response.StatusCode)
	}
	var set struct {
		Keys []struct {
			KeyType   string `json:"kty"`
			KeyID     string `json:"kid"`
			Algorithm string `json:"alg"`
			Modulus   string `json:"n"`
			Exponent  string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxProviderResponseBytes)).Decode(&set); err != nil {
		return nil, fmt.Errorf("decode google keys: %w", err)
	}
	keys := make(map[string]*rsa.PublicKey, len(set.Keys))
	for _, entry := range set.Keys {
		if entry.KeyType != "RSA" || entry.KeyID == "" || (entry.Algorithm != "" && entry.Algorithm != "RS256") {
			continue
		}
		modulus, errN := base64.RawURLEncoding.DecodeString(entry.Modulus)
		exponent, errE := base64.RawURLEncoding.DecodeString(entry.Exponent)
		if errN != nil || errE != nil || len(modulus) < 256 || len(exponent) == 0 || len(exponent) > 4 {
			continue
		}
		keys[entry.KeyID] = &rsa.PublicKey{N: new(big.Int).SetBytes(modulus), E: int(new(big.Int).SetBytes(exponent).Int64())}
	}
	if len(keys) == 0 {
		return nil, errors.New("google key set has no usable RSA keys")
	}
	return keys, nil
}

func decodeSegment(segment string, target any) error {
	raw, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// audienceMatches accepts the `aud` claim as a string or a list of strings.
func audienceMatches(audience any, clientID string) bool {
	switch value := audience.(type) {
	case string:
		return value == clientID
	case []any:
		for _, item := range value {
			if text, ok := item.(string); ok && text == clientID {
				return true
			}
		}
	}
	return false
}

// truthy reads Google's email_verified, which has been seen as both a bool and a string.
func truthy(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return typed == "true"
	}
	return false
}
