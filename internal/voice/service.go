package voice

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/grampr/aster-server/internal/chat"
)

const sessionTTL = 10 * time.Minute

// ErrUnavailable means no Voice Provider is configured.
var ErrUnavailable = errors.New("voice provider is not configured")

// State is a User's public Voice state. ChannelID and SessionID are nil once they leave.
type State struct {
	UserID     uuid.UUID
	ChannelID  *uuid.UUID
	SessionID  *uuid.UUID
	SelfMute   bool
	SelfDeaf   bool
	SelfVideo  bool
	SelfStream bool
	UpdatedAt  time.Time
}

// Session is what a Client needs to connect to the Provider, plus its Voice state.
type Session struct {
	ID uuid.UUID
	ProviderSession
	State State
}

// Channels is the part of the chat Service the Voice Service relies on.
type Channels interface {
	AuthorizeVoice(ctx context.Context, userID, channelID uuid.UUID) (chat.Channel, chat.Access, error)
	ViewVoiceChannel(ctx context.Context, userID, channelID uuid.UUID) (chat.Channel, error)
}

// Update changes the public flags of a Voice state; nil fields stay as they are.
type Update struct {
	SelfMute   *bool
	SelfDeaf   *bool
	SelfVideo  *bool
	SelfStream *bool
}

type entry struct {
	state   State
	guildID uuid.UUID
}

// Service tracks Voice states in Process Memory, like Presence, and brokers Provider Sessions.
type Service struct {
	channels Channels
	provider Provider
	logger   *slog.Logger
	now      func() time.Time

	mu      sync.Mutex
	entries map[uuid.UUID]entry
	notify  func(guildID uuid.UUID, state State)
}

// New returns a Service. A nil provider makes every operation report ErrUnavailable.
func New(channels Channels, provider Provider, logger *slog.Logger) *Service {
	return &Service{channels: channels, provider: provider, logger: logger, now: time.Now, entries: make(map[uuid.UUID]entry)}
}

// SetNotifier registers the callback that publishes every Voice state change.
func (s *Service) SetNotifier(notify func(guildID uuid.UUID, state State)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notify = notify
}

// Join puts the caller in a Voice Channel and returns the Provider Session. Joining
// while in another Channel moves them.
func (s *Service) Join(ctx context.Context, userID uuid.UUID, displayName string, channelID uuid.UUID, selfMute, selfDeaf bool) (Session, error) {
	if s.provider == nil {
		return Session{}, ErrUnavailable
	}
	channel, access, err := s.channels.AuthorizeVoice(ctx, userID, channelID)
	if err != nil {
		return Session{}, err
	}
	sessionID, err := uuid.NewV7()
	if err != nil {
		return Session{}, err
	}
	provider, err := s.provider.IssueSession(ctx, SessionRequest{
		Room: channelID, Identity: userID, DisplayName: displayName,
		CanSpeak: access.Has(chat.PermSpeak), CanStream: access.Has(chat.PermStream), TTL: sessionTTL,
	})
	if err != nil {
		return Session{}, err
	}
	state := State{UserID: userID, ChannelID: &channelID, SessionID: &sessionID, SelfMute: selfMute, SelfDeaf: selfDeaf, UpdatedAt: s.now().UTC()}

	s.mu.Lock()
	previous, wasPresent := s.entries[userID]
	s.entries[userID] = entry{state: state, guildID: *channel.GuildID}
	notify := s.notify
	s.mu.Unlock()

	if wasPresent && *previous.state.ChannelID != channelID {
		s.closeProvider(ctx, previous)
		if notify != nil {
			notify(previous.guildID, leftState(userID, s.now().UTC()))
		}
	}
	if notify != nil {
		notify(*channel.GuildID, state)
	}
	return Session{ID: sessionID, ProviderSession: provider, State: state}, nil
}

// UpdateState changes the caller's flags in their current Voice Channel.
func (s *Service) UpdateState(ctx context.Context, userID uuid.UUID, update Update) (State, error) {
	s.mu.Lock()
	current, present := s.entries[userID]
	s.mu.Unlock()
	if !present {
		return State{}, chat.ErrNotFound
	}
	_, access, err := s.channels.AuthorizeVoice(ctx, userID, *current.state.ChannelID)
	if err != nil {
		// The member lost access (kicked, role changed, channel deleted): drop them.
		s.Evict(ctx, userID)
		return State{}, err
	}
	if (isTrue(update.SelfVideo) || isTrue(update.SelfStream)) && !access.Has(chat.PermStream) {
		return State{}, chat.ErrForbidden
	}

	s.mu.Lock()
	entryNow, present := s.entries[userID]
	if !present || *entryNow.state.SessionID != *current.state.SessionID {
		s.mu.Unlock()
		return State{}, chat.ErrNotFound
	}
	state := entryNow.state
	apply(&state.SelfMute, update.SelfMute)
	apply(&state.SelfDeaf, update.SelfDeaf)
	apply(&state.SelfVideo, update.SelfVideo)
	apply(&state.SelfStream, update.SelfStream)
	state.UpdatedAt = s.now().UTC()
	entryNow.state = state
	s.entries[userID] = entryNow
	notify := s.notify
	s.mu.Unlock()

	if notify != nil {
		notify(entryNow.guildID, state)
	}
	return state, nil
}

// Leave removes the caller from their Voice Channel.
func (s *Service) Leave(ctx context.Context, userID uuid.UUID) error {
	if !s.Evict(ctx, userID) {
		return chat.ErrNotFound
	}
	return nil
}

// Evict removes a User from Voice, closes their Provider Session and tells the Guild.
// It reports whether the User was in a Voice Channel.
func (s *Service) Evict(ctx context.Context, userID uuid.UUID) bool {
	s.mu.Lock()
	previous, present := s.entries[userID]
	delete(s.entries, userID)
	notify := s.notify
	s.mu.Unlock()
	if !present {
		return false
	}
	s.closeProvider(ctx, previous)
	if notify != nil {
		notify(previous.guildID, leftState(userID, s.now().UTC()))
	}
	return true
}

// List returns who is in a Voice Channel the caller can see.
func (s *Service) List(ctx context.Context, userID, channelID uuid.UUID) ([]State, error) {
	if _, err := s.channels.ViewVoiceChannel(ctx, userID, channelID); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	states := make([]State, 0)
	for _, item := range s.entries {
		if *item.state.ChannelID == channelID {
			states = append(states, item.state)
		}
	}
	sortStates(states)
	return states, nil
}

func (s *Service) closeProvider(ctx context.Context, item entry) {
	if s.provider == nil {
		return
	}
	if err := s.provider.CloseSession(ctx, *item.state.ChannelID, item.state.UserID); err != nil {
		s.logger.Error("close voice provider session", "channel_id", *item.state.ChannelID, "user_id", item.state.UserID, "error", err)
	}
}

func leftState(userID uuid.UUID, at time.Time) State {
	return State{UserID: userID, UpdatedAt: at}
}

func apply(target *bool, value *bool) {
	if value != nil {
		*target = *value
	}
}

func isTrue(value *bool) bool { return value != nil && *value }
