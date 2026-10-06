package auth

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrEmailAlreadyRegistered = errors.New("email already registered")
	ErrInvalidCredentials     = errors.New("invalid credentials")
	ErrInvalidRefreshToken    = errors.New("invalid refresh token")
	ErrUnauthorized           = errors.New("unauthorized")
	errIdentityNotFound       = errors.New("identity not found")
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

type Store interface {
	GoogleStore
	AccountStore
	CreatePasswordUser(context.Context, NewPasswordUser) error
	FindPasswordIdentity(context.Context, string) (PasswordIdentity, error)
	CreateSession(context.Context, NewSession) error
	RotateSession(context.Context, []byte, RotatedSession) (uuid.UUID, error)
	RevokeSession(context.Context, []byte, []byte, time.Time) error
	FindUserByAccessToken(context.Context, []byte, time.Time) (User, error)
}

var (
	ErrMailUnavailable           = errors.New("email delivery is not configured")
	ErrInvalidToken              = errors.New("token is invalid or expired")
	ErrIdentityAlreadyLinked     = errors.New("identity is already linked")
	ErrLastAuthenticationMethod  = errors.New("the last authentication method cannot be unlinked")
	ErrAuthMethodNotLinked       = errors.New("authentication method is not linked")
	ErrGoogleUnavailable         = errors.New("google login is not configured")
	ErrInvalidOAuthCallback      = errors.New("oauth callback is invalid or expired")
	ErrInvalidAuthorizationGrant = errors.New("authorization grant is invalid or expired")
	ErrAccountLinkRequired       = errors.New("an account with this email already exists")
	errOAuthNotFound             = errors.New("oauth login not found")
)

// OAuthLogin is one Google Login attempt started by a Desktop Client.
type OAuthLogin struct {
	ID            uuid.UUID
	StateHash     []byte
	Nonce         string
	CodeChallenge string
	ClientState   string
	CreatedAt     time.Time
	ExpiresAt     time.Time
	// LinkUserID is set when the attempt links Google to a signed-in User.
	LinkUserID *uuid.UUID
}

// ExchangeGrant is the verified Google identity waiting behind an Aster Exchange Code.
type ExchangeGrant struct {
	LinkUserID    *uuid.UUID
	CodeChallenge string
	Identity      GoogleIdentity
}

// NewGoogleSignIn carries the IDs to use if the sign-in has to create a User.
type NewGoogleSignIn struct {
	Identity    GoogleIdentity
	UserID      uuid.UUID
	IdentityID  uuid.UUID
	DisplayName string
	Session     NewSession
}

// GoogleStore persists Google Login attempts and Google Identities.
type GoogleStore interface {
	CreateOAuthLogin(ctx context.Context, login OAuthLogin) error
	ClaimOAuthState(ctx context.Context, stateHash []byte, now time.Time) (OAuthLogin, error)
	StoreExchangeGrant(ctx context.Context, loginID uuid.UUID, codeHash []byte, expiresAt time.Time, identity GoogleIdentity) error
	ConsumeExchangeCode(ctx context.Context, codeHash []byte, now time.Time) (ExchangeGrant, error)
	SignInGoogle(ctx context.Context, input NewGoogleSignIn) error
}

const (
	PurposeVerifyEmail   = "VERIFY_EMAIL"
	PurposeResetPassword = "RESET_PASSWORD"
)

// EmailToken is a one-time token sent to an Email Address.
type EmailToken struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	Purpose   string
	Email     string
	TokenHash []byte
	CreatedAt time.Time
	ExpiresAt time.Time
}

type LinkGoogleInput struct {
	UserID     uuid.UUID
	IdentityID uuid.UUID
	Identity   GoogleIdentity
	Now        time.Time
}

// AccountStore persists email tokens and changes to a User's authentication methods.
type AccountStore interface {
	// CreateEmailToken invalidates the User's earlier unused tokens of the same purpose and
	// stores token. It stores nothing and returns false if one was created within cooldown.
	CreateEmailToken(ctx context.Context, token EmailToken, cooldown time.Duration) (bool, error)
	VerifyEmail(ctx context.Context, tokenHash []byte, now time.Time) error
	ResetPassword(ctx context.Context, tokenHash []byte, newPasswordHash string, now time.Time) error
	LinkGoogle(ctx context.Context, input LinkGoogleInput) (User, error)
	UnlinkAuthenticationMethod(ctx context.Context, userID uuid.UUID, provider string) error
}
