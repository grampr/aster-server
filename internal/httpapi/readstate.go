package httpapi

import (
	"net/http"

	"github.com/google/uuid"
	protocolgo "github.com/grampr/Aster-protocol/packages/protocol-go/generated"
	"github.com/grampr/aster-server/internal/chat"
	"github.com/grampr/aster-server/internal/gateway"
)

func (s *Server) updateReadState(writer http.ResponseWriter, request *http.Request) {
	user, ok := s.chatUser(writer, request, "read_state_update")
	if !ok {
		return
	}
	channelID, ok := s.pathID(writer, request, "channel_id")
	if !ok {
		return
	}
	var body protocolgo.UpdateReadStateRequest
	if err := decodeJSON(writer, request, &body); err != nil {
		s.writeError(writer, request, http.StatusBadRequest, "INVALID_REQUEST", "Request body is invalid", err)
		return
	}
	state, changed, err := s.chat.UpdateReadState(request.Context(), user.ID, channelID, body.LastReadMessageId)
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	if changed && s.gateway != nil {
		s.gateway.PublishReadStateUpdate(user.ID, gateway.ReadState{
			ChannelID: state.ChannelID, LastReadMessageID: state.LastReadMessageID, UpdatedAt: state.UpdatedAt,
		})
	}
	writeJSON(writer, http.StatusOK, readStateResponse(state))
}

func (s *Server) listReadStates(writer http.ResponseWriter, request *http.Request) {
	user, ok := s.chatUser(writer, request, "read_states_list")
	if !ok {
		return
	}
	states, err := s.chat.ListReadStates(request.Context(), user.ID)
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	items := make([]protocolgo.ReadState, len(states))
	for index := range states {
		items[index] = readStateResponse(states[index])
	}
	writeJSON(writer, http.StatusOK, protocolgo.ReadStateList{Items: items})
}

func (s *Server) searchMessages(writer http.ResponseWriter, request *http.Request) {
	user, ok := s.chatUser(writer, request, "messages_search")
	if !ok {
		return
	}
	guildID, ok := s.pathID(writer, request, "guild_id")
	if !ok {
		return
	}
	query := request.URL.Query()
	for _, name := range []string{"query", "channel_id", "author_id"} {
		if len(query[name]) > 1 {
			s.handleChatError(writer, request, &chat.ValidationError{Field: "query", Message: "parameters must not be repeated"})
			return
		}
	}
	input := chat.SearchInput{Query: query.Get("query")}
	for name, target := range map[string]**uuid.UUID{"channel_id": &input.ChannelID, "author_id": &input.AuthorID} {
		if value := query.Get(name); value != "" {
			id, err := uuid.Parse(value)
			if err != nil {
				s.handleChatError(writer, request, &chat.ValidationError{Field: name, Message: "must be a UUID"})
				return
			}
			*target = &id
		}
	}
	cursor, limit, err := pagination(request)
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	page, err := s.chat.SearchMessages(request.Context(), user.ID, guildID, input, cursor, limit)
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	items := make([]protocolgo.MessageSearchResult, len(page.Items))
	for index := range page.Items {
		items[index] = protocolgo.MessageSearchResult{Message: messageResponse(page.Items[index].Message), Excerpt: page.Items[index].Excerpt}
	}
	writeJSON(writer, http.StatusOK, protocolgo.MessageSearchResultList{Items: items, Page: pageResponse(page.HasMore, page.NextCursor)})
}

func readStateResponse(state chat.ReadState) protocolgo.ReadState {
	return protocolgo.ReadState{ChannelId: state.ChannelID, LastReadMessageId: state.LastReadMessageID, UpdatedAt: state.UpdatedAt}
}
