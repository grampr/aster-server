package chat

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	cursorDirectChannels = "direct_channels"
	cursorThreads        = "threads"
	cursorSearch         = "message_search"
)

func (s *Service) requireTextStore() (TextStore, error) {
	if s.textStore == nil {
		return nil, &ValidationError{Field: "server", Message: "text extension store is unavailable"}
	}
	return s.textStore, nil
}

func (s *Service) IsDirectChannel(ctx context.Context, channelID uuid.UUID) (bool, error) {
	store, err := s.requireTextStore()
	if err != nil {
		return false, err
	}
	return store.IsDirectChannel(ctx, channelID)
}

func (s *Service) CreateDirectChannel(ctx context.Context, userID, recipientID uuid.UUID) (Channel, error) {
	store, err := s.requireTextStore()
	if err != nil {
		return Channel{}, err
	}
	if recipientID == uuid.Nil || recipientID == userID {
		return Channel{}, &ValidationError{Field: "recipient_id", Message: "must identify another user"}
	}
	id, err := newUUIDv7()
	if err != nil {
		return Channel{}, err
	}
	return store.CreateDirectChannel(ctx, userID, recipientID, Channel{ID: id, Type: ChannelTypeDirect, CreatedAt: s.now().UTC()})
}

func (s *Service) ListDirectChannels(ctx context.Context, userID uuid.UUID, cursorValue string, limit int) (Page[Channel], error) {
	store, err := s.requireTextStore()
	if err != nil {
		return Page[Channel]{}, err
	}
	cursor, err := decodeCursor(cursorValue, cursorDirectChannels)
	if err != nil {
		return Page[Channel]{}, err
	}
	if err := validateLimit(limit); err != nil {
		return Page[Channel]{}, err
	}
	items, err := store.ListDirectChannels(ctx, userID, cursor, limit+1)
	if err != nil {
		return Page[Channel]{}, err
	}
	return channelPage(items, limit, cursorDirectChannels)
}

func (s *Service) CreateThread(ctx context.Context, userID, parentID uuid.UUID, input CreateThreadInput) (Channel, error) {
	store, err := s.requireTextStore()
	if err != nil {
		return Channel{}, err
	}
	parent, err := s.GetChannel(ctx, userID, parentID)
	if err != nil {
		return Channel{}, err
	}
	if parent.Type != ChannelTypeText {
		return Channel{}, ErrNotFound
	}
	if err := s.requirePermission(ctx, userID, parent.GuildID, permissionSendMessages); err != nil {
		return Channel{}, err
	}
	name, err := validateName("name", input.Name)
	if err != nil {
		return Channel{}, err
	}
	if input.MessageID != nil {
		if _, err := s.store.GetMessage(ctx, userID, parentID, *input.MessageID); err != nil {
			return Channel{}, err
		}
	}
	id, err := newUUIDv7()
	if err != nil {
		return Channel{}, err
	}
	return store.CreateThread(ctx, userID, parentID, Channel{ID: id, GuildID: parent.GuildID, ParentID: &parentID, StarterMessageID: input.MessageID, Type: ChannelTypeThread, Name: name, CreatedAt: s.now().UTC()})
}

func (s *Service) ListThreads(ctx context.Context, userID, parentID uuid.UUID, cursorValue string, limit int) (Page[Channel], error) {
	store, err := s.requireTextStore()
	if err != nil {
		return Page[Channel]{}, err
	}
	parent, err := s.GetChannel(ctx, userID, parentID)
	if err != nil {
		return Page[Channel]{}, err
	}
	if parent.Type != ChannelTypeText {
		return Page[Channel]{}, ErrNotFound
	}
	cursor, err := decodeCursor(cursorValue, cursorThreads)
	if err != nil {
		return Page[Channel]{}, err
	}
	if err := validateLimit(limit); err != nil {
		return Page[Channel]{}, err
	}
	items, err := store.ListThreads(ctx, userID, parentID, cursor, limit+1)
	if err != nil {
		return Page[Channel]{}, err
	}
	return channelPage(items, limit, cursorThreads)
}

func channelPage(items []Channel, limit int, kind string) (Page[Channel], error) {
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	var next *string
	if hasMore && len(items) > 0 {
		value, err := encodeCursor(pageCursor{Kind: kind, Time: items[len(items)-1].CreatedAt, ID: items[len(items)-1].ID})
		if err != nil {
			return Page[Channel]{}, err
		}
		next = &value
	}
	return Page[Channel]{Items: items, HasMore: hasMore, NextCursor: next}, nil
}

func (s *Service) SearchMessages(ctx context.Context, userID, guildID uuid.UUID, input MessageSearchInput, cursorValue string, limit int) (Page[MessageSearchResult], error) {
	store, err := s.requireTextStore()
	if err != nil {
		return Page[MessageSearchResult]{}, err
	}
	input.Query = strings.TrimSpace(input.Query)
	if n := utf8.RuneCountInString(input.Query); n < 2 || n > 100 {
		return Page[MessageSearchResult]{}, &ValidationError{Field: "query", Message: "must contain between 2 and 100 characters"}
	}
	if _, err := s.store.GetGuild(ctx, userID, guildID); err != nil {
		return Page[MessageSearchResult]{}, err
	}
	if err := s.requirePermission(ctx, userID, guildID, permissionViewChannel); err != nil {
		return Page[MessageSearchResult]{}, err
	}
	cursor, err := decodeCursor(cursorValue, cursorSearch)
	if err != nil {
		return Page[MessageSearchResult]{}, err
	}
	if err := validateLimit(limit); err != nil {
		return Page[MessageSearchResult]{}, err
	}
	items, err := store.SearchMessages(ctx, userID, guildID, input, cursor, limit+1)
	if err != nil {
		return Page[MessageSearchResult]{}, err
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	var next *string
	if hasMore && len(items) > 0 {
		last := items[len(items)-1].Message
		value, err := encodeCursor(pageCursor{Kind: cursorSearch, Time: last.CreatedAt, ID: last.ID})
		if err != nil {
			return Page[MessageSearchResult]{}, err
		}
		next = &value
	}
	return Page[MessageSearchResult]{Items: items, HasMore: hasMore, NextCursor: next}, nil
}

func (s *Service) ListReadStates(ctx context.Context, userID uuid.UUID) ([]ReadState, error) {
	store, err := s.requireTextStore()
	if err != nil {
		return nil, err
	}
	return store.ListReadStates(ctx, userID)
}
func (s *Service) UpdateReadState(ctx context.Context, userID, channelID, messageID uuid.UUID) (ReadState, error) {
	store, err := s.requireTextStore()
	if err != nil {
		return ReadState{}, err
	}
	if _, err := s.store.GetMessage(ctx, userID, channelID, messageID); err != nil {
		return ReadState{}, err
	}
	return store.UpdateReadState(ctx, userID, channelID, messageID, s.now().UTC())
}
