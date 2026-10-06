package chat

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const channelColumns = `c.id, c.guild_id, c.type, c.name, c.topic, c.parent_id, c.position, c.created_at`

func scanChannel(row rowScanner, channel *Channel) error {
	if err := row.Scan(&channel.ID, &channel.GuildID, &channel.Type, &channel.Name, &channel.Topic, &channel.ParentID, &channel.Position, &channel.CreatedAt); err != nil {
		return err
	}
	channel.Recipients = []UserSummary{}
	return nil
}

func scanChannelRow(row rowScanner, item *channelListRow) error {
	if err := row.Scan(&item.ID, &item.GuildID, &item.Type, &item.Name, &item.Topic, &item.ParentID, &item.Position, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return err
	}
	item.Recipients = []UserSummary{}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgError *pgconn.PgError
	return errors.As(err, &pgError) && pgError.Code == "23505"
}

// populateRecipients fills in the participants of Direct Message Channels.
func (s *PostgresStore) populateRecipients(ctx context.Context, channels ...*Channel) error {
	byID := make(map[uuid.UUID]*Channel)
	ids := make([]uuid.UUID, 0)
	for _, channel := range channels {
		if channel.Type == ChannelTypeDirect {
			byID[channel.ID] = channel
			ids = append(ids, channel.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT dp.channel_id, u.id, u.display_name, u.avatar_url
		FROM dm_participants dp
		JOIN users u ON u.id = dp.user_id
		WHERE dp.channel_id = ANY($1)
		ORDER BY dp.channel_id, u.id`, ids)
	if err != nil {
		return fmt.Errorf("list direct message recipients: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var channelID uuid.UUID
		var recipient UserSummary
		if err := rows.Scan(&channelID, &recipient.ID, &recipient.DisplayName, &recipient.AvatarURL); err != nil {
			return fmt.Errorf("scan direct message recipient: %w", err)
		}
		byID[channelID].Recipients = append(byID[channelID].Recipients, recipient)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate direct message recipients: %w", err)
	}
	return nil
}

func (s *PostgresStore) CreateChannel(ctx context.Context, channel Channel) (Channel, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Channel{}, fmt.Errorf("begin channel creation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := tx.QueryRow(ctx, `SELECT id FROM guilds WHERE id = $1 FOR UPDATE`, channel.GuildID).Scan(new(uuid.UUID)); errors.Is(err, pgx.ErrNoRows) {
		return Channel{}, ErrNotFound
	} else if err != nil {
		return Channel{}, fmt.Errorf("lock guild for channel creation: %w", err)
	}
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(position), -1) + 1 FROM channels WHERE guild_id = $1 AND type <> 'THREAD'`, channel.GuildID).Scan(&channel.Position); err != nil {
		return Channel{}, fmt.Errorf("select channel position: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO channels (id, guild_id, type, name, topic, parent_id, position, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)`,
		channel.ID, channel.GuildID, channel.Type, channel.Name, channel.Topic, channel.ParentID, channel.Position, channel.CreatedAt,
	); err != nil {
		return Channel{}, fmt.Errorf("insert channel: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Channel{}, fmt.Errorf("commit channel creation: %w", err)
	}
	channel.Recipients = []UserSummary{}
	return channel, nil
}

func (s *PostgresStore) ListChannels(ctx context.Context, userID, guildID uuid.UUID, cursor *pageCursor, limit int) ([]Channel, error) {
	var cursorPosition *int
	cursorID := uuid.Nil
	if cursor != nil {
		cursorPosition, cursorID = &cursor.Position, cursor.ID
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+channelColumns+`
		FROM channels c
		JOIN guild_members gm ON gm.guild_id = c.guild_id
		WHERE c.guild_id = $1 AND gm.user_id = $2 AND c.type <> 'THREAD'
		  AND ($3::integer IS NULL OR (c.position, c.id) > ($3, $4))
		ORDER BY c.position, c.id
		LIMIT $5`, guildID, userID, cursorPosition, cursorID, limit)
	if err != nil {
		return nil, fmt.Errorf("list channels: %w", err)
	}
	defer rows.Close()
	items := make([]Channel, 0, limit)
	for rows.Next() {
		var item Channel
		if err := scanChannel(rows, &item); err != nil {
			return nil, fmt.Errorf("scan channel: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate channels: %w", err)
	}
	if len(items) == 0 {
		var exists bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM guild_members WHERE guild_id = $1 AND user_id = $2)`, guildID, userID).Scan(&exists); err != nil {
			return nil, fmt.Errorf("check guild membership: %w", err)
		}
		if !exists {
			return nil, ErrNotFound
		}
	}
	return items, nil
}

func (s *PostgresStore) GetChannel(ctx context.Context, userID, channelID uuid.UUID) (Channel, error) {
	var channel Channel
	err := scanChannel(s.pool.QueryRow(ctx, `
		SELECT `+channelColumns+`
		FROM channels c
		JOIN channel_participants cp ON cp.channel_id = c.id
		WHERE c.id = $1 AND cp.user_id = $2`, channelID, userID), &channel)
	if errors.Is(err, pgx.ErrNoRows) {
		return Channel{}, ErrNotFound
	}
	if err != nil {
		return Channel{}, fmt.Errorf("get channel: %w", err)
	}
	if err := s.populateRecipients(ctx, &channel); err != nil {
		return Channel{}, err
	}
	return channel, nil
}

// UpdateChannel applies already-authorized changes for a Guild Member.
func (s *PostgresStore) UpdateChannel(ctx context.Context, userID, channelID uuid.UUID, input UpdateChannelInput, updatedAt time.Time) (Channel, error) {
	var channel Channel
	err := scanChannel(s.pool.QueryRow(ctx, `
		UPDATE channels c
		SET name = COALESCE($3, c.name),
		    topic = CASE WHEN $4 THEN $5 ELSE c.topic END,
		    position = COALESCE($6, c.position),
		    parent_id = CASE WHEN $7 THEN $8 ELSE c.parent_id END,
		    updated_at = $9
		WHERE c.id = $1
		  AND EXISTS (SELECT 1 FROM guild_members gm WHERE gm.guild_id = c.guild_id AND gm.user_id = $2)
		RETURNING `+channelColumns,
		channelID, userID, input.Name, input.Topic.Set, input.Topic.Value, input.Position,
		input.ParentID.Set, input.ParentID.Value, updatedAt), &channel)
	if errors.Is(err, pgx.ErrNoRows) {
		return Channel{}, ErrNotFound
	}
	if err != nil {
		return Channel{}, fmt.Errorf("update channel: %w", err)
	}
	return channel, nil
}

// DeleteChannel deletes a Guild Channel together with the Threads it parents.
// Channels inside a deleted Category lose their parent but are kept.
func (s *PostgresStore) DeleteChannel(ctx context.Context, userID, channelID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM channels c
		WHERE (c.id = $1 OR (c.parent_id = $1 AND c.type = 'THREAD'))
		  AND EXISTS (SELECT 1 FROM guild_members gm WHERE gm.guild_id = c.guild_id AND gm.user_id = $2)`, channelID, userID)
	if err != nil {
		return fmt.Errorf("delete channel: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) CreateThread(ctx context.Context, thread Channel, starterMessageID *uuid.UUID, now time.Time) (Channel, error) {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO channels (id, guild_id, type, name, parent_id, position, starter_message_id, created_at, updated_at)
		VALUES ($1, $2, 'THREAD', $3, $4, 0, $5, $6, $6)`,
		thread.ID, thread.GuildID, thread.Name, thread.ParentID, starterMessageID, now)
	if isUniqueViolation(err) {
		return Channel{}, ErrThreadExists
	}
	if err != nil {
		return Channel{}, fmt.Errorf("insert thread: %w", err)
	}
	thread.Type, thread.CreatedAt, thread.Recipients = ChannelTypeThread, now, []UserSummary{}
	return thread, nil
}

func (s *PostgresStore) ListThreads(ctx context.Context, userID, parentID uuid.UUID, cursor *pageCursor, limit int) ([]channelListRow, error) {
	var cursorTime *time.Time
	cursorID := uuid.Nil
	if cursor != nil {
		cursorTime, cursorID = &cursor.Time, cursor.ID
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+channelColumns+`, c.updated_at
		FROM channels c
		JOIN channel_participants cp ON cp.channel_id = c.id AND cp.user_id = $2
		WHERE c.parent_id = $1 AND c.type = 'THREAD'
		  AND ($3::timestamptz IS NULL OR (c.updated_at, c.id) < ($3, $4))
		ORDER BY c.updated_at DESC, c.id DESC
		LIMIT $5`, parentID, userID, cursorTime, cursorID, limit)
	if err != nil {
		return nil, fmt.Errorf("list threads: %w", err)
	}
	items, err := collectChannelRows(rows)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		var exists bool
		if err := s.pool.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM channels c
				JOIN channel_participants cp ON cp.channel_id = c.id AND cp.user_id = $2
				WHERE c.id = $1 AND c.type = 'TEXT')`, parentID, userID).Scan(&exists); err != nil {
			return nil, fmt.Errorf("check thread parent access: %w", err)
		}
		if !exists {
			return nil, ErrNotFound
		}
	}
	return items, nil
}

func collectChannelRows(rows pgx.Rows) ([]channelListRow, error) {
	defer rows.Close()
	items := make([]channelListRow, 0)
	for rows.Next() {
		var item channelListRow
		if err := scanChannelRow(rows, &item); err != nil {
			return nil, fmt.Errorf("scan channel: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate channels: %w", err)
	}
	return items, nil
}

// OpenDirectChannel returns the Direct Message between two Users, creating it when
// missing. The returned bool reports whether it was created.
func (s *PostgresStore) OpenDirectChannel(ctx context.Context, channel Channel, userID, recipientID uuid.UUID) (Channel, bool, error) {
	var recipientExists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id = $1)`, recipientID).Scan(&recipientExists); err != nil {
		return Channel{}, false, fmt.Errorf("check direct message recipient: %w", err)
	}
	if !recipientExists {
		return Channel{}, false, ErrNotFound
	}
	first, second := userID.String(), recipientID.String()
	if second < first {
		first, second = second, first
	}
	key := first + ":" + second
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Channel{}, false, fmt.Errorf("begin direct channel: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
		INSERT INTO channels (id, guild_id, type, name, position, dm_key, created_at, updated_at)
		VALUES ($1, NULL, 'DIRECT', NULL, 0, $2, $3, $3)
		ON CONFLICT (dm_key) DO NOTHING`, channel.ID, key, channel.CreatedAt)
	if err != nil {
		return Channel{}, false, fmt.Errorf("insert direct channel: %w", err)
	}
	created := tag.RowsAffected() == 1
	if created {
		if _, err := tx.Exec(ctx, `INSERT INTO dm_participants (channel_id, user_id) VALUES ($1, $2), ($1, $3)`, channel.ID, userID, recipientID); err != nil {
			return Channel{}, false, fmt.Errorf("insert direct participants: %w", err)
		}
	}
	var result Channel
	if err := scanChannel(tx.QueryRow(ctx, `SELECT `+channelColumns+` FROM channels c WHERE c.dm_key = $1`, key), &result); err != nil {
		return Channel{}, false, fmt.Errorf("get direct channel: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Channel{}, false, fmt.Errorf("commit direct channel: %w", err)
	}
	if err := s.populateRecipients(ctx, &result); err != nil {
		return Channel{}, false, err
	}
	return result, created, nil
}

func (s *PostgresStore) ListDirectChannels(ctx context.Context, userID uuid.UUID, cursor *pageCursor, limit int) ([]channelListRow, error) {
	var cursorTime *time.Time
	cursorID := uuid.Nil
	if cursor != nil {
		cursorTime, cursorID = &cursor.Time, cursor.ID
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+channelColumns+`, c.updated_at
		FROM dm_participants dp
		JOIN channels c ON c.id = dp.channel_id
		WHERE dp.user_id = $1
		  AND ($2::timestamptz IS NULL OR (c.updated_at, c.id) < ($2, $3))
		ORDER BY c.updated_at DESC, c.id DESC
		LIMIT $4`, userID, cursorTime, cursorID, limit)
	if err != nil {
		return nil, fmt.Errorf("list direct channels: %w", err)
	}
	items, err := collectChannelRows(rows)
	if err != nil {
		return nil, err
	}
	pointers := make([]*Channel, len(items))
	for index := range items {
		pointers[index] = &items[index].Channel
	}
	if err := s.populateRecipients(ctx, pointers...); err != nil {
		return nil, err
	}
	return items, nil
}
