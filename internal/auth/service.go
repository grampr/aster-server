package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return e.Field + ": " + e.Message
}

type SessionTokens struct {
	AccessToken      string
	RefreshToken     string
	SessionID        uuid.UUID
	AccessExpiresIn  time.Duration
	RefreshExpiresIn time.Duration
}

type RegisterInput struct {
	Email       string
	Password    string
	DisplayName string
}

type LoginInput struct {
	Email    string
	Password string
}

type Service struct {
	store             Store
	hasher            *PasswordHasher
	tokens            TokenIssuer
	accessTTL         time.Duration
	refreshTTL        time.Duration
	now               func() time.Time
	dummyHash         string
	googleProvider    GoogleProvider
	googleAttemptTTL  time.Duration
	googleExchangeTTL time.Duration
}

const googleDesktopRedirectURI = "aster://auth/callback"

var pkceChallengePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
var pkceVerifierPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)
var oauthClientStatePattern = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)

type GoogleAuthorizationInput struct {
	RedirectURI   string
	CodeChallenge string
	ClientState   string
}

type GoogleAuthorization struct {
	URL       string
	ExpiresIn time.Duration
}

type GoogleCallbackInput struct {
	Code  string
	State string
	Error string
}

type GoogleCallbackResult struct {
	RedirectURI  string
	ClientState  string
	ExchangeCode string
	ErrorCode    string
}

func NewService(store Store, hasher *PasswordHasher, accessTTL, refreshTTL time.Duration) (*Service, error) {
	if store == nil || hasher == nil {
		return nil, errors.New("auth store and password hasher are required")
	}
	if accessTTL <= 0 || refreshTTL <= accessTTL {
		return nil, errors.New("refresh TTL must be greater than access TTL")
	}
	dummyHash, err := hasher.Hash("this password is only used to equalize login timing")
	if err != nil {
		return nil, fmt.Errorf("create dummy password hash: %w", err)
	}
	return &Service{
		store: store, hasher: hasher, tokens: TokenIssuer{},
		accessTTL: accessTTL, refreshTTL: refreshTTL,
		now: time.Now, dummyHash: dummyHash,
	}, nil
}

func (s *Service) EnableGoogle(provider GoogleProvider, attemptTTL, exchangeTTL time.Duration) error {
	if provider == nil {
		return errors.New("google provider is required")
	}
	if attemptTTL <= 0 || attemptTTL > 10*time.Minute || exchangeTTL <= 0 || exchangeTTL > 10*time.Minute {
		return errors.New("google attempt and exchange TTLs must be between zero and ten minutes")
	}
	s.googleProvider = provider
	s.googleAttemptTTL = attemptTTL
	s.googleExchangeTTL = exchangeTTL
	return nil
}

func (s *Service) BeginGoogleAuthorization(ctx context.Context, input GoogleAuthorizationInput) (GoogleAuthorization, error) {
	if s.googleProvider == nil {
		return GoogleAuthorization{}, ErrGoogleUnavailable
	}
	if input.RedirectURI != googleDesktopRedirectURI {
		return GoogleAuthorization{}, &ValidationError{Field: "redirect_uri", Message: "must be aster://auth/callback"}
	}
	if !pkceChallengePattern.MatchString(input.CodeChallenge) {
		return GoogleAuthorization{}, &ValidationError{Field: "code_challenge", Message: "must be a valid PKCE S256 challenge"}
	}
	if !oauthClientStatePattern.MatchString(input.ClientState) {
		return GoogleAuthorization{}, &ValidationError{Field: "client_state", Message: "must contain between 43 and 128 unreserved characters"}
	}
	oauthState, err := NewRandomValue()
	if err != nil {
		return GoogleAuthorization{}, err
	}
	nonce, err := NewRandomValue()
	if err != nil {
		return GoogleAuthorization{}, err
	}
	providerVerifier, err := NewRandomValue()
	if err != nil {
		return GoogleAuthorization{}, err
	}
	attemptID, err := newUUIDv7()
	if err != nil {
		return GoogleAuthorization{}, err
	}
	now := s.now().UTC()
	if err := s.store.CreateGoogleLoginAttempt(ctx, NewGoogleLoginAttempt{
		ID: attemptID, OAuthStateHash: HashToken(oauthState), ClientState: input.ClientState,
		CodeChallenge: input.CodeChallenge, RedirectURI: input.RedirectURI, Nonce: nonce,
		ProviderCodeVerifier: providerVerifier, ExpiresAt: now.Add(s.googleAttemptTTL), CreatedAt: now,
	}); err != nil {
		return GoogleAuthorization{}, err
	}
	return GoogleAuthorization{
		URL:       s.googleProvider.AuthorizationURL(oauthState, nonce, providerVerifier),
		ExpiresIn: s.googleAttemptTTL,
	}, nil
}

func (s *Service) CompleteGoogleAuthorization(ctx context.Context, input GoogleCallbackInput) (GoogleCallbackResult, error) {
	if s.googleProvider == nil {
		return GoogleCallbackResult{}, ErrGoogleUnavailable
	}
	if input.State == "" || len(input.State) > 512 || len(input.Code) > 4096 || len(input.Error) > 256 ||
		(input.Code == "" && input.Error == "") || (input.Code != "" && input.Error != "") {
		return GoogleCallbackResult{}, ErrInvalidOAuthCallback
	}
	attempt, err := s.store.ConsumeGoogleLoginAttempt(ctx, HashToken(input.State), s.now().UTC())
	if err != nil {
		return GoogleCallbackResult{}, err
	}
	baseResult := GoogleCallbackResult{RedirectURI: attempt.RedirectURI, ClientState: attempt.ClientState}
	if input.Error != "" {
		if input.Error == "access_denied" {
			baseResult.ErrorCode = "access_denied"
		} else {
			baseResult.ErrorCode = "provider_error"
		}
		return baseResult, s.store.DeleteGoogleLoginAttempt(ctx, attempt.ID)
	}
	identity, err := s.googleProvider.VerifyAuthorization(ctx, input.Code, attempt.ProviderCodeVerifier, attempt.Nonce)
	if err != nil {
		baseResult.ErrorCode = "provider_error"
		return baseResult, errors.Join(err, s.store.DeleteGoogleLoginAttempt(ctx, attempt.ID))
	}
	exchangeCode, exchangeHash, err := s.tokens.NewExchangeCode()
	if err != nil {
		baseResult.ErrorCode = "provider_error"
		return baseResult, errors.Join(err, s.store.DeleteGoogleLoginAttempt(ctx, attempt.ID))
	}
	grantID, err := newUUIDv7()
	if err != nil {
		baseResult.ErrorCode = "provider_error"
		return baseResult, errors.Join(err, s.store.DeleteGoogleLoginAttempt(ctx, attempt.ID))
	}
	now := s.now().UTC()
	if err := s.store.CreateGoogleExchangeGrant(ctx, NewGoogleExchangeGrant{
		ID: grantID, AttemptID: attempt.ID, CodeHash: exchangeHash, Identity: identity,
		ExpiresAt: now.Add(s.googleExchangeTTL), CreatedAt: now,
	}); err != nil {
		baseResult.ErrorCode = "provider_error"
		return baseResult, errors.Join(err, s.store.DeleteGoogleLoginAttempt(ctx, attempt.ID))
	}
	baseResult.ExchangeCode = exchangeCode
	return baseResult, nil
}

func (s *Service) ExchangeGoogleAuthorization(ctx context.Context, exchangeCode, codeVerifier string) (SessionTokens, error) {
	if s.googleProvider == nil {
		return SessionTokens{}, ErrGoogleUnavailable
	}
	if exchangeCode == "" || len(exchangeCode) > 512 || !pkceVerifierPattern.MatchString(codeVerifier) {
		return SessionTokens{}, ErrInvalidAuthorizationGrant
	}
	now := s.now().UTC()
	session, tokens, err := s.issueSession(uuid.Nil, now)
	if err != nil {
		return SessionTokens{}, err
	}
	challengeHash := sha256.Sum256([]byte(codeVerifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeHash[:])
	if err := s.store.ExchangeGoogleGrant(ctx, GoogleSessionExchange{
		CodeHash: HashToken(exchangeCode), CodeChallenge: challenge,
		Session: session, ExchangedAt: now,
	}); err != nil {
		return SessionTokens{}, err
	}
	return tokens, nil
}

func (s *Service) Register(ctx context.Context, input RegisterInput) (SessionTokens, error) {
	email, err := validateEmail(input.Email)
	if err != nil {
		return SessionTokens{}, err
	}
	if err := validatePassword(input.Password); err != nil {
		return SessionTokens{}, err
	}
	displayName, err := validateDisplayName(input.DisplayName)
	if err != nil {
		return SessionTokens{}, err
	}

	passwordHash, err := s.hasher.Hash(input.Password)
	if err != nil {
		return SessionTokens{}, fmt.Errorf("hash password: %w", err)
	}
	now := s.now().UTC()
	userID, err := newUUIDv7()
	if err != nil {
		return SessionTokens{}, err
	}
	identityID, err := newUUIDv7()
	if err != nil {
		return SessionTokens{}, err
	}
	session, tokens, err := s.issueSession(userID, now)
	if err != nil {
		return SessionTokens{}, err
	}

	err = s.store.CreatePasswordUser(ctx, NewPasswordUser{
		UserID: userID, IdentityID: identityID,
		Email: strings.TrimSpace(input.Email), NormalizedEmail: email,
		DisplayName: displayName, PasswordHash: passwordHash, Session: session,
	})
	if err != nil {
		return SessionTokens{}, err
	}
	return tokens, nil
}

func (s *Service) Login(ctx context.Context, input LoginInput) (SessionTokens, error) {
	email, err := validateEmail(input.Email)
	if err != nil {
		return SessionTokens{}, ErrInvalidCredentials
	}
	if err := validatePassword(input.Password); err != nil {
		return SessionTokens{}, ErrInvalidCredentials
	}

	identity, err := s.store.FindPasswordIdentity(ctx, email)
	if errors.Is(err, errIdentityNotFound) {
		_, _ = s.hasher.Verify(input.Password, s.dummyHash)
		return SessionTokens{}, ErrInvalidCredentials
	}
	if err != nil {
		return SessionTokens{}, err
	}
	valid, err := s.hasher.Verify(input.Password, identity.PasswordHash)
	if err != nil {
		return SessionTokens{}, fmt.Errorf("verify stored password hash: %w", err)
	}
	if !valid {
		return SessionTokens{}, ErrInvalidCredentials
	}

	now := s.now().UTC()
	session, tokens, err := s.issueSession(identity.User.ID, now)
	if err != nil {
		return SessionTokens{}, err
	}
	if err := s.store.CreateSession(ctx, session); err != nil {
		return SessionTokens{}, err
	}
	return tokens, nil
}

func (s *Service) Refresh(ctx context.Context, refreshToken string) (SessionTokens, error) {
	if refreshToken == "" {
		return SessionTokens{}, ErrInvalidRefreshToken
	}
	now := s.now().UTC()
	accessToken, accessHash, err := s.tokens.NewAccessToken()
	if err != nil {
		return SessionTokens{}, err
	}
	nextRefreshToken, refreshHash, err := s.tokens.NewRefreshToken()
	if err != nil {
		return SessionTokens{}, err
	}
	refreshID, err := newUUIDv7()
	if err != nil {
		return SessionTokens{}, err
	}
	rotation := RotatedSession{
		AccessTokenHash: accessHash, AccessExpiresAt: now.Add(s.accessTTL),
		RefreshTokenID: refreshID, RefreshTokenHash: refreshHash,
		RefreshExpiresAt: now.Add(s.refreshTTL), RotatedAt: now,
	}
	sessionID, err := s.store.RotateSession(ctx, HashToken(refreshToken), rotation)
	if err != nil {
		return SessionTokens{}, err
	}
	return SessionTokens{
		AccessToken: accessToken, RefreshToken: nextRefreshToken,
		SessionID: sessionID, AccessExpiresIn: s.accessTTL, RefreshExpiresIn: s.refreshTTL,
	}, nil
}

func (s *Service) Logout(ctx context.Context, accessToken, refreshToken string) error {
	if accessToken == "" || refreshToken == "" {
		return ErrUnauthorized
	}
	return s.store.RevokeSession(ctx, HashToken(accessToken), HashToken(refreshToken), s.now().UTC())
}

func (s *Service) Authenticate(ctx context.Context, accessToken string) (User, error) {
	if accessToken == "" {
		return User{}, ErrUnauthorized
	}
	return s.store.FindUserByAccessToken(ctx, HashToken(accessToken), s.now().UTC())
}

func (s *Service) issueSession(userID uuid.UUID, now time.Time) (NewSession, SessionTokens, error) {
	sessionID, err := newUUIDv7()
	if err != nil {
		return NewSession{}, SessionTokens{}, err
	}
	familyID, err := newUUIDv7()
	if err != nil {
		return NewSession{}, SessionTokens{}, err
	}
	refreshID, err := newUUIDv7()
	if err != nil {
		return NewSession{}, SessionTokens{}, err
	}
	accessToken, accessHash, err := s.tokens.NewAccessToken()
	if err != nil {
		return NewSession{}, SessionTokens{}, err
	}
	refreshToken, refreshHash, err := s.tokens.NewRefreshToken()
	if err != nil {
		return NewSession{}, SessionTokens{}, err
	}
	session := NewSession{
		ID: sessionID, UserID: userID, TokenFamilyID: familyID,
		AccessTokenHash: accessHash, AccessExpiresAt: now.Add(s.accessTTL),
		RefreshTokenID: refreshID, RefreshTokenHash: refreshHash,
		RefreshExpiresAt: now.Add(s.refreshTTL), CreatedAt: now,
	}
	return session, SessionTokens{
		AccessToken: accessToken, RefreshToken: refreshToken, SessionID: sessionID,
		AccessExpiresIn: s.accessTTL, RefreshExpiresIn: s.refreshTTL,
	}, nil
}

func validateEmail(value string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" || len(normalized) > 320 {
		return "", &ValidationError{Field: "email", Message: "must be a valid email address"}
	}
	address, err := mail.ParseAddress(normalized)
	if err != nil || address.Address != normalized {
		return "", &ValidationError{Field: "email", Message: "must be a valid email address"}
	}
	return normalized, nil
}

func validatePassword(value string) error {
	length := utf8.RuneCountInString(value)
	if length < 15 || length > 128 {
		return &ValidationError{Field: "password", Message: "must contain between 15 and 128 characters"}
	}
	return nil
}

func validateDisplayName(value string) (string, error) {
	value = strings.TrimSpace(value)
	length := utf8.RuneCountInString(value)
	if length < 1 || length > 64 {
		return "", &ValidationError{Field: "display_name", Message: "must contain between 1 and 64 characters"}
	}
	return value, nil
}

func newUUIDv7() (uuid.UUID, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, fmt.Errorf("generate UUIDv7: %w", err)
	}
	return id, nil
}
