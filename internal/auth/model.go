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
	CreatePasswordUser(context.Context, NewPasswordUser) error
	FindPasswordIdentity(context.Context, string) (PasswordIdentity, error)
	CreateSession(context.Context, NewSession) error
	RotateSession(context.Context, []byte, RotatedSession) (uuid.UUID, error)
	RevokeSession(context.Context, []byte, []byte, time.Time) error
	FindUserByAccessToken(context.Context, []byte, time.Time) (User, error)
}
