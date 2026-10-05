// Package voice issues short-lived Voice Provider Sessions and tracks who is in which
// Voice Channel. Media packets never pass through the Server.
package voice

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// SessionRequest asks a Provider for credentials to join one room as one participant.
type SessionRequest struct {
	Room        uuid.UUID
	Identity    uuid.UUID
	DisplayName string
	CanSpeak    bool
	CanStream   bool
	TTL         time.Duration
}

// ProviderSession is what a Client needs to connect to the Provider.
type ProviderSession struct {
	Provider   string
	Endpoint   string
	Credential string
	ExpiresAt  time.Time
}

// Provider is a media backend such as LiveKit. Clients pick their adapter from
// ProviderSession.Provider and connect to Endpoint directly.
type Provider interface {
	IssueSession(ctx context.Context, request SessionRequest) (ProviderSession, error)
	// CloseSession disconnects a participant. It must succeed when nobody is connected.
	CloseSession(ctx context.Context, room, identity uuid.UUID) error
}
