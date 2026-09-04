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

func (s *PostgresStore) IsDirectChannel(ctx context.Context, channelID uuid.UUID) (bool, error) {
	var direct bool
	if err := s.pool.QueryRow(ctx, `SELECT type = 'DIRECT' FROM channels WHERE id = $1`, channelID).Scan(&direct); errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotFound
	} else if err != nil {
		return false, fmt.Errorf("identify channel message intent: %w", err)
	}
	return direct, nil
}

func (s *PostgresStore) CreateDirectChannel(ctx context.Context, userID, recipientID uuid.UUID, channel Channel) (Channel, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Channel{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var recipientExists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1)`, recipientID).Scan(&recipientExists); err != nil {
		return Channel{}, err
	}
	if !recipientExists {
		return Channel{}, ErrNotFound
	}
	first, second := userID.String(), recipientID.String()
	if first > second {
		first, second = second, first
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, first+":"+second); err != nil {
		return Channel{}, err
	}
	var existingID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT c.id FROM channels c JOIN direct_channel_members dm ON dm.channel_id=c.id WHERE c.type='DIRECT' AND dm.user_id=ANY($1) GROUP BY c.id HAVING count(*)=2 AND count(*) FILTER(WHERE dm.user_id=ANY($1))=2 LIMIT 1`, []uuid.UUID{userID, recipientID}).Scan(&existingID)
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return Channel{}, err
		}
		return s.GetChannel(ctx, userID, existingID)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Channel{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO channels(id,guild_id,type,name,topic,position,created_at,updated_at) VALUES($1,NULL,'DIRECT',NULL,NULL,0,$2,$2)`, channel.ID, channel.CreatedAt); err != nil {
		return Channel{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO direct_channel_members(channel_id,user_id,joined_at) VALUES($1,$2,$4),($1,$3,$4)`, channel.ID, userID, recipientID, channel.CreatedAt); err != nil {
		return Channel{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Channel{}, err
	}
	return s.GetChannel(ctx, userID, channel.ID)
}

func (s *PostgresStore) populateDirectRecipients(ctx context.Context, channel *Channel) error {
	rows, err := s.pool.Query(ctx, `SELECT u.id,u.display_name,u.avatar_url FROM direct_channel_members dm JOIN users u ON u.id=dm.user_id WHERE dm.channel_id=$1 ORDER BY u.id`, channel.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	channel.Recipients = []UserSummary{}
	for rows.Next() {
		var user UserSummary
		if err := rows.Scan(&user.ID, &user.DisplayName, &user.AvatarURL); err != nil {
			return err
		}
		channel.Recipients = append(channel.Recipients, user)
	}
	return rows.Err()
}

func (s *PostgresStore) ListDirectChannels(ctx context.Context, userID uuid.UUID, cursor *pageCursor, limit int) ([]Channel, error) {
	var cursorTime *time.Time
	cursorID := uuid.Nil
	if cursor != nil {
		cursorTime = &cursor.Time
		cursorID = cursor.ID
	}
	rows, err := s.pool.Query(ctx, `SELECT c.id,c.guild_id,c.type,c.name,c.topic,c.position,c.created_at,c.parent_id,c.starter_message_id FROM channels c JOIN direct_channel_members dm ON dm.channel_id=c.id WHERE c.type='DIRECT' AND dm.user_id=$1 AND ($2::timestamptz IS NULL OR (c.updated_at,c.id)<($2,$3)) ORDER BY c.updated_at DESC,c.id DESC LIMIT $4`, userID, cursorTime, cursorID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Channel{}
	for rows.Next() {
		var item Channel
		if err := scanChannel(rows, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range items {
		if err := s.populateDirectRecipients(ctx, &items[i]); err != nil {
			return nil, err
		}
	}
	return items, nil
}

func (s *PostgresStore) CreateThread(ctx context.Context, userID, parentID uuid.UUID, channel Channel) (Channel, error) {
	err := scanChannel(s.pool.QueryRow(ctx, `INSERT INTO channels(id,guild_id,type,name,topic,position,parent_id,starter_message_id,created_at,updated_at) SELECT $1,c.guild_id,'THREAD',$3,NULL,COALESCE((SELECT max(position)+1 FROM channels WHERE parent_id=$2),0),$2,$4,$5,$5 FROM channels c JOIN guild_members gm ON gm.guild_id=c.guild_id AND gm.user_id=$6 WHERE c.id=$2 AND c.type='TEXT' RETURNING id,guild_id,type,name,topic,position,created_at,parent_id,starter_message_id`, channel.ID, parentID, channel.Name, channel.StarterMessageID, channel.CreatedAt, userID), &channel)
	if errors.Is(err, pgx.ErrNoRows) {
		return Channel{}, ErrNotFound
	}
	return channel, err
}

func (s *PostgresStore) ListThreads(ctx context.Context, userID, parentID uuid.UUID, cursor *pageCursor, limit int) ([]Channel, error) {
	var cursorTime *time.Time
	cursorID := uuid.Nil
	if cursor != nil {
		cursorTime = &cursor.Time
		cursorID = cursor.ID
	}
	rows, err := s.pool.Query(ctx, `SELECT c.id,c.guild_id,c.type,c.name,c.topic,c.position,c.created_at,c.parent_id,c.starter_message_id FROM channels c JOIN guild_members gm ON gm.guild_id=c.guild_id AND gm.user_id=$2 WHERE c.parent_id=$1 AND c.type='THREAD' AND ($3::timestamptz IS NULL OR (c.updated_at,c.id)<($3,$4)) ORDER BY c.updated_at DESC,c.id DESC LIMIT $5`, parentID, userID, cursorTime, cursorID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Channel{}
	for rows.Next() {
		var item Channel
		if err := scanChannel(rows, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *PostgresStore) SearchMessages(ctx context.Context, userID, guildID uuid.UUID, input MessageSearchInput, cursor *pageCursor, limit int) ([]MessageSearchResult, error) {
	var cursorTime *time.Time
	cursorID := uuid.Nil
	if cursor != nil {
		cursorTime = &cursor.Time
		cursorID = cursor.ID
	}
	pattern := "%" + strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(input.Query, "\\", "\\\\"), "%", "\\%"), "_", "\\_") + "%"
	rows, err := s.pool.Query(ctx, `SELECT m.id,m.channel_id,u.id,u.display_name,u.avatar_url,m.content,m.reply_to_message_id,m.created_at,m.edited_at,reply.id,reply.channel_id,reply_author.id,reply_author.display_name,reply_author.avatar_url,reply.content,reply.created_at,reply.edited_at FROM messages m JOIN channels c ON c.id=m.channel_id JOIN guild_members gm ON gm.guild_id=c.guild_id AND gm.user_id=$2 JOIN users u ON u.id=m.author_id LEFT JOIN messages reply ON reply.id=m.reply_to_message_id LEFT JOIN users reply_author ON reply_author.id=reply.author_id WHERE c.guild_id=$1 AND m.content ILIKE $3 ESCAPE '\' AND ($4::uuid IS NULL OR c.id=$4) AND ($5::uuid IS NULL OR m.author_id=$5) AND ($6::timestamptz IS NULL OR (m.created_at,m.id)<($6,$7)) ORDER BY m.created_at DESC,m.id DESC LIMIT $8`, guildID, userID, pattern, input.ChannelID, input.AuthorID, cursorTime, cursorID, limit)
	if err != nil {
		return nil, fmt.Errorf("search messages: %w", err)
	}
	defer rows.Close()
	items := []MessageSearchResult{}
	for rows.Next() {
		var result MessageSearchResult
		if err := scanMessage(rows, &result.Message); err != nil {
			return nil, err
		}
		result.Excerpt = searchExcerpt(result.Message.Content, input.Query, 500)
		items = append(items, result)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	pointers := make([]*Message, len(items))
	for index := range items {
		pointers[index] = &items[index].Message
	}
	if err := s.populateMessageReactions(ctx, userID, pointers...); err != nil {
		return nil, err
	}
	return items, nil
}

func searchExcerpt(content, query string, maximum int) string {
	runes := []rune(content)
	if len(runes) <= maximum {
		return content
	}
	lower := strings.ToLower(content)
	index := strings.Index(lower, strings.ToLower(query))
	if index < 0 {
		return string(runes[:maximum])
	}
	prefixRunes := []rune(content[:index])
	start := len(prefixRunes) - maximum/3
	if start < 0 {
		start = 0
	}
	end := start + maximum
	if end > len(runes) {
		end = len(runes)
	}
	return string(runes[start:end])
}

func (s *PostgresStore) ListReadStates(ctx context.Context, userID uuid.UUID) ([]ReadState, error) {
	rows, err := s.pool.Query(ctx, `SELECT rs.channel_id,rs.last_read_message_id,rs.updated_at FROM channel_read_states rs JOIN channels c ON c.id=rs.channel_id LEFT JOIN guild_members gm ON gm.guild_id=c.guild_id AND gm.user_id=$1 LEFT JOIN direct_channel_members dm ON dm.channel_id=c.id AND dm.user_id=$1 WHERE rs.user_id=$1 AND (gm.user_id IS NOT NULL OR dm.user_id IS NOT NULL) ORDER BY rs.updated_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []ReadState{}
	for rows.Next() {
		var item ReadState
		if err := rows.Scan(&item.ChannelID, &item.LastReadMessageID, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *PostgresStore) UpdateReadState(ctx context.Context, userID, channelID, messageID uuid.UUID, updatedAt time.Time) (ReadState, error) {
	var state ReadState
	err := s.pool.QueryRow(ctx, `INSERT INTO channel_read_states(channel_id,user_id,last_read_message_id,updated_at) SELECT $1,$2,$3,$4 FROM messages target JOIN channels c ON c.id=target.channel_id LEFT JOIN guild_members gm ON gm.guild_id=c.guild_id AND gm.user_id=$2 LEFT JOIN direct_channel_members dm ON dm.channel_id=c.id AND dm.user_id=$2 WHERE target.id=$3 AND target.channel_id=$1 AND (gm.user_id IS NOT NULL OR dm.user_id IS NOT NULL) ON CONFLICT(channel_id,user_id) DO UPDATE SET last_read_message_id=EXCLUDED.last_read_message_id,updated_at=EXCLUDED.updated_at WHERE (SELECT created_at FROM messages WHERE id=channel_read_states.last_read_message_id)<=(SELECT created_at FROM messages WHERE id=EXCLUDED.last_read_message_id) RETURNING channel_id,last_read_message_id,updated_at`, channelID, userID, messageID, updatedAt).Scan(&state.ChannelID, &state.LastReadMessageID, &state.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = s.pool.QueryRow(ctx, `SELECT channel_id,last_read_message_id,updated_at FROM channel_read_states WHERE channel_id=$1 AND user_id=$2`, channelID, userID).Scan(&state.ChannelID, &state.LastReadMessageID, &state.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ReadState{}, ErrNotFound
		}
	}
	return state, err
}
