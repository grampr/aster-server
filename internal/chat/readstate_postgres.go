package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const maxReadStates = 1000

// UpdateReadState moves the caller's read position to messageID. The position never
// moves backward; the returned bool reports whether it changed.
func (s *PostgresStore) UpdateReadState(ctx context.Context, userID, channelID, messageID uuid.UUID, now time.Time) (ReadState, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ReadState{}, false, fmt.Errorf("begin read state update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var newTime time.Time
	err = tx.QueryRow(ctx, `
		SELECT m.created_at
		FROM messages m
		JOIN channels c ON c.id = m.channel_id AND c.type IN ('TEXT', 'THREAD', 'DIRECT')
		JOIN channel_participants cp ON cp.channel_id = c.id AND cp.user_id = $3
		WHERE m.id = $1 AND m.channel_id = $2`, messageID, channelID, userID).Scan(&newTime)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReadState{}, false, ErrNotFound
	}
	if err != nil {
		return ReadState{}, false, fmt.Errorf("check read state target: %w", err)
	}

	state := ReadState{ChannelID: channelID, LastReadMessageID: &messageID, UpdatedAt: now}
	var currentID uuid.UUID
	var currentUpdatedAt time.Time
	var currentTime *time.Time
	err = tx.QueryRow(ctx, `
		SELECT rs.last_read_message_id, rs.updated_at, m.created_at
		FROM read_states rs
		LEFT JOIN messages m ON m.id = rs.last_read_message_id
		WHERE rs.user_id = $1 AND rs.channel_id = $2
		FOR UPDATE OF rs`, userID, channelID).Scan(&currentID, &currentUpdatedAt, &currentTime)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return ReadState{}, false, fmt.Errorf("lock read state: %w", err)
	default:
		// Equal times are ordered by ID, matching Message paging.
		behind := currentTime != nil && (newTime.Before(*currentTime) || (newTime.Equal(*currentTime) && messageID.String() <= currentID.String()))
		if behind {
			return ReadState{ChannelID: channelID, LastReadMessageID: &currentID, UpdatedAt: currentUpdatedAt}, false, nil
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO read_states (user_id, channel_id, last_read_message_id, updated_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id, channel_id) DO UPDATE
		SET last_read_message_id = EXCLUDED.last_read_message_id, updated_at = EXCLUDED.updated_at`,
		userID, channelID, messageID, now); err != nil {
		return ReadState{}, false, fmt.Errorf("upsert read state: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ReadState{}, false, fmt.Errorf("commit read state: %w", err)
	}
	return state, true, nil
}

// ListReadStates returns a position for every readable text-like Channel, with a nil
// position for Channels the caller has never read.
func (s *PostgresStore) ListReadStates(ctx context.Context, userID uuid.UUID) ([]ReadState, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT c.id, rs.last_read_message_id, COALESCE(rs.updated_at, c.created_at)
		FROM channels c
		JOIN channel_participants cp ON cp.channel_id = c.id AND cp.user_id = $1
		LEFT JOIN read_states rs ON rs.channel_id = c.id AND rs.user_id = $1
		WHERE c.type IN ('TEXT', 'THREAD', 'DIRECT')
		ORDER BY c.id
		LIMIT $2`, userID, maxReadStates)
	if err != nil {
		return nil, fmt.Errorf("list read states: %w", err)
	}
	defer rows.Close()
	states := make([]ReadState, 0)
	for rows.Next() {
		var state ReadState
		if err := rows.Scan(&state.ChannelID, &state.LastReadMessageID, &state.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan read state: %w", err)
		}
		states = append(states, state)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate read states: %w", err)
	}
	return states, nil
}

// SearchMessages finds Messages whose body contains the query, case-insensitively,
// across the Guild's text-like Channels.
func (s *PostgresStore) SearchMessages(ctx context.Context, userID, guildID uuid.UUID, input SearchInput, cursor *pageCursor, limit int) ([]Message, error) {
	var cursorTime *time.Time
	cursorID := uuid.Nil
	if cursor != nil {
		cursorTime, cursorID = &cursor.Time, cursor.ID
	}
	rows, err := s.pool.Query(ctx, `
		SELECT m.id, m.channel_id, u.id, u.display_name, u.avatar_url, m.content,
		       m.reply_to_message_id, m.created_at, m.edited_at,
		       reply.id, reply.channel_id, reply_author.id, reply_author.display_name,
		       reply_author.avatar_url, reply.content, reply.created_at, reply.edited_at
		FROM messages m
		JOIN channels c ON c.id = m.channel_id AND c.guild_id = $1 AND c.type IN ('TEXT', 'THREAD')
		JOIN guild_members gm ON gm.guild_id = c.guild_id AND gm.user_id = $2
		JOIN users u ON u.id = m.author_id
		LEFT JOIN messages reply ON reply.id = m.reply_to_message_id AND reply.channel_id = m.channel_id
		LEFT JOIN users reply_author ON reply_author.id = reply.author_id
		WHERE m.content ILIKE $3 ESCAPE '\'
		  AND ($4::uuid IS NULL OR m.channel_id = $4)
		  AND ($5::uuid IS NULL OR m.author_id = $5)
		  AND ($6::timestamptz IS NULL OR (m.created_at, m.id) < ($6, $7))
		ORDER BY m.created_at DESC, m.id DESC
		LIMIT $8`,
		guildID, userID, "%"+escapeLike(input.Query)+"%", input.ChannelID, input.AuthorID, cursorTime, cursorID, limit)
	if err != nil {
		return nil, fmt.Errorf("search messages: %w", err)
	}
	defer rows.Close()
	items := make([]Message, 0, limit)
	for rows.Next() {
		var item Message
		if err := scanMessage(rows, &item); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate search results: %w", err)
	}
	if len(items) == 0 {
		var isMember bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM guild_members WHERE guild_id = $1 AND user_id = $2)`, guildID, userID).Scan(&isMember); err != nil {
			return nil, fmt.Errorf("check guild membership: %w", err)
		}
		if !isMember {
			return nil, ErrNotFound
		}
	}
	pointers := make([]*Message, len(items))
	for index := range items {
		pointers[index] = &items[index]
	}
	if err := s.populateMessageReactions(ctx, userID, pointers...); err != nil {
		return nil, err
	}
	return items, nil
}

func escapeLike(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
}
