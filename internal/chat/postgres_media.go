package chat

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) CreateMessageWithAttachments(ctx context.Context, message Message, attachmentIDs []uuid.UUID) (Message, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Message{}, fmt.Errorf("begin message with attachments: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	err = scanMessage(tx.QueryRow(ctx, `
		WITH inserted AS (
			INSERT INTO messages (id,channel_id,author_id,content,reply_to_message_id,created_at)
			SELECT $1,c.id,$3,$4,$5,$6 FROM channels c
			LEFT JOIN guild_members gm ON gm.guild_id=c.guild_id AND gm.user_id=$3
			LEFT JOIN direct_channel_members dm ON dm.channel_id=c.id AND dm.user_id=$3
			LEFT JOIN messages reply ON reply.id=$5 AND reply.channel_id=c.id
			WHERE c.id=$2 AND c.type IN ('TEXT','THREAD','DIRECT')
			  AND (gm.user_id IS NOT NULL OR dm.user_id IS NOT NULL)
			  AND ($5::uuid IS NULL OR reply.id IS NOT NULL)
			RETURNING id,channel_id,author_id,content,reply_to_message_id,created_at,edited_at
		)
		SELECT i.id,i.channel_id,u.id,u.display_name,u.avatar_url,i.content,i.reply_to_message_id,i.created_at,i.edited_at,
		       reply.id,reply.channel_id,reply_author.id,reply_author.display_name,reply_author.avatar_url,reply.content,reply.created_at,reply.edited_at
		FROM inserted i JOIN users u ON u.id=i.author_id
		LEFT JOIN messages reply ON reply.id=i.reply_to_message_id AND reply.channel_id=i.channel_id
		LEFT JOIN users reply_author ON reply_author.id=reply.author_id`, message.ID, message.ChannelID, message.Author.ID, message.Content, message.ReplyToMessageID, message.CreatedAt), &message)
	if errors.Is(err, pgx.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	if err != nil {
		return Message{}, fmt.Errorf("create message with attachments: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO message_attachments(message_id,attachment_id,position)
		SELECT $1,a.id,(requested.ordinality-1)::smallint
		FROM unnest($2::uuid[]) WITH ORDINALITY requested(id,ordinality)
		JOIN attachments a ON a.id=requested.id AND a.channel_id=$3 AND a.uploader_id=$4 AND a.status='READY'
		WHERE NOT EXISTS(SELECT 1 FROM message_attachments existing WHERE existing.attachment_id=a.id)`, message.ID, attachmentIDs, message.ChannelID, message.Author.ID)
	if err != nil {
		return Message{}, fmt.Errorf("attach files to message: %w", err)
	}
	if tag.RowsAffected() != int64(len(attachmentIDs)) {
		return Message{}, ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return Message{}, fmt.Errorf("commit message with attachments: %w", err)
	}
	if err := s.populateMessageReactions(ctx, message.Author.ID, &message); err != nil {
		return Message{}, err
	}
	if err := s.populateMessageAttachments(ctx, &message); err != nil {
		return Message{}, err
	}
	return message, nil
}

func (s *PostgresStore) populateMessageAttachments(ctx context.Context, messages ...*Message) error {
	if len(messages) == 0 {
		return nil
	}
	byID := make(map[uuid.UUID]*Message, len(messages))
	ids := make([]uuid.UUID, 0, len(messages))
	for _, message := range messages {
		message.Attachments = []Attachment{}
		byID[message.ID] = message
		ids = append(ids, message.ID)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT ma.message_id,a.id,a.uploader_id,a.channel_id,a.filename,a.content_type,a.size,a.checksum_sha256,a.status,a.created_at
		FROM message_attachments ma JOIN attachments a ON a.id=ma.attachment_id
		WHERE ma.message_id=ANY($1) ORDER BY ma.message_id,ma.position`, ids)
	if err != nil {
		return fmt.Errorf("list message attachments: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var messageID uuid.UUID
		var item Attachment
		if err := rows.Scan(&messageID, &item.ID, &item.UploaderID, &item.ChannelID, &item.Filename, &item.ContentType, &item.Size, &item.ChecksumSHA256, &item.Status, &item.CreatedAt); err != nil {
			return err
		}
		if message := byID[messageID]; message != nil {
			message.Attachments = append(message.Attachments, item)
		}
	}
	return rows.Err()
}
