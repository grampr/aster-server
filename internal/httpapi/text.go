package httpapi

import (
	"net/http"

	"github.com/google/uuid"
	protocolgo "github.com/grampr/Aster-protocol/packages/protocol-go/generated"
	"github.com/grampr/aster-server/internal/chat"
	"github.com/grampr/aster-server/internal/gateway"
)

func (s *Server) listDirectChannels(w http.ResponseWriter, r *http.Request) {
	user, ok := s.chatUser(w, r, "direct_channels_list")
	if !ok {
		return
	}
	cursor, limit, err := pagination(r)
	if err != nil {
		s.handleChatError(w, r, err)
		return
	}
	page, err := s.chat.ListDirectChannels(r.Context(), user.ID, cursor, limit)
	if err != nil {
		s.handleChatError(w, r, err)
		return
	}
	items := make([]protocolgo.Channel, len(page.Items))
	for i := range page.Items {
		items[i] = channelResponse(page.Items[i])
	}
	writeJSON(w, http.StatusOK, protocolgo.ChannelList{Items: items, Page: pageResponse(page.HasMore, page.NextCursor)})
}

func (s *Server) createDirectChannel(w http.ResponseWriter, r *http.Request) {
	user, ok := s.chatUser(w, r, "direct_channels_create")
	if !ok {
		return
	}
	var body protocolgo.CreateDirectChannelRequest
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Request body is invalid", err)
		return
	}
	channel, err := s.chat.CreateDirectChannel(r.Context(), user.ID, body.RecipientId)
	if err != nil {
		s.handleChatError(w, r, err)
		return
	}
	s.publishChannel(r, "create", channel)
	writeJSON(w, http.StatusOK, channelResponse(channel))
}

func (s *Server) listChannelThreads(w http.ResponseWriter, r *http.Request) {
	user, ok := s.chatUser(w, r, "threads_list")
	if !ok {
		return
	}
	channelID, ok := s.pathID(w, r, "channel_id")
	if !ok {
		return
	}
	cursor, limit, err := pagination(r)
	if err != nil {
		s.handleChatError(w, r, err)
		return
	}
	page, err := s.chat.ListThreads(r.Context(), user.ID, channelID, cursor, limit)
	if err != nil {
		s.handleChatError(w, r, err)
		return
	}
	items := make([]protocolgo.Channel, len(page.Items))
	for i := range page.Items {
		items[i] = channelResponse(page.Items[i])
	}
	writeJSON(w, http.StatusOK, protocolgo.ChannelList{Items: items, Page: pageResponse(page.HasMore, page.NextCursor)})
}

func (s *Server) createChannelThread(w http.ResponseWriter, r *http.Request) {
	user, ok := s.chatUser(w, r, "threads_create")
	if !ok {
		return
	}
	channelID, ok := s.pathID(w, r, "channel_id")
	if !ok {
		return
	}
	var body protocolgo.CreateThreadRequest
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Request body is invalid", err)
		return
	}
	thread, err := s.chat.CreateThread(r.Context(), user.ID, channelID, chat.CreateThreadInput{Name: body.Name, MessageID: body.MessageId})
	if err != nil {
		s.handleChatError(w, r, err)
		return
	}
	s.publishChannel(r, "create", thread)
	writeJSON(w, http.StatusCreated, channelResponse(thread))
}

func (s *Server) searchGuildMessages(w http.ResponseWriter, r *http.Request) {
	user, ok := s.chatUser(w, r, "messages_search")
	if !ok {
		return
	}
	guildID, ok := s.pathID(w, r, "guild_id")
	if !ok {
		return
	}
	cursor, limit, err := pagination(r)
	if err != nil {
		s.handleChatError(w, r, err)
		return
	}
	query := r.URL.Query()
	var channelID, authorID *uuid.UUID
	for name, target := range map[string]**uuid.UUID{"channel_id": &channelID, "author_id": &authorID} {
		if value := query.Get(name); value != "" {
			parsed, parseErr := uuid.Parse(value)
			if parseErr != nil {
				s.writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", name+" must be a UUID", nil)
				return
			}
			*target = &parsed
		}
	}
	page, err := s.chat.SearchMessages(r.Context(), user.ID, guildID, chat.MessageSearchInput{Query: query.Get("query"), ChannelID: channelID, AuthorID: authorID}, cursor, limit)
	if err != nil {
		s.handleChatError(w, r, err)
		return
	}
	items := make([]protocolgo.MessageSearchResult, len(page.Items))
	for i := range page.Items {
		items[i] = protocolgo.MessageSearchResult{Message: messageResponse(page.Items[i].Message), Excerpt: page.Items[i].Excerpt}
	}
	writeJSON(w, http.StatusOK, protocolgo.MessageSearchResultList{Items: items, Page: pageResponse(page.HasMore, page.NextCursor)})
}

func (s *Server) listReadStates(w http.ResponseWriter, r *http.Request) {
	user, ok := s.chatUser(w, r, "read_states_list")
	if !ok {
		return
	}
	states, err := s.chat.ListReadStates(r.Context(), user.ID)
	if err != nil {
		s.handleChatError(w, r, err)
		return
	}
	items := make([]protocolgo.ReadState, len(states))
	for i := range states {
		items[i] = readStateResponse(states[i])
	}
	writeJSON(w, http.StatusOK, protocolgo.ReadStateList{Items: items})
}
func (s *Server) updateReadState(w http.ResponseWriter, r *http.Request) {
	user, ok := s.chatUser(w, r, "read_states_update")
	if !ok {
		return
	}
	channelID, ok := s.pathID(w, r, "channel_id")
	if !ok {
		return
	}
	var body protocolgo.UpdateReadStateRequest
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Request body is invalid", err)
		return
	}
	state, err := s.chat.UpdateReadState(r.Context(), user.ID, channelID, body.LastReadMessageId)
	if err != nil {
		s.handleChatError(w, r, err)
		return
	}
	if s.gateway != nil {
		s.gateway.PublishReadStateUpdate(user.ID, gateway.ReadState{ChannelID: state.ChannelID, LastReadMessageID: state.LastReadMessageID, UpdatedAt: state.UpdatedAt})
	}
	writeJSON(w, http.StatusOK, readStateResponse(state))
}
func readStateResponse(value chat.ReadState) protocolgo.ReadState {
	return protocolgo.ReadState{ChannelId: value.ChannelID, LastReadMessageId: value.LastReadMessageID, UpdatedAt: value.UpdatedAt}
}
