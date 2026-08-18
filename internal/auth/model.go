package auth

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrEmailAlreadyRegistered    = errors.New("email already registered")
	ErrInvalidCredentials        = errors.New("invalid credentials")
	ErrInvalidRefreshToken       = errors.New("invalid refresh token")
	ErrInvalidAuthorizationGrant = errors.New("invalid authorization grant")
	ErrAccountLinkRequired       = errors.New("account link required")
	ErrGoogleUnavailable         = errors.New("google authentication unavailable")
	ErrInvalidOAuthCallback      = errors.New("invalid oauth callback")
	ErrUnauthorized              = errors.New("unauthorized")
	errIdentityNotFound          = errors.New("identity not found")
)

const (
	ProviderPassword = "PASSWORD"
	ProviderGoogle   = "GOOGLE"
)

type User struct {
	ID                    uuid.UUID
	Email                 string
	EmailVerified         bool
	DisplayName           string
	AvatarURL             *string
	AuthenticationMethods []string
	CreatedAt             time.Time
}

type PasswordIdentity struct {
	User         User
	PasswordHash string
}

type NewPasswordUser struct {
	UserID          uuid.UUID
	IdentityID      uuid.UUID
	Email           string
	NormalizedEmail string
	DisplayName     string
	PasswordHash    string
	Session         NewSession
}

type NewSession struct {
	ID               uuid.UUID
	UserID           uuid.UUID
	TokenFamilyID    uuid.UUID
	AccessTokenHash  []byte
	AccessExpiresAt  time.Time
	RefreshTokenID   uuid.UUID
	RefreshTokenHash []byte
	RefreshExpiresAt time.Time
	CreatedAt        time.Time
}

type RotatedSession struct {
	AccessTokenHash  []byte
	AccessExpiresAt  time.Time
	RefreshTokenID   uuid.UUID
	RefreshTokenHash []byte
	RefreshExpiresAt time.Time
	RotatedAt        time.Time
}

type NewGoogleLoginAttempt struct {
	ID                   uuid.UUID
	OAuthStateHash       []byte
	ClientState          string
	CodeChallenge        string
	RedirectURI          string
	Nonce                string
	ProviderCodeVerifier string
	ExpiresAt            time.Time
	CreatedAt            time.Time
}

type GoogleLoginAttempt struct {
	ID                   uuid.UUID
	ClientState          string
	CodeChallenge        string
	RedirectURI          string
	Nonce                string
	ProviderCodeVerifier string
}

type GoogleIdentity struct {
	Subject       string
	Email         string
	EmailVerified bool
	DisplayName   string
	AvatarURL     *string
}

type NewGoogleExchangeGrant struct {
	ID        uuid.UUID
	AttemptID uuid.UUID
	CodeHash  []byte
	Identity  GoogleIdentity
	ExpiresAt time.Time
	CreatedAt time.Time
}

type GoogleSessionExchange struct {
	CodeHash      []byte
	CodeChallenge string
	Session       NewSession
	ExchangedAt   time.Time
}

type Store interface {
	CreatePasswordUser(context.Context, NewPasswordUser) error
	FindPasswordIdentity(context.Context, string) (PasswordIdentity, error)
	CreateSession(context.Context, NewSession) error
	RotateSession(context.Context, []byte, RotatedSession) (uuid.UUID, error)
	RevokeSession(context.Context, []byte, []byte, time.Time) error
	FindUserByAccessToken(context.Context, []byte, time.Time) (User, error)
	CreateGoogleLoginAttempt(context.Context, NewGoogleLoginAttempt) error
	ConsumeGoogleLoginAttempt(context.Context, []byte, time.Time) (GoogleLoginAttempt, error)
	DeleteGoogleLoginAttempt(context.Context, uuid.UUID) error
	CreateGoogleExchangeGrant(context.Context, NewGoogleExchangeGrant) error
	ExchangeGoogleGrant(context.Context, GoogleSessionExchange) error
}
