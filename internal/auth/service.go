package auth

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
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
	store      Store
	hasher     *PasswordHasher
	tokens     TokenIssuer
	accessTTL  time.Duration
	refreshTTL time.Duration
	now        func() time.Time
	dummyHash  string
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
