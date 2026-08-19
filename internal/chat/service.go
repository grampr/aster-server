package chat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	cursorGuilds   = "guilds"
	cursorChannels = "channels"
	cursorMessages = "messages"
)

type Service struct {
	store Store
	now   func() time.Time
}

func NewService(store Store) (*Service, error) {
	if store == nil {
		return nil, errors.New("chat store is required")
	}
	return &Service{store: store, now: time.Now}, nil
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
	current, err := s.store.GetGuild(ctx, userID, guildID)
	if err != nil {
		return Guild{}, err
	}
	if current.OwnerID != userID {
		return Guild{}, ErrForbidden
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
	guild, err := s.store.GetGuild(ctx, userID, guildID)
	if err != nil {
		return Channel{}, err
	}
	if guild.OwnerID != userID {
		return Channel{}, ErrForbidden
	}
	if input.Type != ChannelTypeText && input.Type != ChannelTypeVoice {
		return Channel{}, &ValidationError{Field: "type", Message: "must be TEXT or VOICE"}
	}
	name, err := validateName("name", input.Name)
	if err != nil {
		return Channel{}, err
	}
	topic, err := normalizeOptionalText("topic", input.Topic, 1024)
	if err != nil {
		return Channel{}, err
	}
	if input.Type == ChannelTypeVoice && topic != nil {
		return Channel{}, &ValidationError{Field: "topic", Message: "must be omitted for a VOICE channel"}
	}
	id, err := newUUIDv7()
	if err != nil {
		return Channel{}, err
	}
	return s.store.CreateChannel(ctx, Channel{
		ID: id, GuildID: guildID, Type: input.Type, Name: name, Topic: topic, CreatedAt: s.now().UTC(),
	})
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

func (s *Service) UpdateChannel(ctx context.Context, userID, channelID uuid.UUID, input UpdateChannelInput) (Channel, error) {
	if input.Name == nil && !input.Topic.Set && input.Position == nil {
		return Channel{}, &ValidationError{Field: "body", Message: "must contain at least one field"}
	}
	channel, err := s.store.GetChannel(ctx, userID, channelID)
	if err != nil {
		return Channel{}, err
	}
	guild, err := s.store.GetGuild(ctx, userID, channel.GuildID)
	if err != nil {
		return Channel{}, err
	}
	if guild.OwnerID != userID {
		return Channel{}, ErrForbidden
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
		if channel.Type == ChannelTypeVoice && value != nil {
			return Channel{}, &ValidationError{Field: "topic", Message: "must be null for a VOICE channel"}
		}
		input.Topic.Value = value
	}
	if input.Position != nil && *input.Position < 0 {
		return Channel{}, &ValidationError{Field: "position", Message: "must be zero or greater"}
	}
	return s.store.UpdateChannel(ctx, userID, channelID, input, s.now().UTC())
}

func (s *Service) DeleteChannel(ctx context.Context, userID, channelID uuid.UUID) error {
	channel, err := s.store.GetChannel(ctx, userID, channelID)
	if err != nil {
		return err
	}
	guild, err := s.store.GetGuild(ctx, userID, channel.GuildID)
	if err != nil {
		return err
	}
	if guild.OwnerID != userID {
		return ErrForbidden
	}
	return s.store.DeleteChannel(ctx, userID, channelID)
}

func (s *Service) CreateMessage(ctx context.Context, userID, channelID uuid.UUID, content string) (Message, error) {
	channel, err := s.store.GetChannel(ctx, userID, channelID)
	if err != nil {
		return Message{}, err
	}
	if channel.Type != ChannelTypeText {
		return Message{}, ErrNotFound
	}
	if err := validateContent(content); err != nil {
		return Message{}, err
	}
	id, err := newUUIDv7()
	if err != nil {
		return Message{}, err
	}
	return s.store.CreateMessage(ctx, Message{
		ID: id, ChannelID: channelID, Author: UserSummary{ID: userID}, Content: content, CreatedAt: s.now().UTC(),
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
	return s.store.DeleteMessage(ctx, userID, channelID, messageID)
}

func (s *Service) ListChannelMemberIDs(ctx context.Context, channelID uuid.UUID) ([]uuid.UUID, error) {
	return s.store.ListChannelMemberIDs(ctx, channelID)
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
	if (kind == cursorGuilds || kind == cursorMessages) && cursor.Time.IsZero() {
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
