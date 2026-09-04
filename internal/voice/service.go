package voice

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/grampr/aster-server/internal/chat"
)

type Service struct {
	store    Store
	provider Provider
	chat     *chat.Service
	now      func() time.Time
}

func NewService(store Store, provider Provider, chatService *chat.Service) (*Service, error) {
	if store == nil || provider == nil || chatService == nil {
		return nil, errors.New("voice dependencies are required")
	}
	return &Service{store: store, provider: provider, chat: chatService, now: time.Now}, nil
}

func (s *Service) Join(ctx context.Context, userID uuid.UUID, displayName string, channelID uuid.UUID, selfMute, selfDeaf bool) (Session, error) {
	if _, err := s.chat.CanConnectVoice(ctx, userID, channelID); err != nil {
		return Session{}, err
	}
	roomID, err := s.store.GetRoom(ctx, channelID)
	if errors.Is(err, ErrNotFound) {
		created, createErr := s.provider.CreateRoom(ctx, "Aster voice "+channelID.String())
		if createErr != nil {
			return Session{}, fmt.Errorf("create voice room: %w", createErr)
		}
		roomID, err = s.store.CreateRoom(ctx, channelID, created, s.now().UTC())
	}
	if err != nil {
		return Session{}, err
	}
	sessionID, err := uuid.NewV7()
	if err != nil {
		return Session{}, err
	}
	canSpeak := s.chat.CanSpeakVoice(ctx, userID, channelID) == nil
	canStream := s.chat.CanStreamVoice(ctx, userID, channelID) == nil
	participant, err := s.provider.AddParticipant(ctx, roomID, userID.String()+":"+sessionID.String(), displayName, canSpeak, canStream)
	if err != nil {
		return Session{}, fmt.Errorf("create voice participant: %w", err)
	}
	now := s.now().UTC()
	expires := participant.ExpiresAt
	if expires.IsZero() {
		expires = now.Add(time.Hour)
	}
	state := State{UserID: userID, ChannelID: &channelID, SessionID: &sessionID, ProviderParticipantID: participant.ID, SelfMute: selfMute, SelfDeaf: selfDeaf, UpdatedAt: now, ExpiresAt: expires}
	previous, err := s.store.PutState(ctx, state)
	if err != nil {
		_ = s.provider.RemoveParticipant(ctx, roomID, participant.ID)
		return Session{}, err
	}
	if previous != nil && previous.ChannelID != nil && previous.ProviderParticipantID != "" {
		if oldRoom, roomErr := s.store.GetRoom(ctx, *previous.ChannelID); roomErr == nil {
			_ = s.provider.RemoveParticipant(ctx, oldRoom, previous.ProviderParticipantID)
		}
	}
	return Session{ID: sessionID, Provider: s.provider.Name(), Endpoint: s.provider.Endpoint(), Credential: participant.Credential, ExpiresAt: expires, State: state}, nil
}

func (s *Service) List(ctx context.Context, userID, channelID uuid.UUID) ([]State, error) {
	if _, err := s.chat.CanConnectVoice(ctx, userID, channelID); err != nil {
		return nil, err
	}
	return s.store.ListStates(ctx, channelID, s.now().UTC())
}

func (s *Service) Update(ctx context.Context, userID uuid.UUID, input UpdateInput) (State, error) {
	if input.SelfMute == nil && input.SelfDeaf == nil && input.SelfVideo == nil && input.SelfStream == nil {
		return State{}, &chat.ValidationError{Field: "body", Message: "must contain at least one field"}
	}
	current, err := s.store.GetState(ctx, userID, s.now().UTC())
	if err != nil {
		return State{}, err
	}
	if input.SelfStream != nil && *input.SelfStream && !current.SelfStream {
		if err := s.chat.CanStreamVoice(ctx, userID, *current.ChannelID); err != nil {
			return State{}, err
		}
	}
	return s.store.UpdateState(ctx, userID, input, s.now().UTC())
}

func (s *Service) Leave(ctx context.Context, userID uuid.UUID) (State, uuid.UUID, error) {
	state, err := s.store.DeleteState(ctx, userID)
	if err != nil {
		return State{}, uuid.Nil, err
	}
	if state.ChannelID == nil {
		return State{}, uuid.Nil, ErrNotFound
	}
	channelID := *state.ChannelID
	if state.ProviderParticipantID != "" {
		if roomID, roomErr := s.store.GetRoom(ctx, channelID); roomErr == nil {
			_ = s.provider.RemoveParticipant(ctx, roomID, state.ProviderParticipantID)
		}
	}
	now := s.now().UTC()
	state.ChannelID = nil
	state.SessionID = nil
	state.ProviderParticipantID = ""
	state.SelfMute = false
	state.SelfDeaf = false
	state.SelfVideo = false
	state.SelfStream = false
	state.UpdatedAt = now
	return state, channelID, nil
}
