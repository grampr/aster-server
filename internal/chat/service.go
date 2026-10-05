package chat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/grampr/aster-server/internal/media"
)

const (
	cursorGuilds   = "guilds"
	cursorChannels = "channels"
	cursorMessages = "messages"
	cursorThreads  = "threads"
	cursorDirects  = "directs"
)

type Service struct {
	storage  media.Storage
	store    Store
	now      func() time.Time
	presence *presenceTracker
}

func NewService(store Store) (*Service, error) {
	if store == nil {
		return nil, errors.New("chat store is required")
	}
	return &Service{store: store, now: time.Now, presence: newPresenceTracker()}, nil
}

func (s *Service) CreateGuild(ctx context.Context, ownerID uuid.UUID, input CreateGuildInput) (Guild, error) {
	name, err := validateName("name", input.Name)
	if err != nil {
		return Guild{}, err
	}
	description, err := normalizeOptionalText("description", input.Description, 1024)
	if err != nil {
		return Guild{}, err
	}
	id, err := newUUIDv7()
	if err != nil {
		return Guild{}, err
	}
	guild := Guild{ID: id, OwnerID: ownerID, Name: name, Description: description, CreatedAt: s.now().UTC()}
	if err := s.store.CreateGuild(ctx, ownerID, guild); err != nil {
		return Guild{}, err
	}
	return guild, nil
}

func (s *Service) ListGuilds(ctx context.Context, userID uuid.UUID, cursorValue string, limit int) (Page[Guild], error) {
	cursor, err := decodeCursor(cursorValue, cursorGuilds)
	if err != nil {
		return Page[Guild]{}, err
	}
	if err := validateLimit(limit); err != nil {
		return Page[Guild]{}, err
	}
	rows, err := s.store.ListGuilds(ctx, userID, cursor, limit+1)
	if err != nil {
		return Page[Guild]{}, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	items := make([]Guild, len(rows))
	for index := range rows {
		items[index] = rows[index].Guild
	}
	var next *string
	if hasMore && len(rows) > 0 {
		value, err := encodeCursor(pageCursor{Kind: cursorGuilds, Time: rows[len(rows)-1].JoinedAt, ID: rows[len(rows)-1].ID})
		if err != nil {
			return Page[Guild]{}, err
		}
		next = &value
	}
	return Page[Guild]{Items: items, HasMore: hasMore, NextCursor: next}, nil
}

func (s *Service) GetGuild(ctx context.Context, userID, guildID uuid.UUID) (Guild, error) {
	return s.store.GetGuild(ctx, userID, guildID)
}

func (s *Service) UpdateGuild(ctx context.Context, userID, guildID uuid.UUID, input UpdateGuildInput) (Guild, error) {
	if input.Name == nil && !input.Description.Set {
		return Guild{}, &ValidationError{Field: "body", Message: "must contain at least one field"}
	}
	if _, err := s.require(ctx, userID, guildID, PermManageGuild); err != nil {
		return Guild{}, err
	}
	if input.Name != nil {
		value, err := validateName("name", *input.Name)
		if err != nil {
			return Guild{}, err
		}
		input.Name = &value
	}
	if input.Description.Set {
		value, err := normalizeOptionalText("description", input.Description.Value, 1024)
		if err != nil {
			return Guild{}, err
		}
		input.Description.Value = value
	}
	return s.store.UpdateGuild(ctx, userID, guildID, input, s.now().UTC())
}

func (s *Service) DeleteGuild(ctx context.Context, userID, guildID uuid.UUID) error {
	guild, err := s.store.GetGuild(ctx, userID, guildID)
	if err != nil {
		return err
	}
	if guild.OwnerID != userID {
		return ErrForbidden
	}
	return s.store.DeleteGuild(ctx, userID, guildID)
}

func (s *Service) CreateChannel(ctx context.Context, userID, guildID uuid.UUID, input CreateChannelInput) (Channel, error) {
	if _, err := s.require(ctx, userID, guildID, PermManageChannels); err != nil {
		return Channel{}, err
	}
	switch input.Type {
	case ChannelTypeText, ChannelTypeVoice, ChannelTypeCategory:
	default:
		return Channel{}, &ValidationError{Field: "type", Message: "must be TEXT, VOICE or CATEGORY"}
	}
	name, err := validateName("name", input.Name)
	if err != nil {
		return Channel{}, err
	}
	topic, err := normalizeOptionalText("topic", input.Topic, 1024)
	if err != nil {
		return Channel{}, err
	}
	if input.Type != ChannelTypeText && topic != nil {
		return Channel{}, &ValidationError{Field: "topic", Message: "must be omitted for a " + input.Type + " channel"}
	}
	if input.ParentID != nil {
		if input.Type == ChannelTypeCategory {
			return Channel{}, &ValidationError{Field: "parent_id", Message: "must be omitted for a CATEGORY channel"}
		}
		if err := s.checkCategory(ctx, userID, guildID, *input.ParentID); err != nil {
			return Channel{}, err
		}
	}
	id, err := newUUIDv7()
	if err != nil {
		return Channel{}, err
	}
	return s.store.CreateChannel(ctx, Channel{
		ID: id, GuildID: &guildID, Type: input.Type, Name: &name, Topic: topic, ParentID: input.ParentID, CreatedAt: s.now().UTC(),
	})
}

// checkCategory verifies that parentID names a Category of the same Guild.
func (s *Service) checkCategory(ctx context.Context, userID, guildID, parentID uuid.UUID) error {
	parent, err := s.store.GetChannel(ctx, userID, parentID)
	if errors.Is(err, ErrNotFound) || (err == nil && (parent.Type != ChannelTypeCategory || parent.GuildID == nil || *parent.GuildID != guildID)) {
		return &ValidationError{Field: "parent_id", Message: "must reference a category in this guild"}
	}
	return err
}

func (s *Service) ListChannels(ctx context.Context, userID, guildID uuid.UUID, cursorValue string, limit int) (Page[Channel], error) {
	cursor, err := decodeCursor(cursorValue, cursorChannels)
	if err != nil {
		return Page[Channel]{}, err
	}
	if err := validateLimit(limit); err != nil {
		return Page[Channel]{}, err
	}
	rows, err := s.store.ListChannels(ctx, userID, guildID, cursor, limit+1)
	if err != nil {
		return Page[Channel]{}, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	var next *string
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		value, err := encodeCursor(pageCursor{Kind: cursorChannels, Position: last.Position, ID: last.ID})
		if err != nil {
			return Page[Channel]{}, err
		}
		next = &value
	}
	return Page[Channel]{Items: rows, HasMore: hasMore, NextCursor: next}, nil
}

func (s *Service) GetChannel(ctx context.Context, userID, channelID uuid.UUID) (Channel, error) {
	return s.store.GetChannel(ctx, userID, channelID)
}

func (s *Service) StartTyping(ctx context.Context, userID, channelID uuid.UUID) error {
	channel, err := s.store.GetChannel(ctx, userID, channelID)
	if err != nil {
		return err
	}
	if !channel.IsText() {
		return ErrNotFound
	}
	return nil
}

func (s *Service) UpdateChannel(ctx context.Context, userID, channelID uuid.UUID, input UpdateChannelInput) (Channel, error) {
	if input.Name == nil && !input.Topic.Set && input.Position == nil && !input.ParentID.Set {
		return Channel{}, &ValidationError{Field: "body", Message: "must contain at least one field"}
	}
	channel, err := s.store.GetChannel(ctx, userID, channelID)
	if err != nil {
		return Channel{}, err
	}
	if channel.GuildID == nil {
		return Channel{}, ErrForbidden
	}
	if _, err := s.require(ctx, userID, *channel.GuildID, PermManageChannels); err != nil {
		return Channel{}, err
	}
	if channel.Type == ChannelTypeThread && (input.Topic.Set || input.Position != nil || input.ParentID.Set) {
		return Channel{}, &ValidationError{Field: "body", Message: "only the name of a thread can be changed"}
	}
	if input.Name != nil {
		value, err := validateName("name", *input.Name)
		if err != nil {
			return Channel{}, err
		}
		input.Name = &value
	}
	if input.Topic.Set {
		value, err := normalizeOptionalText("topic", input.Topic.Value, 1024)
		if err != nil {
			return Channel{}, err
		}
		if channel.Type != ChannelTypeText && value != nil {
			return Channel{}, &ValidationError{Field: "topic", Message: "must be null for a " + channel.Type + " channel"}
		}
		input.Topic.Value = value
	}
	if input.Position != nil && *input.Position < 0 {
		return Channel{}, &ValidationError{Field: "position", Message: "must be zero or greater"}
	}
	if input.ParentID.Set && input.ParentID.Value != nil {
		if channel.Type == ChannelTypeCategory {
			return Channel{}, &ValidationError{Field: "parent_id", Message: "must be null for a CATEGORY channel"}
		}
		if err := s.checkCategory(ctx, userID, *channel.GuildID, *input.ParentID.Value); err != nil {
			return Channel{}, err
		}
	}
	updated, err := s.store.UpdateChannel(ctx, userID, channelID, input, s.now().UTC())
	if err != nil {
		return Channel{}, err
	}
	return updated, nil
}

func (s *Service) DeleteChannel(ctx context.Context, userID, channelID uuid.UUID) error {
	channel, err := s.store.GetChannel(ctx, userID, channelID)
	if err != nil {
		return err
	}
	if channel.GuildID == nil {
		return ErrForbidden
	}
	if _, err := s.require(ctx, userID, *channel.GuildID, PermManageChannels); err != nil {
		return err
	}
	return s.store.DeleteChannel(ctx, userID, channelID)
}

// CreateThread starts a Thread under a Text Channel, optionally from one of its Messages.
func (s *Service) CreateThread(ctx context.Context, userID, parentID uuid.UUID, name string, messageID *uuid.UUID) (Channel, error) {
	parent, err := s.store.GetChannel(ctx, userID, parentID)
	if err != nil {
		return Channel{}, err
	}
	if parent.Type != ChannelTypeText || parent.GuildID == nil {
		return Channel{}, ErrNotFound
	}
	if _, err := s.require(ctx, userID, *parent.GuildID, PermSendMessages); err != nil {
		return Channel{}, err
	}
	name, err = validateName("name", name)
	if err != nil {
		return Channel{}, err
	}
	if messageID != nil {
		if _, err := s.store.GetMessage(ctx, userID, parentID, *messageID); err != nil {
			return Channel{}, err
		}
	}
	id, err := newUUIDv7()
	if err != nil {
		return Channel{}, err
	}
	return s.store.CreateThread(ctx, Channel{ID: id, GuildID: parent.GuildID, Name: &name, ParentID: &parentID}, messageID, s.now().UTC())
}

func (s *Service) ListThreads(ctx context.Context, userID, parentID uuid.UUID, cursorValue string, limit int) (Page[Channel], error) {
	cursor, err := decodeCursor(cursorValue, cursorThreads)
	if err != nil {
		return Page[Channel]{}, err
	}
	if err := validateLimit(limit); err != nil {
		return Page[Channel]{}, err
	}
	rows, err := s.store.ListThreads(ctx, userID, parentID, cursor, limit+1)
	if err != nil {
		return Page[Channel]{}, err
	}
	return channelPage(rows, limit, cursorThreads)
}

// OpenDirectChannel returns the Direct Message with recipientID, creating it if needed.
func (s *Service) OpenDirectChannel(ctx context.Context, userID, recipientID uuid.UUID) (Channel, bool, error) {
	if recipientID == uuid.Nil {
		return Channel{}, false, &ValidationError{Field: "recipient_id", Message: "must be a UUID"}
	}
	if recipientID == userID {
		return Channel{}, false, &ValidationError{Field: "recipient_id", Message: "must be another user"}
	}
	id, err := newUUIDv7()
	if err != nil {
		return Channel{}, false, err
	}
	return s.store.OpenDirectChannel(ctx, Channel{ID: id, CreatedAt: s.now().UTC()}, userID, recipientID)
}

func (s *Service) ListDirectChannels(ctx context.Context, userID uuid.UUID, cursorValue string, limit int) (Page[Channel], error) {
	cursor, err := decodeCursor(cursorValue, cursorDirects)
	if err != nil {
		return Page[Channel]{}, err
	}
	if err := validateLimit(limit); err != nil {
		return Page[Channel]{}, err
	}
	rows, err := s.store.ListDirectChannels(ctx, userID, cursor, limit+1)
	if err != nil {
		return Page[Channel]{}, err
	}
	return channelPage(rows, limit, cursorDirects)
}

func channelPage(rows []channelListRow, limit int, kind string) (Page[Channel], error) {
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	items := make([]Channel, len(rows))
	for index := range rows {
		items[index] = rows[index].Channel
	}
	var next *string
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		value, err := encodeCursor(pageCursor{Kind: kind, Time: last.UpdatedAt, ID: last.ID})
		if err != nil {
			return Page[Channel]{}, err
		}
		next = &value
	}
	return Page[Channel]{Items: items, HasMore: hasMore, NextCursor: next}, nil
}

func (s *Service) CreateMessage(ctx context.Context, userID, channelID uuid.UUID, content string, replyToMessageID *uuid.UUID, attachmentIDs []uuid.UUID) (Message, error) {
	channel, err := s.store.GetChannel(ctx, userID, channelID)
	if err != nil {
		return Message{}, err
	}
	if !channel.IsText() {
		return Message{}, ErrNotFound
	}
	if channel.GuildID != nil {
		if _, err := s.require(ctx, userID, *channel.GuildID, PermSendMessages); err != nil {
			return Message{}, err
		}
	}
	if len(attachmentIDs) > maxMessageAttachments {
		return Message{}, &ValidationError{Field: "attachment_ids", Message: "must contain at most 10 attachments"}
	}
	seen := make(map[uuid.UUID]struct{}, len(attachmentIDs))
	for _, attachmentID := range attachmentIDs {
		if _, duplicate := seen[attachmentID]; duplicate {
			return Message{}, &ValidationError{Field: "attachment_ids", Message: "must not contain duplicates"}
		}
		seen[attachmentID] = struct{}{}
	}
	if len(attachmentIDs) == 0 {
		if err := validateContent(content); err != nil {
			return Message{}, err
		}
	} else if utf8.RuneCountInString(content) > 4000 {
		return Message{}, &ValidationError{Field: "content", Message: "must contain at most 4000 characters"}
	}
	if replyToMessageID != nil {
		if *replyToMessageID == uuid.Nil {
			return Message{}, &ValidationError{Field: "reply_to_message_id", Message: "must be a UUID"}
		}
		if _, err := s.store.GetMessage(ctx, userID, channelID, *replyToMessageID); err != nil {
			return Message{}, err
		}
	}
	id, err := newUUIDv7()
	if err != nil {
		return Message{}, err
	}
	return s.store.CreateMessage(ctx, Message{
		ID: id, ChannelID: channelID, Author: UserSummary{ID: userID}, Content: content,
		ReplyToMessageID: replyToMessageID, AttachmentIDs: attachmentIDs, CreatedAt: s.now().UTC(),
	})
}

func (s *Service) ListMessages(ctx context.Context, userID, channelID uuid.UUID, cursorValue string, limit int) (Page[Message], error) {
	cursor, err := decodeCursor(cursorValue, cursorMessages)
	if err != nil {
		return Page[Message]{}, err
	}
	if err := validateLimit(limit); err != nil {
		return Page[Message]{}, err
	}
	rows, err := s.store.ListMessages(ctx, userID, channelID, cursor, limit+1)
	if err != nil {
		return Page[Message]{}, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	var next *string
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		value, err := encodeCursor(pageCursor{Kind: cursorMessages, Time: last.CreatedAt, ID: last.ID})
		if err != nil {
			return Page[Message]{}, err
		}
		next = &value
	}
	return Page[Message]{Items: rows, HasMore: hasMore, NextCursor: next}, nil
}

func (s *Service) GetMessage(ctx context.Context, userID, channelID, messageID uuid.UUID) (Message, error) {
	return s.store.GetMessage(ctx, userID, channelID, messageID)
}

func (s *Service) UpdateMessage(ctx context.Context, userID, channelID, messageID uuid.UUID, content string) (Message, error) {
	if err := validateContent(content); err != nil {
		return Message{}, err
	}
	message, err := s.store.GetMessage(ctx, userID, channelID, messageID)
	if err != nil {
		return Message{}, err
	}
	if message.Author.ID != userID {
		return Message{}, ErrForbidden
	}
	return s.store.UpdateMessage(ctx, userID, channelID, messageID, content, s.now().UTC())
}

func (s *Service) DeleteMessage(ctx context.Context, userID, channelID, messageID uuid.UUID) error {
	channel, err := s.store.GetChannel(ctx, userID, channelID)
	if err != nil {
		return err
	}
	canManage := false
	if channel.GuildID != nil {
		access, err := s.store.GetAccess(ctx, *channel.GuildID, userID)
		if err != nil {
			return err
		}
		canManage = access.Has(PermManageMessages)
	}
	return s.store.DeleteMessage(ctx, userID, channelID, messageID, canManage)
}

func (s *Service) AddMessageReaction(ctx context.Context, userID, channelID, messageID uuid.UUID, emoji string) (MessageReaction, bool, error) {
	if err := validateReactionEmoji(emoji); err != nil {
		return MessageReaction{}, false, err
	}
	return s.store.AddMessageReaction(ctx, userID, channelID, messageID, emoji, s.now().UTC())
}

func (s *Service) RemoveMessageReaction(ctx context.Context, userID, channelID, messageID uuid.UUID, emoji string) (MessageReaction, bool, error) {
	if err := validateReactionEmoji(emoji); err != nil {
		return MessageReaction{}, false, err
	}
	return s.store.RemoveMessageReaction(ctx, userID, channelID, messageID, emoji)
}

func validateReactionEmoji(emoji string) error {
	if !utf8.ValidString(emoji) || utf8.RuneCountInString(emoji) < 1 || utf8.RuneCountInString(emoji) > 64 {
		return &ValidationError{Field: "emoji", Message: "must contain 1 to 64 Unicode characters"}
	}
	hasEmojiSymbol := false
	for _, character := range emoji {
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			return &ValidationError{Field: "emoji", Message: "must not contain control or whitespace characters"}
		}
		if unicode.Is(unicode.So, character) || character == '\u20e3' {
			hasEmojiSymbol = true
		}
	}
	if !hasEmojiSymbol {
		return &ValidationError{Field: "emoji", Message: "must be a Unicode emoji sequence"}
	}
	return nil
}

func (s *Service) ChannelAudience(ctx context.Context, channelID uuid.UUID) (Audience, error) {
	return s.store.ChannelAudience(ctx, channelID)
}

func validateName(field, value string) (string, error) {
	value = strings.TrimSpace(value)
	length := utf8.RuneCountInString(value)
	if length < 1 || length > 100 {
		return "", &ValidationError{Field: field, Message: "must contain between 1 and 100 characters"}
	}
	return value, nil
}

func normalizeOptionalText(field string, value *string, maximum int) (*string, error) {
	if value == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil, nil
	}
	if utf8.RuneCountInString(trimmed) > maximum {
		return nil, &ValidationError{Field: field, Message: fmt.Sprintf("must contain at most %d characters", maximum)}
	}
	return &trimmed, nil
}

func validateContent(value string) error {
	length := utf8.RuneCountInString(value)
	if length < 1 || length > 4000 {
		return &ValidationError{Field: "content", Message: "must contain between 1 and 4000 characters"}
	}
	return nil
}

func validateLimit(limit int) error {
	if limit < 1 || limit > 100 {
		return &ValidationError{Field: "limit", Message: "must be between 1 and 100"}
	}
	return nil
}

func encodeCursor(cursor pageCursor) (string, error) {
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encode cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeCursor(value, kind string) (*pageCursor, error) {
	if value == "" {
		return nil, nil
	}
	if len(value) > 512 {
		return nil, &ValidationError{Field: "cursor", Message: "is invalid"}
	}
	payload, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, &ValidationError{Field: "cursor", Message: "is invalid"}
	}
	var cursor pageCursor
	if err := json.Unmarshal(payload, &cursor); err != nil || cursor.Kind != kind || cursor.ID == uuid.Nil {
		return nil, &ValidationError{Field: "cursor", Message: "is invalid"}
	}
	if (kind == cursorSearch || kind == cursorGuilds || kind == cursorMessages || kind == cursorMembers || kind == cursorThreads || kind == cursorDirects) && cursor.Time.IsZero() {
		return nil, &ValidationError{Field: "cursor", Message: "is invalid"}
	}
	if kind == cursorChannels && cursor.Position < 0 {
		return nil, &ValidationError{Field: "cursor", Message: "is invalid"}
	}
	return &cursor, nil
}

func newUUIDv7() (uuid.UUID, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, fmt.Errorf("generate UUIDv7: %w", err)
	}
	return id, nil
}
