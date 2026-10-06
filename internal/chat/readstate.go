package chat

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	cursorSearch = "search"

	minSearchLength    = 2
	maxSearchLength    = 100
	excerptContextRuns = 100
	excerptMaxRunes    = 480
)

// UpdateReadState records that the caller has read up to messageID. The returned bool
// reports whether the stored position changed.
func (s *Service) UpdateReadState(ctx context.Context, userID, channelID, messageID uuid.UUID) (ReadState, bool, error) {
	if messageID == uuid.Nil {
		return ReadState{}, false, &ValidationError{Field: "last_read_message_id", Message: "must be a UUID"}
	}
	return s.store.UpdateReadState(ctx, userID, channelID, messageID, s.now().UTC())
}

func (s *Service) ListReadStates(ctx context.Context, userID uuid.UUID) ([]ReadState, error) {
	return s.store.ListReadStates(ctx, userID)
}

func (s *Service) SearchMessages(ctx context.Context, userID, guildID uuid.UUID, input SearchInput, cursorValue string, limit int) (Page[SearchResult], error) {
	input.Query = strings.TrimSpace(input.Query)
	if length := utf8.RuneCountInString(input.Query); length < minSearchLength || length > maxSearchLength {
		return Page[SearchResult]{}, &ValidationError{Field: "query", Message: "must contain between 2 and 100 characters"}
	}
	cursor, err := decodeCursor(cursorValue, cursorSearch)
	if err != nil {
		return Page[SearchResult]{}, err
	}
	if err := validateLimit(limit); err != nil {
		return Page[SearchResult]{}, err
	}
	rows, err := s.store.SearchMessages(ctx, userID, guildID, input, cursor, limit+1)
	if err != nil {
		return Page[SearchResult]{}, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	items := make([]SearchResult, len(rows))
	for index := range rows {
		items[index] = SearchResult{Message: rows[index], Excerpt: makeExcerpt(rows[index].Content, input.Query)}
	}
	var next *string
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		value, err := encodeCursor(pageCursor{Kind: cursorSearch, Time: last.CreatedAt, ID: last.ID})
		if err != nil {
			return Page[SearchResult]{}, err
		}
		next = &value
	}
	return Page[SearchResult]{Items: items, HasMore: hasMore, NextCursor: next}, nil
}

// makeExcerpt returns plain text around the first case-insensitive match of query.
func makeExcerpt(content, query string) string {
	runes := []rune(content)
	queryLength := utf8.RuneCountInString(query)
	match := 0
	for index := 0; index+queryLength <= len(runes); index++ {
		if strings.EqualFold(string(runes[index:index+queryLength]), query) {
			match = index
			break
		}
	}
	start := match - excerptContextRuns
	if start < 0 {
		start = 0
	}
	end := start + excerptMaxRunes
	if end > len(runes) {
		end = len(runes)
	}
	excerpt := string(runes[start:end])
	if start > 0 {
		excerpt = "…" + excerpt
	}
	if end < len(runes) {
		excerpt += "…"
	}
	return excerpt
}
