package voice

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

var ErrNotFound = errors.New("voice state not found")

type State struct {
	UserID                uuid.UUID
	ChannelID             *uuid.UUID
	SessionID             *uuid.UUID
	ProviderParticipantID string
	SelfMute              bool
	SelfDeaf              bool
	SelfVideo             bool
	SelfStream            bool
	UpdatedAt             time.Time
	ExpiresAt             time.Time
}

type Session struct {
	ID                             uuid.UUID
	Provider, Endpoint, Credential string
	ExpiresAt                      time.Time
	State                          State
}
type ProviderParticipant struct {
	ID, Credential string
	ExpiresAt      time.Time
}

type Provider interface {
	Name() string
	Endpoint() string
	CreateRoom(context.Context, string) (string, error)
	AddParticipant(context.Context, string, string, string, bool, bool) (ProviderParticipant, error)
	RemoveParticipant(context.Context, string, string) error
}

type Store interface {
	GetRoom(context.Context, uuid.UUID) (string, error)
	CreateRoom(context.Context, uuid.UUID, string, time.Time) (string, error)
	PutState(context.Context, State) (*State, error)
	GetState(context.Context, uuid.UUID, time.Time) (State, error)
	ListStates(context.Context, uuid.UUID, time.Time) ([]State, error)
	UpdateState(context.Context, uuid.UUID, UpdateInput, time.Time) (State, error)
	DeleteState(context.Context, uuid.UUID) (State, error)
}

type UpdateInput struct{ SelfMute, SelfDeaf, SelfVideo, SelfStream *bool }
