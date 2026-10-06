package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	protocolgo "github.com/grampr/Aster-protocol/packages/protocol-go/generated"
	"github.com/grampr/aster-server/internal/chat"
	"github.com/grampr/aster-server/internal/gateway"
)

func (s *Server) createThread(writer http.ResponseWriter, request *http.Request) {
	user, ok := s.chatUser(writer, request, "threads_create")
	if !ok {
		return
	}
	parentID, ok := s.pathID(writer, request, "channel_id")
	if !ok {
		return
	}
	var body protocolgo.CreateThreadRequest
	if err := decodeJSON(writer, request, &body); err != nil {
		s.writeError(writer, request, http.StatusBadRequest, "INVALID_REQUEST", "Request body is invalid", err)
		return
	}
	thread, err := s.chat.CreateThread(request.Context(), user.ID, parentID, body.Name, body.MessageId)
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	s.publishChannel(request, "create", thread)
	writeJSON(writer, http.StatusCreated, channelResponse(thread))
}

func (s *Server) listThreads(writer http.ResponseWriter, request *http.Request) {
	user, ok := s.chatUser(writer, request, "threads_list")
	if !ok {
		return
	}
	parentID, ok := s.pathID(writer, request, "channel_id")
	if !ok {
		return
	}
	cursor, limit, err := pagination(request)
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	page, err := s.chat.ListThreads(request.Context(), user.ID, parentID, cursor, limit)
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, channelListResponse(page))
}

func (s *Server) openDirectChannel(writer http.ResponseWriter, request *http.Request) {
	user, ok := s.chatUser(writer, request, "direct_channels_open")
	if !ok {
		return
	}
	var body protocolgo.CreateDirectChannelRequest
	if err := decodeJSON(writer, request, &body); err != nil {
		s.writeError(writer, request, http.StatusBadRequest, "INVALID_REQUEST", "Request body is invalid", err)
		return
	}
	channel, created, err := s.chat.OpenDirectChannel(request.Context(), user.ID, body.RecipientId)
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	if created {
		s.publishChannel(request, "create", channel)
	}
	writeJSON(writer, http.StatusOK, channelResponse(channel))
}

func (s *Server) listDirectChannels(writer http.ResponseWriter, request *http.Request) {
	user, ok := s.chatUser(writer, request, "direct_channels_list")
	if !ok {
		return
	}
	cursor, limit, err := pagination(request)
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	page, err := s.chat.ListDirectChannels(request.Context(), user.ID, cursor, limit)
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, channelListResponse(page))
}

func channelListResponse(page chat.Page[chat.Channel]) protocolgo.ChannelList {
	items := make([]protocolgo.Channel, len(page.Items))
	for index := range page.Items {
		items[index] = channelResponse(page.Items[index])
	}
	return protocolgo.ChannelList{Items: items, Page: pageResponse(page.HasMore, page.NextCursor)}
}

// channelRecipients returns who is told about a Channel: its participants for a
// Direct Message and every Guild Member otherwise.
func (s *Server) channelRecipients(ctx context.Context, channel chat.Channel) ([]uuid.UUID, error) {
	if channel.GuildID == nil {
		recipients := make([]uuid.UUID, len(channel.Recipients))
		for index, recipient := range channel.Recipients {
			recipients[index] = recipient.ID
		}
		return recipients, nil
	}
	return s.chat.ListGuildMemberIDs(ctx, *channel.GuildID)
}

func (s *Server) publishChannel(request *http.Request, event string, channel chat.Channel) {
	if s.gateway == nil {
		return
	}
	publishContext, cancel := context.WithTimeout(context.WithoutCancel(request.Context()), 2*time.Second)
	defer cancel()
	recipients, err := s.channelRecipients(publishContext, channel)
	if err != nil {
		s.logger.Error("list gateway channel recipients", "request_id", requestIDFromContext(request), "channel_id", channel.ID, "error", err)
		return
	}
	payload := gateway.Channel{
		ID: channel.ID, GuildID: channel.GuildID, ParentID: channel.ParentID, Type: channel.Type, Name: channel.Name,
		Topic: channel.Topic, Position: channel.Position, CreatedAt: channel.CreatedAt,
	}
	for _, recipient := range channel.Recipients {
		payload.Recipients = append(payload.Recipients, gateway.UserSummary{ID: recipient.ID, DisplayName: recipient.DisplayName, AvatarURL: recipient.AvatarURL})
	}
	if event == "create" {
		s.gateway.PublishChannelCreate(recipients, payload)
		return
	}
	s.gateway.PublishChannelUpdate(recipients, payload)
}

func (s *Server) publishChannelDelete(request *http.Request, channel chat.Channel) {
	if s.gateway == nil {
		return
	}
	publishContext, cancel := context.WithTimeout(context.WithoutCancel(request.Context()), 2*time.Second)
	defer cancel()
	recipients, err := s.channelRecipients(publishContext, channel)
	if err != nil {
		s.logger.Error("list gateway channel recipients", "request_id", requestIDFromContext(request), "channel_id", channel.ID, "error", err)
		return
	}
	s.gateway.PublishChannelDelete(recipients, channel.ID, channel.GuildID)
}
