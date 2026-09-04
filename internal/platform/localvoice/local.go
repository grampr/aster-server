// Package localvoice provides a development-only media adapter.
// It lets the desktop client exercise microphone, camera, and screen-share
// controls locally without issuing third-party credentials.
package localvoice

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/grampr/aster-server/internal/voice"
)

type Provider struct{}

func New() *Provider { return &Provider{} }

func (*Provider) Name() string     { return "aster-local" }
func (*Provider) Endpoint() string { return "aster-local://media" }

func (*Provider) CreateRoom(_ context.Context, _ string) (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("create local voice room ID: %w", err)
	}
	return id.String(), nil
}

func (*Provider) AddParticipant(_ context.Context, _ string, _ string, _ string, _, _ bool) (voice.ProviderParticipant, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return voice.ProviderParticipant{}, fmt.Errorf("create local voice participant ID: %w", err)
	}
	credential := make([]byte, 32)
	if _, err := rand.Read(credential); err != nil {
		return voice.ProviderParticipant{}, fmt.Errorf("create local voice credential: %w", err)
	}
	return voice.ProviderParticipant{
		ID:         id.String(),
		Credential: base64.RawURLEncoding.EncodeToString(credential),
		ExpiresAt:  time.Now().UTC().Add(time.Hour),
	}, nil
}

func (*Provider) RemoveParticipant(context.Context, string, string) error { return nil }
