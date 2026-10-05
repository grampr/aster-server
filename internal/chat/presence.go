package chat

import (
	"context"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	PresenceOnline       = "ONLINE"
	PresenceIdle         = "IDLE"
	PresenceDoNotDisturb = "DO_NOT_DISTURB"
)

// Presence is a short-lived, public status. It lives in Process Memory only, so a
// restart reports everyone as offline until they publish it again.
type Presence struct {
	UserID     uuid.UUID
	Status     string
	CustomText *string
	UpdatedAt  time.Time
}

type presenceTracker struct {
	mu      sync.RWMutex
	entries map[uuid.UUID]Presence
}

func newPresenceTracker() *presenceTracker {
	return &presenceTracker{entries: make(map[uuid.UUID]Presence)}
}

func (t *presenceTracker) set(presence Presence) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.entries[presence.UserID] = presence
}

func (t *presenceTracker) get(userID uuid.UUID, fallback time.Time) Presence {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if presence, ok := t.entries[userID]; ok {
		return presence
	}
	return Presence{UserID: userID, Status: PresenceOffline, UpdatedAt: fallback}
}

// SetPresence records the caller's Presence and returns it with the Guilds whose
// Members should be notified.
func (s *Service) SetPresence(ctx context.Context, userID uuid.UUID, status string, customText *string) (Presence, []uuid.UUID, error) {
	switch status {
	case PresenceOnline, PresenceIdle, PresenceDoNotDisturb:
	default:
		return Presence{}, nil, &ValidationError{Field: "status", Message: "must be ONLINE, IDLE or DO_NOT_DISTURB"}
	}
	var text *string
	if customText != nil {
		trimmed := strings.TrimSpace(*customText)
		if trimmed != "" {
			if utf8.RuneCountInString(trimmed) > 128 {
				return Presence{}, nil, &ValidationError{Field: "custom_text", Message: "must contain at most 128 characters"}
			}
			text = &trimmed
		}
	}
	guildIDs, err := s.store.ListUserGuildIDs(ctx, userID)
	if err != nil {
		return Presence{}, nil, err
	}
	presence := Presence{UserID: userID, Status: status, CustomText: text, UpdatedAt: s.now().UTC()}
	s.presence.set(presence)
	return presence, guildIDs, nil
}

func (s *Service) applyPresence(members ...*Member) {
	for _, member := range members {
		presence := s.presence.get(member.User.ID, member.JoinedAt)
		member.Presence = &presence
	}
}

// ClearPresence forgets the caller's Presence. It returns the offline Presence and the
// Guilds to notify, or false if nothing was set.
func (s *Service) ClearPresence(ctx context.Context, userID uuid.UUID) (Presence, []uuid.UUID, bool, error) {
	if !s.presence.clear(userID) {
		return Presence{}, nil, false, nil
	}
	guildIDs, err := s.store.ListUserGuildIDs(ctx, userID)
	if err != nil {
		return Presence{}, nil, false, err
	}
	return Presence{UserID: userID, Status: PresenceOffline, UpdatedAt: s.now().UTC()}, guildIDs, true, nil
}

func (t *presenceTracker) clear(userID uuid.UUID) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, present := t.entries[userID]
	delete(t.entries, userID)
	return present
}
