package httpapi

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
	protocolgo "github.com/grampr/Aster-protocol/packages/protocol-go/generated"
	"github.com/grampr/aster-server/internal/gateway"
	"github.com/grampr/aster-server/internal/voice"
)

// Option customizes the Server beyond the core services.
type Option func(*Server)

// WithVoice enables the Voice APIs and publishes Voice state changes.
func WithVoice(service *voice.Service) Option {
	return func(server *Server) { server.voice = service }
}

// voiceRoute reports the Voice APIs as unavailable when no Voice Service is wired in.
func (s *Server) voiceRoute(handler http.HandlerFunc) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if s.voice == nil {
			s.handleChatError(writer, request, voice.ErrUnavailable)
			return
		}
		handler(writer, request)
	}
}

func (s *Server) joinVoiceChannel(writer http.ResponseWriter, request *http.Request) {
	user, ok := s.chatUser(writer, request, "voice_join")
	if !ok {
		return
	}
	channelID, ok := s.pathID(writer, request, "channel_id")
	if !ok {
		return
	}
	var body protocolgo.JoinVoiceChannelRequest
	if err := decodeOptionalJSON(writer, request, &body); err != nil {
		s.writeError(writer, request, http.StatusBadRequest, "INVALID_REQUEST", "Request body is invalid", err)
		return
	}
	session, err := s.voice.Join(request.Context(), user.ID, user.DisplayName, channelID, body.SelfMute != nil && *body.SelfMute, body.SelfDeaf != nil && *body.SelfDeaf)
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	// The credential is a bearer secret: it is never logged and must not be cached.
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, protocolgo.VoiceSession{
		Id: session.ID, Provider: session.Provider, Endpoint: session.Endpoint, Credential: session.Credential,
		ExpiresAt: session.ExpiresAt, State: voiceStateResponse(session.State),
	})
}

func (s *Server) listVoiceStates(writer http.ResponseWriter, request *http.Request) {
	user, ok := s.chatUser(writer, request, "voice_list")
	if !ok {
		return
	}
	channelID, ok := s.pathID(writer, request, "channel_id")
	if !ok {
		return
	}
	states, err := s.voice.List(request.Context(), user.ID, channelID)
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	items := make([]protocolgo.VoiceState, len(states))
	for index := range states {
		items[index] = voiceStateResponse(states[index])
	}
	writeJSON(writer, http.StatusOK, protocolgo.VoiceStateList{Items: items})
}

func (s *Server) updateVoiceState(writer http.ResponseWriter, request *http.Request) {
	user, ok := s.chatUser(writer, request, "voice_update")
	if !ok {
		return
	}
	var body protocolgo.UpdateVoiceStateRequest
	if err := decodeJSON(writer, request, &body); err != nil {
		s.writeError(writer, request, http.StatusBadRequest, "INVALID_REQUEST", "Request body is invalid", err)
		return
	}
	if body.SelfMute == nil && body.SelfDeaf == nil && body.SelfVideo == nil && body.SelfStream == nil {
		s.writeError(writer, request, http.StatusBadRequest, "INVALID_REQUEST", "body: must contain at least one field", nil)
		return
	}
	state, err := s.voice.UpdateState(request.Context(), user.ID, voice.Update{
		SelfMute: body.SelfMute, SelfDeaf: body.SelfDeaf, SelfVideo: body.SelfVideo, SelfStream: body.SelfStream,
	})
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, voiceStateResponse(state))
}

func (s *Server) leaveVoiceChannel(writer http.ResponseWriter, request *http.Request) {
	user, ok := s.chatUser(writer, request, "voice_leave")
	if !ok {
		return
	}
	if err := s.voice.Leave(request.Context(), user.ID); err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

// publishVoiceState tells every Member of the Guild about a Voice state change.
func (s *Server) publishVoiceState(guildID uuid.UUID, state voice.State) {
	if s.gateway == nil {
		return
	}
	publishContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	recipients, err := s.chat.ListGuildMemberIDs(publishContext, guildID)
	if err != nil {
		s.logger.Error("list gateway voice recipients", "guild_id", guildID, "error", err)
		return
	}
	s.gateway.PublishVoiceStateUpdate(recipients, gateway.VoiceState(state))
}

// userGone cleans up short-lived state once a User has no Gateway Session left.
func (s *Server) userGone(userID uuid.UUID) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if s.voice != nil {
		s.voice.Evict(ctx, userID)
	}
	presence, guildIDs, cleared, err := s.chat.ClearPresence(ctx, userID)
	if err != nil {
		s.logger.Error("clear presence", "user_id", userID, "error", err)
		return
	}
	if !cleared || s.gateway == nil {
		return
	}
	payload := gateway.Presence{UserID: presence.UserID, Status: presence.Status, UpdatedAt: presence.UpdatedAt}
	for _, guildID := range guildIDs {
		recipients, err := s.chat.ListGuildMemberIDs(ctx, guildID)
		if err != nil {
			s.logger.Error("list gateway presence recipients", "guild_id", guildID, "error", err)
			continue
		}
		s.gateway.PublishPresenceUpdate(recipients, guildID, payload)
	}
}

func voiceStateResponse(state voice.State) protocolgo.VoiceState {
	return protocolgo.VoiceState{
		UserId: state.UserID, ChannelId: state.ChannelID, SessionId: state.SessionID, SelfMute: state.SelfMute,
		SelfDeaf: state.SelfDeaf, SelfVideo: state.SelfVideo, SelfStream: state.SelfStream, UpdatedAt: state.UpdatedAt,
	}
}

// decodeOptionalJSON decodes a JSON body that may be absent.
func decodeOptionalJSON(writer http.ResponseWriter, request *http.Request, target any) error {
	request.Body = http.MaxBytesReader(writer, request.Body, maxRequestBodyBytes)
	payload, err := io.ReadAll(request.Body)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(payload)) == 0 {
		return nil
	}
	request.Body = io.NopCloser(bytes.NewReader(payload))
	return decodeJSON(writer, request, target)
}
