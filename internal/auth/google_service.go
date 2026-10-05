package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	loginLifetime        = 5 * time.Minute
	exchangeCodeLifetime = time.Minute
	// DesktopRedirectURI is the only Redirect URI Aster Desktop may use.
	DesktopRedirectURI = "aster://auth/callback"
)

var (
	challengePattern   = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	clientStatePattern = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)
	verifierPattern    = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)
)

// WithGoogle enables Google Login.
func (s *Service) WithGoogle(client *GoogleClient) *Service {
	s.google = client
	return s
}

// GoogleCallbackResult is where the browser goes after Google answers. Cause explains a
// failure for the Server's logs and is never sent to the Client.
type GoogleCallbackResult struct {
	RedirectURL string
	Cause       error
}

// BeginGoogleLogin records the Desktop Client's PKCE challenge and returns the Google
// URL to open in the System Browser, with how many seconds the attempt stays valid.
// A non-nil linkUserID makes it a link attempt for that signed-in User.
func (s *Service) BeginGoogleLogin(ctx context.Context, codeChallenge, clientState string, linkUserID *uuid.UUID) (string, int, error) {
	if s.google == nil {
		return "", 0, ErrGoogleUnavailable
	}
	if !challengePattern.MatchString(codeChallenge) {
		return "", 0, &ValidationError{Field: "code_challenge", Message: "must be a 43 character base64url S256 challenge"}
	}
	if !clientStatePattern.MatchString(clientState) {
		return "", 0, &ValidationError{Field: "client_state", Message: "must be 43 to 128 unreserved characters"}
	}
	state, err := randomToken()
	if err != nil {
		return "", 0, err
	}
	nonce, err := randomToken()
	if err != nil {
		return "", 0, err
	}
	id, err := newUUIDv7()
	if err != nil {
		return "", 0, err
	}
	now := s.now().UTC()
	if err := s.store.CreateOAuthLogin(ctx, OAuthLogin{
		ID: id, StateHash: HashToken(state), Nonce: nonce, CodeChallenge: codeChallenge, ClientState: clientState,
		CreatedAt: now, ExpiresAt: now.Add(loginLifetime), LinkUserID: linkUserID,
	}); err != nil {
		return "", 0, err
	}
	return s.google.AuthorizationURL(state, nonce), int(loginLifetime / time.Second), nil
}

// CompleteGoogleLogin handles Google's redirect. It returns ErrInvalidOAuthCallback
// when the state is unknown, expired or already used, and the caller must not redirect.
// Otherwise the result holds the Deep Link to send the browser to.
func (s *Service) CompleteGoogleLogin(ctx context.Context, state, code, providerError string) (GoogleCallbackResult, error) {
	if s.google == nil {
		return GoogleCallbackResult{}, ErrGoogleUnavailable
	}
	if len(state) < 32 || len(state) > 512 {
		return GoogleCallbackResult{}, ErrInvalidOAuthCallback
	}
	login, err := s.store.ClaimOAuthState(ctx, HashToken(state), s.now().UTC())
	if errors.Is(err, errOAuthNotFound) {
		return GoogleCallbackResult{}, ErrInvalidOAuthCallback
	}
	if err != nil {
		return GoogleCallbackResult{}, err
	}
	failure := func(code string, cause error) GoogleCallbackResult {
		return GoogleCallbackResult{RedirectURL: deepLink(url.Values{"error": {code}, "state": {login.ClientState}}), Cause: cause}
	}
	if providerError != "" {
		if providerError == "access_denied" {
			return failure("access_denied", nil), nil
		}
		return failure("provider_error", fmt.Errorf("google returned error %q", providerError)), nil
	}
	if code == "" {
		return failure("provider_error", errors.New("google callback has no code")), nil
	}
	identity, err := s.google.Exchange(ctx, code, login.Nonce)
	if err != nil {
		return failure("provider_error", err), nil
	}
	if !identity.EmailVerified {
		return failure("provider_error", errors.New("google email address is not verified")), nil
	}
	if _, err := validateEmail(identity.Email); err != nil {
		return failure("provider_error", errors.New("google email address is not usable")), nil
	}
	exchangeCode, err := randomToken()
	if err != nil {
		return GoogleCallbackResult{}, err
	}
	if err := s.store.StoreExchangeGrant(ctx, login.ID, HashToken(exchangeCode), s.now().UTC().Add(exchangeCodeLifetime), identity); err != nil {
		return GoogleCallbackResult{}, err
	}
	return GoogleCallbackResult{RedirectURL: deepLink(url.Values{"code": {exchangeCode}, "state": {login.ClientState}})}, nil
}

// redeemExchangeCode spends an Exchange Code and checks the PKCE verifier. The code is
// spent by any attempt, successful or not.
func (s *Service) redeemExchangeCode(ctx context.Context, exchangeCode, codeVerifier string) (ExchangeGrant, error) {
	if s.google == nil {
		return ExchangeGrant{}, ErrGoogleUnavailable
	}
	if len(exchangeCode) < 32 || len(exchangeCode) > 512 || !verifierPattern.MatchString(codeVerifier) {
		return ExchangeGrant{}, ErrInvalidAuthorizationGrant
	}
	grant, err := s.store.ConsumeExchangeCode(ctx, HashToken(exchangeCode), s.now().UTC())
	if errors.Is(err, errOAuthNotFound) {
		return ExchangeGrant{}, ErrInvalidAuthorizationGrant
	}
	if err != nil {
		return ExchangeGrant{}, err
	}
	digest := sha256.Sum256([]byte(codeVerifier))
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])
	if subtle.ConstantTimeCompare([]byte(challenge), []byte(grant.CodeChallenge)) != 1 {
		return ExchangeGrant{}, ErrInvalidAuthorizationGrant
	}
	return grant, nil
}

// ExchangeGoogleLogin trades the Exchange Code and PKCE verifier for an Aster Session.
// Codes from a link attempt only work with LinkGoogleIdentity.
func (s *Service) ExchangeGoogleLogin(ctx context.Context, exchangeCode, codeVerifier string) (SessionTokens, error) {
	grant, err := s.redeemExchangeCode(ctx, exchangeCode, codeVerifier)
	if err != nil {
		return SessionTokens{}, err
	}
	if grant.LinkUserID != nil {
		return SessionTokens{}, ErrInvalidAuthorizationGrant
	}
	userID, err := newUUIDv7()
	if err != nil {
		return SessionTokens{}, err
	}
	identityID, err := newUUIDv7()
	if err != nil {
		return SessionTokens{}, err
	}
	session, tokens, err := s.issueSession(userID, s.now().UTC())
	if err != nil {
		return SessionTokens{}, err
	}
	// For an existing Google Identity the store signs in as that User instead.
	if err := s.store.SignInGoogle(ctx, NewGoogleSignIn{
		Identity: grant.Identity, UserID: userID, IdentityID: identityID,
		DisplayName: googleDisplayName(grant.Identity), Session: session,
	}); err != nil {
		return SessionTokens{}, err
	}
	return tokens, nil
}

// googleDisplayName uses the Google profile name, falling back to the email's local part.
func googleDisplayName(identity GoogleIdentity) string {
	name := strings.TrimSpace(identity.Name)
	if name == "" {
		name, _, _ = strings.Cut(identity.Email, "@")
	}
	runes := []rune(name)
	if len(runes) > 64 {
		name = string(runes[:64])
	}
	if utf8.RuneCountInString(strings.TrimSpace(name)) == 0 {
		return "Aster User"
	}
	return strings.TrimSpace(name)
}

func deepLink(query url.Values) string {
	return DesktopRedirectURI + "?" + query.Encode()
}

func randomToken() (string, error) {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(random), nil
}
