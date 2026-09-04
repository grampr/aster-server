package gateway

import (
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
)

var errSessionCapacity = errors.New("gateway session capacity reached")

const (
	maxSessions        = 10_000
	maxSessionsPerUser = 10
)

type storedEvent struct {
	sequence int64
	payload  []byte
}

type session struct {
	id             uuid.UUID
	userID         uuid.UUID
	intents        int64
	sequence       int64
	events         []storedEvent
	client         *client
	disconnectedAt *time.Time
}

type hub struct {
	mu               sync.Mutex
	sessions         map[uuid.UUID]*session
	sessionsByUser   map[uuid.UUID]map[uuid.UUID]*session
	gatewayURL       string
	sessionRetention time.Duration
	eventBufferSize  int
	now              func() time.Time
}

func newHub(gatewayURL string, sessionRetention time.Duration, eventBufferSize int) *hub {
	return &hub{
		sessions: make(map[uuid.UUID]*session), sessionsByUser: make(map[uuid.UUID]map[uuid.UUID]*session),
		gatewayURL: gatewayURL, sessionRetention: sessionRetention, eventBufferSize: eventBufferSize, now: time.Now,
	}
}

func (h *hub) identify(userID uuid.UUID, intents int64, connection *client) (uuid.UUID, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneLocked()
	if len(h.sessions) >= maxSessions || len(h.sessionsByUser[userID]) >= maxSessionsPerUser {
		return uuid.Nil, errSessionCapacity
	}

	sessionID, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, err
	}
	s := &session{id: sessionID, userID: userID, intents: intents, client: connection, sequence: 0}
	h.sessions[sessionID] = s
	if h.sessionsByUser[userID] == nil {
		h.sessionsByUser[userID] = make(map[uuid.UUID]*session)
	}
	h.sessionsByUser[userID][sessionID] = s

	sequence := int64(0)
	payload, err := json.Marshal(gatewayMessage{
		Op: opDispatch, T: eventReady, S: &sequence,
		D: map[string]any{"session_id": sessionID, "resume_gateway_url": h.gatewayURL},
	})
	if err != nil {
		h.removeLocked(s)
		return uuid.Nil, err
	}
	h.appendEventLocked(s, storedEvent{sequence: sequence, payload: payload})
	if !connection.enqueue(payload) {
		h.detachLocked(s, connection)
	}
	return sessionID, nil
}

func (h *hub) resume(userID, sessionID uuid.UUID, afterSequence int64, connection *client) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneLocked()

	s := h.sessions[sessionID]
	if s == nil || s.userID != userID || afterSequence < 0 || afterSequence > s.sequence {
		return false
	}
	if len(s.events) > 0 && afterSequence < s.events[0].sequence-1 {
		return false
	}
	if s.client != nil && s.client != connection {
		s.client.terminate()
	}
	s.client = connection
	s.disconnectedAt = nil
	for _, event := range s.events {
		if event.sequence > afterSequence && !connection.enqueue(event.payload) {
			h.detachLocked(s, connection)
			return false
		}
	}
	return h.dispatchLocked(s, eventResumed, struct{}{})
}

func (h *hub) detach(sessionID uuid.UUID, connection *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s := h.sessions[sessionID]; s != nil {
		h.detachLocked(s, connection)
	}
}

func (h *hub) detachLocked(s *session, connection *client) {
	if s.client != connection {
		return
	}
	s.client = nil
	now := h.now().UTC()
	s.disconnectedAt = &now
}

func (h *hub) publishMessage(eventName string, recipients []uuid.UUID, message Message) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneLocked()

	seen := make(map[uuid.UUID]struct{}, len(recipients))
	for _, userID := range recipients {
		if _, exists := seen[userID]; exists {
			continue
		}
		seen[userID] = struct{}{}
		for _, s := range h.sessionsByUser[userID] {
			if s.intents&intentGuildMessages == 0 {
				continue
			}
			payload := messageEventPayload(message, s.intents&intentMessageContent != 0)
			h.dispatchLocked(s, eventName, payload)
		}
	}
}

func (h *hub) publishMessageDelete(recipients []uuid.UUID, messageID, channelID uuid.UUID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneLocked()

	seen := make(map[uuid.UUID]struct{}, len(recipients))
	for _, userID := range recipients {
		if _, exists := seen[userID]; exists {
			continue
		}
		seen[userID] = struct{}{}
		for _, s := range h.sessionsByUser[userID] {
			if s.intents&intentGuildMessages != 0 {
				h.dispatchLocked(s, eventMessageDelete, messageDeletePayload{ID: messageID, ChannelID: channelID})
			}
		}
	}
}

func (h *hub) publishMessageReaction(eventName string, recipients []uuid.UUID, reaction MessageReaction) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneLocked()

	payload := messageReactionPayload{
		MessageID: reaction.MessageID, ChannelID: reaction.ChannelID, UserID: reaction.UserID,
		Emoji: reaction.Emoji, Count: reaction.Count,
	}
	seen := make(map[uuid.UUID]struct{}, len(recipients))
	for _, userID := range recipients {
		if _, exists := seen[userID]; exists {
			continue
		}
		seen[userID] = struct{}{}
		for _, s := range h.sessionsByUser[userID] {
			if s.intents&intentReactions != 0 {
				h.dispatchLocked(s, eventName, payload)
			}
		}
	}
}

func (h *hub) publishTypingStart(recipients []uuid.UUID, typing TypingStart) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneLocked()

	payload := typingStartPayload{
		ChannelID: typing.ChannelID,
		User: userPayload{
			ID: typing.User.ID, DisplayName: typing.User.DisplayName, AvatarURL: typing.User.AvatarURL,
		},
		StartedAt: typing.StartedAt,
	}
	seen := make(map[uuid.UUID]struct{}, len(recipients))
	for _, userID := range recipients {
		if _, exists := seen[userID]; exists {
			continue
		}
		seen[userID] = struct{}{}
		for _, s := range h.sessionsByUser[userID] {
			if s.intents&intentTyping != 0 {
				h.dispatchLocked(s, eventTypingStart, payload)
			}
		}
	}
}

func (h *hub) dispatchLocked(s *session, eventName string, data any) bool {
	s.sequence++
	sequence := s.sequence
	payload, err := json.Marshal(gatewayMessage{Op: opDispatch, T: eventName, S: &sequence, D: data})
	if err != nil {
		return false
	}
	h.appendEventLocked(s, storedEvent{sequence: sequence, payload: payload})
	if s.client != nil && !s.client.enqueue(payload) {
		h.detachLocked(s, s.client)
		return false
	}
	return true
}

func (h *hub) appendEventLocked(s *session, event storedEvent) {
	s.events = append(s.events, event)
	if len(s.events) > h.eventBufferSize {
		s.events = append([]storedEvent(nil), s.events[len(s.events)-h.eventBufferSize:]...)
	}
}

func (h *hub) pruneLocked() {
	now := h.now().UTC()
	for _, s := range h.sessions {
		if s.disconnectedAt != nil && now.Sub(*s.disconnectedAt) >= h.sessionRetention {
			h.removeLocked(s)
		}
	}
}

func (h *hub) removeLocked(s *session) {
	delete(h.sessions, s.id)
	delete(h.sessionsByUser[s.userID], s.id)
	if len(h.sessionsByUser[s.userID]) == 0 {
		delete(h.sessionsByUser, s.userID)
	}
}
