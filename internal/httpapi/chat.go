package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	protocolgo "github.com/grampr/Aster-protocol/packages/protocol-go/generated"
	"github.com/grampr/aster-server/internal/auth"
	"github.com/grampr/aster-server/internal/chat"
	"github.com/grampr/aster-server/internal/gateway"
)

func (s *Server) createGuild(writer http.ResponseWriter, request *http.Request) {
	user, ok := s.chatUser(writer, request, "guilds_create")
	if !ok {
		return
	}
	var body protocolgo.CreateGuildRequest
	if err := decodeJSON(writer, request, &body); err != nil {
		s.writeError(writer, request, http.StatusBadRequest, "INVALID_REQUEST", "Request body is invalid", err)
		return
	}
	guild, err := s.chat.CreateGuild(request.Context(), user.ID, chat.CreateGuildInput{Name: body.Name, Description: body.Description})
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, guildResponse(guild))
}

func (s *Server) listGuilds(writer http.ResponseWriter, request *http.Request) {
	user, ok := s.chatUser(writer, request, "guilds_list")
	if !ok {
		return
	}
	cursor, limit, err := pagination(request)
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	page, err := s.chat.ListGuilds(request.Context(), user.ID, cursor, limit)
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	items := make([]protocolgo.Guild, len(page.Items))
	for index := range page.Items {
		items[index] = guildResponse(page.Items[index])
	}
	writeJSON(writer, http.StatusOK, protocolgo.GuildList{Items: items, Page: pageResponse(page.HasMore, page.NextCursor)})
}

func (s *Server) getGuild(writer http.ResponseWriter, request *http.Request) {
	user, ok := s.chatUser(writer, request, "guilds_get")
	if !ok {
		return
	}
	guildID, ok := s.pathID(writer, request, "guild_id")
	if !ok {
		return
	}
	guild, err := s.chat.GetGuild(request.Context(), user.ID, guildID)
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, guildResponse(guild))
}

func (s *Server) updateGuild(writer http.ResponseWriter, request *http.Request) {
	user, ok := s.chatUser(writer, request, "guilds_update")
	if !ok {
		return
	}
	guildID, ok := s.pathID(writer, request, "guild_id")
	if !ok {
		return
	}
	var body guildPatchBody
	if err := decodeJSON(writer, request, &body); err != nil {
		s.writeError(writer, request, http.StatusBadRequest, "INVALID_REQUEST", "Request body is invalid", err)
		return
	}
	guild, err := s.chat.UpdateGuild(request.Context(), user.ID, guildID, chat.UpdateGuildInput{
		Name:        body.Name,
		Description: chat.OptionalString{Set: body.Description.Set, Value: body.Description.Value},
	})
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, guildResponse(guild))
}

func (s *Server) deleteGuild(writer http.ResponseWriter, request *http.Request) {
	user, ok := s.chatUser(writer, request, "guilds_delete")
	if !ok {
		return
	}
	guildID, ok := s.pathID(writer, request, "guild_id")
	if !ok {
		return
	}
	if err := s.chat.DeleteGuild(request.Context(), user.ID, guildID); err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) createChannel(writer http.ResponseWriter, request *http.Request) {
	user, ok := s.chatUser(writer, request, "channels_create")
	if !ok {
		return
	}
	guildID, ok := s.pathID(writer, request, "guild_id")
	if !ok {
		return
	}
	var body protocolgo.CreateChannelRequest
	if err := decodeJSON(writer, request, &body); err != nil {
		s.writeError(writer, request, http.StatusBadRequest, "INVALID_REQUEST", "Request body is invalid", err)
		return
	}
	channel, err := s.chat.CreateChannel(request.Context(), user.ID, guildID, chat.CreateChannelInput{
		Type: string(body.Type), Name: body.Name, Topic: body.Topic,
	})
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, channelResponse(channel))
}

func (s *Server) listChannels(writer http.ResponseWriter, request *http.Request) {
	user, ok := s.chatUser(writer, request, "channels_list")
	if !ok {
		return
	}
	guildID, ok := s.pathID(writer, request, "guild_id")
	if !ok {
		return
	}
	cursor, limit, err := pagination(request)
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	page, err := s.chat.ListChannels(request.Context(), user.ID, guildID, cursor, limit)
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	items := make([]protocolgo.Channel, len(page.Items))
	for index := range page.Items {
		items[index] = channelResponse(page.Items[index])
	}
	writeJSON(writer, http.StatusOK, protocolgo.ChannelList{Items: items, Page: pageResponse(page.HasMore, page.NextCursor)})
}

func (s *Server) getChannel(writer http.ResponseWriter, request *http.Request) {
	user, ok := s.chatUser(writer, request, "channels_get")
	if !ok {
		return
	}
	channelID, ok := s.pathID(writer, request, "channel_id")
	if !ok {
		return
	}
	channel, err := s.chat.GetChannel(request.Context(), user.ID, channelID)
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, channelResponse(channel))
}

func (s *Server) updateChannel(writer http.ResponseWriter, request *http.Request) {
	user, ok := s.chatUser(writer, request, "channels_update")
	if !ok {
		return
	}
	channelID, ok := s.pathID(writer, request, "channel_id")
	if !ok {
		return
	}
	var body channelPatchBody
	if err := decodeJSON(writer, request, &body); err != nil {
		s.writeError(writer, request, http.StatusBadRequest, "INVALID_REQUEST", "Request body is invalid", err)
		return
	}
	channel, err := s.chat.UpdateChannel(request.Context(), user.ID, channelID, chat.UpdateChannelInput{
		Name: body.Name, Position: body.Position,
		Topic: chat.OptionalString{Set: body.Topic.Set, Value: body.Topic.Value},
	})
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, channelResponse(channel))
}

func (s *Server) deleteChannel(writer http.ResponseWriter, request *http.Request) {
	user, ok := s.chatUser(writer, request, "channels_delete")
	if !ok {
		return
	}
	channelID, ok := s.pathID(writer, request, "channel_id")
	if !ok {
		return
	}
	if err := s.chat.DeleteChannel(request.Context(), user.ID, channelID); err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) createMessage(writer http.ResponseWriter, request *http.Request) {
	user, ok := s.chatUser(writer, request, "messages_create")
	if !ok {
		return
	}
	channelID, ok := s.pathID(writer, request, "channel_id")
	if !ok {
		return
	}
	var body protocolgo.CreateMessageRequest
	if err := decodeJSON(writer, request, &body); err != nil {
		s.writeError(writer, request, http.StatusBadRequest, "INVALID_REQUEST", "Request body is invalid", err)
		return
	}
	message, err := s.chat.CreateMessage(request.Context(), user.ID, channelID, body.Content, body.ReplyToMessageId)
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	s.publishMessage(request, "create", message)
	writeJSON(writer, http.StatusCreated, messageResponse(message))
}

func (s *Server) listMessages(writer http.ResponseWriter, request *http.Request) {
	user, ok := s.chatUser(writer, request, "messages_list")
	if !ok {
		return
	}
	channelID, ok := s.pathID(writer, request, "channel_id")
	if !ok {
		return
	}
	cursor, limit, err := pagination(request)
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	page, err := s.chat.ListMessages(request.Context(), user.ID, channelID, cursor, limit)
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	items := make([]protocolgo.Message, len(page.Items))
	for index := range page.Items {
		items[index] = messageResponse(page.Items[index])
	}
	writeJSON(writer, http.StatusOK, protocolgo.MessageList{Items: items, Page: pageResponse(page.HasMore, page.NextCursor)})
}

func (s *Server) getMessage(writer http.ResponseWriter, request *http.Request) {
	user, channelID, messageID, ok := s.messageRequest(writer, request, "messages_get")
	if !ok {
		return
	}
	message, err := s.chat.GetMessage(request.Context(), user.ID, channelID, messageID)
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, messageResponse(message))
}

func (s *Server) updateMessage(writer http.ResponseWriter, request *http.Request) {
	user, channelID, messageID, ok := s.messageRequest(writer, request, "messages_update")
	if !ok {
		return
	}
	var body protocolgo.UpdateMessageRequest
	if err := decodeJSON(writer, request, &body); err != nil {
		s.writeError(writer, request, http.StatusBadRequest, "INVALID_REQUEST", "Request body is invalid", err)
		return
	}
	message, err := s.chat.UpdateMessage(request.Context(), user.ID, channelID, messageID, body.Content)
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	s.publishMessage(request, "update", message)
	writeJSON(writer, http.StatusOK, messageResponse(message))
}

func (s *Server) deleteMessage(writer http.ResponseWriter, request *http.Request) {
	user, channelID, messageID, ok := s.messageRequest(writer, request, "messages_delete")
	if !ok {
		return
	}
	if err := s.chat.DeleteMessage(request.Context(), user.ID, channelID, messageID); err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	s.publishMessageDelete(request, messageID, channelID)
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) addMessageReaction(writer http.ResponseWriter, request *http.Request) {
	s.changeMessageReaction(writer, request, true)
}

func (s *Server) removeMessageReaction(writer http.ResponseWriter, request *http.Request) {
	s.changeMessageReaction(writer, request, false)
}

func (s *Server) changeMessageReaction(writer http.ResponseWriter, request *http.Request, add bool) {
	bucket := "message_reactions_remove"
	if add {
		bucket = "message_reactions_add"
	}
	user, channelID, messageID, ok := s.messageRequest(writer, request, bucket)
	if !ok {
		return
	}
	emoji := request.PathValue("emoji")
	var reaction chat.MessageReaction
	var changed bool
	var err error
	if add {
		reaction, changed, err = s.chat.AddMessageReaction(request.Context(), user.ID, channelID, messageID, emoji)
	} else {
		reaction, changed, err = s.chat.RemoveMessageReaction(request.Context(), user.ID, channelID, messageID, emoji)
	}
	if err != nil {
		s.handleChatError(writer, request, err)
		return
	}
	if changed {
		s.publishMessageReaction(request, add, messageID, channelID, user.ID, reaction)
	}
	writeJSON(writer, http.StatusOK, messageReactionResponse(reaction))
}

func (s *Server) publishMessage(request *http.Request, event string, message chat.Message) {
	if s.gateway == nil {
		return
	}
	publishContext, cancel := context.WithTimeout(context.WithoutCancel(request.Context()), 2*time.Second)
	defer cancel()
	recipients, err := s.chat.ListChannelMemberIDs(publishContext, message.ChannelID)
	if err != nil {
		s.logger.Error("list gateway message recipients", "request_id", requestIDFromContext(request), "channel_id", message.ChannelID, "error", err)
		return
	}
	payload := gateway.Message{
		ID: message.ID, ChannelID: message.ChannelID, Content: message.Content, ReplyToMessageID: message.ReplyToMessageID,
		Author:    gateway.UserSummary{ID: message.Author.ID, DisplayName: message.Author.DisplayName, AvatarURL: message.Author.AvatarURL},
		CreatedAt: message.CreatedAt, EditedAt: message.EditedAt,
	}
	if message.ReplyTo != nil {
		payload.ReplyTo = &gateway.MessageReply{
			ID: message.ReplyTo.ID, ChannelID: message.ReplyTo.ChannelID, Content: message.ReplyTo.Content,
			Author: gateway.UserSummary{
				ID: message.ReplyTo.Author.ID, DisplayName: message.ReplyTo.Author.DisplayName, AvatarURL: message.ReplyTo.Author.AvatarURL,
			},
			CreatedAt: message.ReplyTo.CreatedAt, EditedAt: message.ReplyTo.EditedAt,
		}
	}
	if event == "create" {
		s.gateway.PublishMessageCreate(recipients, payload)
		return
	}
	s.gateway.PublishMessageUpdate(recipients, payload)
}

func (s *Server) publishMessageDelete(request *http.Request, messageID, channelID uuid.UUID) {
	if s.gateway == nil {
		return
	}
	publishContext, cancel := context.WithTimeout(context.WithoutCancel(request.Context()), 2*time.Second)
	defer cancel()
	recipients, err := s.chat.ListChannelMemberIDs(publishContext, channelID)
	if err != nil {
		s.logger.Error("list gateway message recipients", "request_id", requestIDFromContext(request), "channel_id", channelID, "error", err)
		return
	}
	s.gateway.PublishMessageDelete(recipients, messageID, channelID)
}

func (s *Server) publishMessageReaction(request *http.Request, add bool, messageID, channelID, userID uuid.UUID, reaction chat.MessageReaction) {
	if s.gateway == nil {
		return
	}
	publishContext, cancel := context.WithTimeout(context.WithoutCancel(request.Context()), 2*time.Second)
	defer cancel()
	recipients, err := s.chat.ListChannelMemberIDs(publishContext, channelID)
	if err != nil {
		s.logger.Error("list gateway reaction recipients", "request_id", requestIDFromContext(request), "channel_id", channelID, "error", err)
		return
	}
	s.gateway.PublishMessageReaction(recipients, add, gateway.MessageReaction{
		MessageID: messageID, ChannelID: channelID, UserID: userID, Emoji: reaction.Emoji, Count: reaction.Count,
	})
}

func (s *Server) chatUser(writer http.ResponseWriter, request *http.Request, bucket string) (auth.User, bool) {
	if !s.allowRequest(writer, request, bucket) {
		return auth.User{}, false
	}
	accessToken, err := bearerToken(request)
	if err != nil {
		s.writeError(writer, request, http.StatusUnauthorized, "UNAUTHORIZED", "Authentication is required", nil)
		return auth.User{}, false
	}
	user, err := s.auth.Authenticate(request.Context(), accessToken)
	if err != nil {
		s.handleAuthError(writer, request, err)
		return auth.User{}, false
	}
	return user, true
}

func (s *Server) messageRequest(writer http.ResponseWriter, request *http.Request, bucket string) (auth.User, uuid.UUID, uuid.UUID, bool) {
	user, ok := s.chatUser(writer, request, bucket)
	if !ok {
		return auth.User{}, uuid.Nil, uuid.Nil, false
	}
	channelID, ok := s.pathID(writer, request, "channel_id")
	if !ok {
		return auth.User{}, uuid.Nil, uuid.Nil, false
	}
	messageID, ok := s.pathID(writer, request, "message_id")
	if !ok {
		return auth.User{}, uuid.Nil, uuid.Nil, false
	}
	return user, channelID, messageID, true
}

func (s *Server) pathID(writer http.ResponseWriter, request *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(request.PathValue(name))
	if err != nil {
		s.writeError(writer, request, http.StatusBadRequest, "INVALID_REQUEST", name+" must be a UUID", nil)
		return uuid.Nil, false
	}
	return id, true
}

func (s *Server) handleChatError(writer http.ResponseWriter, request *http.Request, err error) {
	var validationError *chat.ValidationError
	switch {
	case errors.As(err, &validationError):
		s.writeError(writer, request, http.StatusBadRequest, "INVALID_REQUEST", validationError.Error(), nil)
	case errors.Is(err, chat.ErrForbidden):
		s.writeError(writer, request, http.StatusForbidden, "FORBIDDEN", "You do not have permission to perform this operation", nil)
	case errors.Is(err, chat.ErrNotFound):
		s.writeError(writer, request, http.StatusNotFound, "NOT_FOUND", "Resource not found", nil)
	default:
		s.writeError(writer, request, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error", err)
	}
}

func pagination(request *http.Request) (string, int, error) {
	query := request.URL.Query()
	if len(query["cursor"]) > 1 || len(query["limit"]) > 1 {
		return "", 0, &chat.ValidationError{Field: "query", Message: "parameters must not be repeated"}
	}
	cursor := query.Get("cursor")
	limit := 50
	if value := query.Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return "", 0, &chat.ValidationError{Field: "limit", Message: "must be an integer"}
		}
		limit = parsed
	}
	return cursor, limit, nil
}

type optionalNullableString struct {
	Set   bool
	Value *string
}

func (value *optionalNullableString) UnmarshalJSON(payload []byte) error {
	value.Set = true
	if bytes.Equal(payload, []byte("null")) {
		value.Value = nil
		return nil
	}
	var decoded string
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return fmt.Errorf("must be a string or null: %w", err)
	}
	value.Value = &decoded
	return nil
}

type guildPatchBody struct {
	Name        *string                `json:"name,omitempty"`
	Description optionalNullableString `json:"description,omitempty"`
}

type channelPatchBody struct {
	Name     *string                `json:"name,omitempty"`
	Topic    optionalNullableString `json:"topic,omitempty"`
	Position *int                   `json:"position,omitempty"`
}

func guildResponse(guild chat.Guild) protocolgo.Guild {
	return protocolgo.Guild{
		Id: guild.ID, OwnerId: guild.OwnerID, Name: guild.Name, Description: guild.Description,
		IconUrl: guild.IconURL, CreatedAt: guild.CreatedAt,
	}
}

func channelResponse(channel chat.Channel) protocolgo.Channel {
	return protocolgo.Channel{
		Id: channel.ID, GuildId: channel.GuildID, Type: protocolgo.ChannelType(channel.Type),
		Name: channel.Name, Topic: channel.Topic, Position: channel.Position, CreatedAt: channel.CreatedAt,
	}
}

func messageResponse(message chat.Message) protocolgo.Message {
	reactions := make([]protocolgo.MessageReaction, len(message.Reactions))
	for index, reaction := range message.Reactions {
		reactions[index] = messageReactionResponse(reaction)
	}
	response := protocolgo.Message{
		Id: message.ID, ChannelId: message.ChannelID, Content: message.Content,
		ReplyToMessageId: message.ReplyToMessageID, Reactions: reactions, CreatedAt: message.CreatedAt, EditedAt: message.EditedAt,
		Author: protocolgo.UserSummary{
			Id: message.Author.ID, DisplayName: message.Author.DisplayName, AvatarUrl: message.Author.AvatarURL,
		},
	}
	if message.ReplyTo != nil {
		response.ReplyTo = &protocolgo.MessageReply{
			Id: message.ReplyTo.ID, ChannelId: message.ReplyTo.ChannelID, Content: message.ReplyTo.Content,
			Author: protocolgo.UserSummary{
				Id: message.ReplyTo.Author.ID, DisplayName: message.ReplyTo.Author.DisplayName, AvatarUrl: message.ReplyTo.Author.AvatarURL,
			},
			CreatedAt: message.ReplyTo.CreatedAt, EditedAt: message.ReplyTo.EditedAt,
		}
	}
	return response
}

func messageReactionResponse(reaction chat.MessageReaction) protocolgo.MessageReaction {
	return protocolgo.MessageReaction{Emoji: protocolgo.ReactionEmoji(reaction.Emoji), Count: reaction.Count, Me: reaction.Me}
}

func pageResponse(hasMore bool, cursor *string) protocolgo.PageInfo {
	return protocolgo.PageInfo{HasMore: hasMore, NextCursor: cursor}
}
