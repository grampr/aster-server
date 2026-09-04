package httpapi

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	protocolgo "github.com/grampr/Aster-protocol/packages/protocol-go/generated"
	"github.com/grampr/aster-server/internal/chat"
	"github.com/grampr/aster-server/internal/gateway"
	voiceapi "github.com/grampr/aster-server/internal/voice"
)

func (s *Server) listVoiceStates(w http.ResponseWriter, r *http.Request) {
	user, ok := s.chatUser(w, r, "voice_states_list")
	if !ok {
		return
	}
	channelID, ok := s.pathID(w, r, "channel_id")
	if !ok {
		return
	}
	states, err := s.voice.List(r.Context(), user.ID, channelID)
	if err != nil {
		s.handleVoiceError(w, r, err)
		return
	}
	items := make([]protocolgo.VoiceState, len(states))
	for i := range states {
		items[i] = voiceStateResponse(states[i])
	}
	writeJSON(w, http.StatusOK, protocolgo.VoiceStateList{Items: items})
}
func (s *Server) joinVoiceChannel(w http.ResponseWriter, r *http.Request) {
	user, ok := s.chatUser(w, r, "voice_join")
	if !ok {
		return
	}
	channelID, ok := s.pathID(w, r, "channel_id")
	if !ok {
		return
	}
	var body protocolgo.JoinVoiceChannelRequest
	if r.ContentLength != 0 {
		if err := decodeJSON(w, r, &body); err != nil {
			s.writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Request body is invalid", err)
			return
		}
	}
	session, err := s.voice.Join(r.Context(), user.ID, user.DisplayName, channelID, boolValue(body.SelfMute), boolValue(body.SelfDeaf))
	if err != nil {
		s.handleVoiceError(w, r, err)
		return
	}
	s.publishVoiceState(r, channelID, session.State)
	writeJSON(w, http.StatusOK, protocolgo.VoiceSession{Id: session.ID, Provider: session.Provider, Endpoint: session.Endpoint, Credential: session.Credential, ExpiresAt: session.ExpiresAt, State: voiceStateResponse(session.State)})
}
func (s *Server) updateVoiceState(w http.ResponseWriter, r *http.Request) {
	user, ok := s.chatUser(w, r, "voice_state_update")
	if !ok {
		return
	}
	var body protocolgo.UpdateVoiceStateRequest
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Request body is invalid", err)
		return
	}
	state, err := s.voice.Update(r.Context(), user.ID, voiceapi.UpdateInput{SelfMute: body.SelfMute, SelfDeaf: body.SelfDeaf, SelfVideo: body.SelfVideo, SelfStream: body.SelfStream})
	if err != nil {
		s.handleVoiceError(w, r, err)
		return
	}
	s.publishVoiceState(r, *state.ChannelID, state)
	writeJSON(w, http.StatusOK, voiceStateResponse(state))
}
func (s *Server) leaveVoiceChannel(w http.ResponseWriter, r *http.Request) {
	user, ok := s.chatUser(w, r, "voice_leave")
	if !ok {
		return
	}
	state, channelID, err := s.voice.Leave(r.Context(), user.ID)
	if err != nil {
		s.handleVoiceError(w, r, err)
		return
	}
	s.publishVoiceState(r, channelID, state)
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) publishVoiceState(r *http.Request, channelID uuid.UUID, state voiceapi.State) {
	if s.gateway == nil || s.community == nil {
		return
	}
	channel, err := s.chat.GetChannel(r.Context(), state.UserID, channelID)
	if err != nil {
		return
	}
	recipients, err := s.community.ListGuildMemberIDs(r.Context(), channel.GuildID)
	if err != nil {
		return
	}
	s.gateway.PublishVoiceStateUpdate(recipients, gateway.VoiceState{UserID: state.UserID, ChannelID: state.ChannelID, SessionID: state.SessionID, SelfMute: state.SelfMute, SelfDeaf: state.SelfDeaf, SelfVideo: state.SelfVideo, SelfStream: state.SelfStream, UpdatedAt: state.UpdatedAt})
}
func (s *Server) handleVoiceError(w http.ResponseWriter, r *http.Request, err error) {
	var validation *chat.ValidationError
	switch {
	case errors.As(err, &validation):
		s.writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", validation.Error(), nil)
	case errors.Is(err, chat.ErrForbidden):
		s.writeError(w, r, http.StatusForbidden, "FORBIDDEN", "Operation is not permitted", nil)
	case errors.Is(err, voiceapi.ErrNotFound), errors.Is(err, chat.ErrNotFound):
		s.writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource was not found", nil)
	default:
		s.logger.Error("voice request failed", "request_id", requestIDFromContext(r), "error", err)
		s.writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error", nil)
	}
}
func voiceStateResponse(state voiceapi.State) protocolgo.VoiceState {
	return protocolgo.VoiceState{UserId: state.UserID, ChannelId: state.ChannelID, SessionId: state.SessionID, SelfMute: state.SelfMute, SelfDeaf: state.SelfDeaf, SelfVideo: state.SelfVideo, SelfStream: state.SelfStream, UpdatedAt: state.UpdatedAt}
}
func boolValue(value *bool) bool { return value != nil && *value }
