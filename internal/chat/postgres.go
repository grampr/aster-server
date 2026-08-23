package chat

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

func (s *PostgresStore) CreateGuild(ctx context.Context, ownerID uuid.UUID, guild Guild) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin guild creation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		INSERT INTO guilds (id, owner_id, name, description, icon_url, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $6)`,
		guild.ID, ownerID, guild.Name, guild.Description, guild.IconURL, guild.CreatedAt,
	); err != nil {
		return fmt.Errorf("insert guild: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO guild_members (guild_id, user_id, joined_at) VALUES ($1, $2, $3)`,
		guild.ID, ownerID, guild.CreatedAt,
	); err != nil {
		return fmt.Errorf("insert guild owner membership: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit guild creation: %w", err)
	}
	return nil
}

func (s *PostgresStore) ListGuilds(ctx context.Context, userID uuid.UUID, cursor *pageCursor, limit int) ([]guildListRow, error) {
	var cursorTime *time.Time
	cursorID := uuid.Nil
	if cursor != nil {
		cursorTime, cursorID = &cursor.Time, cursor.ID
	}
	rows, err := s.pool.Query(ctx, `
		SELECT g.id, g.owner_id, g.name, g.description, g.icon_url, g.created_at, gm.joined_at
		FROM guild_members gm
		JOIN guilds g ON g.id = gm.guild_id
		WHERE gm.user_id = $1
		  AND ($2::timestamptz IS NULL OR (gm.joined_at, g.id) < ($2, $3))
		ORDER BY gm.joined_at DESC, g.id DESC
		LIMIT $4`, userID, cursorTime, cursorID, limit)
	if err != nil {
		return nil, fmt.Errorf("list guilds: %w", err)
	}
	defer rows.Close()
	items := make([]guildListRow, 0, limit)
	for rows.Next() {
		var item guildListRow
		if err := rows.Scan(&item.ID, &item.OwnerID, &item.Name, &item.Description, &item.IconURL, &item.CreatedAt, &item.JoinedAt); err != nil {
			return nil, fmt.Errorf("scan guild: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate guilds: %w", err)
	}
	return items, nil
}

func (s *PostgresStore) GetGuild(ctx context.Context, userID, guildID uuid.UUID) (Guild, error) {
	var guild Guild
	err := s.pool.QueryRow(ctx, `
		SELECT g.id, g.owner_id, g.name, g.description, g.icon_url, g.created_at
		FROM guilds g
		JOIN guild_members gm ON gm.guild_id = g.id
		WHERE g.id = $1 AND gm.user_id = $2`, guildID, userID,
	).Scan(&guild.ID, &guild.OwnerID, &guild.Name, &guild.Description, &guild.IconURL, &guild.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Guild{}, ErrNotFound
	}
	if err != nil {
		return Guild{}, fmt.Errorf("get guild: %w", err)
	}
	return guild, nil
}

func (s *PostgresStore) UpdateGuild(ctx context.Context, ownerID, guildID uuid.UUID, input UpdateGuildInput, updatedAt time.Time) (Guild, error) {
	var guild Guild
	err := s.pool.QueryRow(ctx, `
		UPDATE guilds
		SET name = COALESCE($3, name),
		    description = CASE WHEN $4 THEN $5 ELSE description END,
		    updated_at = $6
		WHERE id = $1 AND owner_id = $2
		RETURNING id, owner_id, name, description, icon_url, created_at`,
		guildID, ownerID, input.Name, input.Description.Set, input.Description.Value, updatedAt,
	).Scan(&guild.ID, &guild.OwnerID, &guild.Name, &guild.Description, &guild.IconURL, &guild.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Guild{}, ErrNotFound
	}
	if err != nil {
		return Guild{}, fmt.Errorf("update guild: %w", err)
	}
	return guild, nil
}

func (s *PostgresStore) DeleteGuild(ctx context.Context, ownerID, guildID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM guilds WHERE id = $1 AND owner_id = $2`, guildID, ownerID)
	if err != nil {
		return fmt.Errorf("delete guild: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
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
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(position), -1) + 1 FROM channels WHERE guild_id = $1`, channel.GuildID).Scan(&channel.Position); err != nil {
		return Channel{}, fmt.Errorf("select channel position: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO channels (id, guild_id, type, name, topic, position, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $7)`,
		channel.ID, channel.GuildID, channel.Type, channel.Name, channel.Topic, channel.Position, channel.CreatedAt,
	); err != nil {
		return Channel{}, fmt.Errorf("insert channel: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Channel{}, fmt.Errorf("commit channel creation: %w", err)
	}
	return channel, nil
}

func (s *PostgresStore) ListChannels(ctx context.Context, userID, guildID uuid.UUID, cursor *pageCursor, limit int) ([]Channel, error) {
	var cursorPosition *int
	cursorID := uuid.Nil
	if cursor != nil {
		cursorPosition, cursorID = &cursor.Position, cursor.ID
	}
	rows, err := s.pool.Query(ctx, `
		SELECT c.id, c.guild_id, c.type, c.name, c.topic, c.position, c.created_at
		FROM channels c
		JOIN guild_members gm ON gm.guild_id = c.guild_id
		WHERE c.guild_id = $1 AND gm.user_id = $2
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
	err := s.pool.QueryRow(ctx, `
		SELECT c.id, c.guild_id, c.type, c.name, c.topic, c.position, c.created_at
		FROM channels c
		JOIN guild_members gm ON gm.guild_id = c.guild_id
		WHERE c.id = $1 AND gm.user_id = $2`, channelID, userID,
	).Scan(&channel.ID, &channel.GuildID, &channel.Type, &channel.Name, &channel.Topic, &channel.Position, &channel.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Channel{}, ErrNotFound
	}
	if err != nil {
		return Channel{}, fmt.Errorf("get channel: %w", err)
	}
	return channel, nil
}

func (s *PostgresStore) UpdateChannel(ctx context.Context, ownerID, channelID uuid.UUID, input UpdateChannelInput, updatedAt time.Time) (Channel, error) {
	var channel Channel
	err := s.pool.QueryRow(ctx, `
		UPDATE channels c
		SET name = COALESCE($3, c.name),
		    topic = CASE WHEN $4 THEN $5 ELSE c.topic END,
		    position = COALESCE($6, c.position),
		    updated_at = $7
		FROM guilds g
		WHERE c.id = $1 AND g.id = c.guild_id AND g.owner_id = $2
		RETURNING c.id, c.guild_id, c.type, c.name, c.topic, c.position, c.created_at`,
		channelID, ownerID, input.Name, input.Topic.Set, input.Topic.Value, input.Position, updatedAt,
	).Scan(&channel.ID, &channel.GuildID, &channel.Type, &channel.Name, &channel.Topic, &channel.Position, &channel.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Channel{}, ErrNotFound
	}
	if err != nil {
		return Channel{}, fmt.Errorf("update channel: %w", err)
	}
	return channel, nil
}

func (s *PostgresStore) DeleteChannel(ctx context.Context, ownerID, channelID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM channels c USING guilds g
		WHERE c.id = $1 AND g.id = c.guild_id AND g.owner_id = $2`, channelID, ownerID)
	if err != nil {
		return fmt.Errorf("delete channel: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) CreateMessage(ctx context.Context, message Message) (Message, error) {
	err := scanMessage(s.pool.QueryRow(ctx, `
		WITH inserted AS (
			INSERT INTO messages (id, channel_id, author_id, content, reply_to_message_id, created_at)
			SELECT $1, c.id, $3, $4, $5, $6
			FROM channels c
			JOIN guild_members gm ON gm.guild_id = c.guild_id
			LEFT JOIN messages reply ON reply.id = $5 AND reply.channel_id = c.id
			WHERE c.id = $2 AND c.type = 'TEXT' AND gm.user_id = $3
			  AND ($5::uuid IS NULL OR reply.id IS NOT NULL)
			RETURNING id, channel_id, author_id, content, reply_to_message_id, created_at, edited_at
		)
		SELECT i.id, i.channel_id, u.id, u.display_name, u.avatar_url, i.content,
		       i.reply_to_message_id, i.created_at, i.edited_at,
		       reply.id, reply.channel_id, reply_author.id, reply_author.display_name,
		       reply_author.avatar_url, reply.content, reply.created_at, reply.edited_at
		FROM inserted i
		JOIN users u ON u.id = i.author_id
		LEFT JOIN messages reply ON reply.id = i.reply_to_message_id AND reply.channel_id = i.channel_id
		LEFT JOIN users reply_author ON reply_author.id = reply.author_id`,
		message.ID, message.ChannelID, message.Author.ID, message.Content, message.ReplyToMessageID, message.CreatedAt,
	), &message)
	if errors.Is(err, pgx.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	if err != nil {
		return Message{}, fmt.Errorf("create message: %w", err)
	}
	return message, nil
}

func (s *PostgresStore) ListMessages(ctx context.Context, userID, channelID uuid.UUID, cursor *pageCursor, limit int) ([]Message, error) {
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
		JOIN channels c ON c.id = m.channel_id AND c.type = 'TEXT'
		JOIN guild_members gm ON gm.guild_id = c.guild_id AND gm.user_id = $2
		JOIN users u ON u.id = m.author_id
		LEFT JOIN messages reply ON reply.id = m.reply_to_message_id AND reply.channel_id = m.channel_id
		LEFT JOIN users reply_author ON reply_author.id = reply.author_id
		WHERE m.channel_id = $1
		  AND ($3::timestamptz IS NULL OR (m.created_at, m.id) < ($3, $4))
		ORDER BY m.created_at DESC, m.id DESC
		LIMIT $5`, channelID, userID, cursorTime, cursorID, limit)
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
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
		return nil, fmt.Errorf("iterate messages: %w", err)
	}
	if len(items) == 0 {
		var exists bool
		if err := s.pool.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM channels c
				JOIN guild_members gm ON gm.guild_id = c.guild_id
				WHERE c.id = $1 AND c.type = 'TEXT' AND gm.user_id = $2
			)`, channelID, userID).Scan(&exists); err != nil {
			return nil, fmt.Errorf("check text channel access: %w", err)
		}
		if !exists {
			return nil, ErrNotFound
		}
	}
	return items, nil
}

func (s *PostgresStore) GetMessage(ctx context.Context, userID, channelID, messageID uuid.UUID) (Message, error) {
	var message Message
	err := scanMessage(s.pool.QueryRow(ctx, `
		SELECT m.id, m.channel_id, u.id, u.display_name, u.avatar_url, m.content,
		       m.reply_to_message_id, m.created_at, m.edited_at,
		       reply.id, reply.channel_id, reply_author.id, reply_author.display_name,
		       reply_author.avatar_url, reply.content, reply.created_at, reply.edited_at
		FROM messages m
		JOIN channels c ON c.id = m.channel_id AND c.type = 'TEXT'
		JOIN guild_members gm ON gm.guild_id = c.guild_id
		JOIN users u ON u.id = m.author_id
		LEFT JOIN messages reply ON reply.id = m.reply_to_message_id AND reply.channel_id = m.channel_id
		LEFT JOIN users reply_author ON reply_author.id = reply.author_id
		WHERE m.id = $1 AND m.channel_id = $2 AND gm.user_id = $3`, messageID, channelID, userID,
	), &message)
	if errors.Is(err, pgx.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	if err != nil {
		return Message{}, fmt.Errorf("get message: %w", err)
	}
	return message, nil
}

func (s *PostgresStore) UpdateMessage(ctx context.Context, authorID, channelID, messageID uuid.UUID, content string, editedAt time.Time) (Message, error) {
	var message Message
	err := scanMessage(s.pool.QueryRow(ctx, `
		WITH updated AS (
			UPDATE messages
			SET content = $4, edited_at = $5
			WHERE id = $1 AND channel_id = $2 AND author_id = $3
			RETURNING id, channel_id, author_id, content, reply_to_message_id, created_at, edited_at
		)
		SELECT m.id, m.channel_id, u.id, u.display_name, u.avatar_url, m.content,
		       m.reply_to_message_id, m.created_at, m.edited_at,
		       reply.id, reply.channel_id, reply_author.id, reply_author.display_name,
		       reply_author.avatar_url, reply.content, reply.created_at, reply.edited_at
		FROM updated m
		JOIN users u ON u.id = m.author_id
		LEFT JOIN messages reply ON reply.id = m.reply_to_message_id AND reply.channel_id = m.channel_id
		LEFT JOIN users reply_author ON reply_author.id = reply.author_id`,
		messageID, channelID, authorID, content, editedAt,
	), &message)
	if errors.Is(err, pgx.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	if err != nil {
		return Message{}, fmt.Errorf("update message: %w", err)
	}
	return message, nil
}

func (s *PostgresStore) DeleteMessage(ctx context.Context, userID, channelID, messageID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM messages m
		USING channels c, guilds g, guild_members gm
		WHERE m.id = $1 AND m.channel_id = $2
		  AND c.id = m.channel_id AND g.id = c.guild_id
		  AND gm.guild_id = g.id AND gm.user_id = $3
		  AND (m.author_id = $3 OR g.owner_id = $3)`, messageID, channelID, userID)
	if err != nil {
		return fmt.Errorf("delete message: %w", err)
	}
	if tag.RowsAffected() == 0 {
		message, findErr := s.GetMessage(ctx, userID, channelID, messageID)
		if findErr != nil {
			return findErr
		}
		if message.Author.ID != userID {
			return ErrForbidden
		}
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) ListChannelMemberIDs(ctx context.Context, channelID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT gm.user_id
		FROM channels c
		JOIN guild_members gm ON gm.guild_id = c.guild_id
		WHERE c.id = $1 AND c.type = 'TEXT'
		ORDER BY gm.user_id`, channelID)
	if err != nil {
		return nil, fmt.Errorf("list channel member IDs: %w", err)
	}
	defer rows.Close()
	memberIDs := make([]uuid.UUID, 0)
	for rows.Next() {
		var memberID uuid.UUID
		if err := rows.Scan(&memberID); err != nil {
			return nil, fmt.Errorf("scan channel member ID: %w", err)
		}
		memberIDs = append(memberIDs, memberID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate channel member IDs: %w", err)
	}
	return memberIDs, nil
}

type rowScanner interface {
	Scan(...any) error
}

func scanChannel(row rowScanner, channel *Channel) error {
	return row.Scan(&channel.ID, &channel.GuildID, &channel.Type, &channel.Name, &channel.Topic, &channel.Position, &channel.CreatedAt)
}

func scanMessage(row rowScanner, message *Message) error {
	var replyID, replyChannelID, replyAuthorID *uuid.UUID
	var replyDisplayName, replyAvatarURL, replyContent *string
	var replyCreatedAt, replyEditedAt *time.Time
	err := row.Scan(
		&message.ID, &message.ChannelID, &message.Author.ID, &message.Author.DisplayName,
		&message.Author.AvatarURL, &message.Content, &message.ReplyToMessageID, &message.CreatedAt, &message.EditedAt,
		&replyID, &replyChannelID, &replyAuthorID, &replyDisplayName,
		&replyAvatarURL, &replyContent, &replyCreatedAt, &replyEditedAt,
	)
	if err != nil {
		return err
	}
	if replyID == nil {
		message.ReplyTo = nil
		return nil
	}
	message.ReplyTo = &MessageReply{
		ID: *replyID, ChannelID: *replyChannelID,
		Author:  UserSummary{ID: *replyAuthorID, DisplayName: *replyDisplayName, AvatarURL: replyAvatarURL},
		Content: *replyContent, CreatedAt: *replyCreatedAt, EditedAt: replyEditedAt,
	}
	return nil
}
