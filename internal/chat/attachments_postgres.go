package chat

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const attachmentColumns = `a.id, a.channel_id, a.uploader_id, a.message_id, a.filename, a.content_type, a.size, a.checksum_sha256, a.status, a.object_key, a.created_at`

func scanAttachment(row rowScanner, attachment *Attachment) error {
	return row.Scan(
		&attachment.ID, &attachment.ChannelID, &attachment.UploaderID, &attachment.MessageID, &attachment.Filename,
		&attachment.ContentType, &attachment.Size, &attachment.ChecksumSHA256, &attachment.Status, &attachment.ObjectKey, &attachment.CreatedAt,
	)
}

// CreateAttachment records a pending upload unless the uploader already has maxUnused
// attachments that are not on a Message.
func (s *PostgresStore) CreateAttachment(ctx context.Context, attachment Attachment, maxUnused int) (Attachment, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Attachment{}, fmt.Errorf("begin attachment creation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Serialize concurrent intents of one user so the quota cannot be raced.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, attachment.UploaderID); err != nil {
		return Attachment{}, fmt.Errorf("lock uploader: %w", err)
	}
	var unused int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM attachments WHERE uploader_id = $1 AND message_id IS NULL`, attachment.UploaderID).Scan(&unused); err != nil {
		return Attachment{}, fmt.Errorf("count unused attachments: %w", err)
	}
	if unused >= maxUnused {
		return Attachment{}, ErrUploadQuota
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO attachments (id, channel_id, uploader_id, filename, content_type, size, checksum_sha256, status, object_key, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'PENDING', $8, $9)`,
		attachment.ID, attachment.ChannelID, attachment.UploaderID, attachment.Filename, attachment.ContentType,
		attachment.Size, attachment.ChecksumSHA256, attachment.ObjectKey, attachment.CreatedAt); err != nil {
		return Attachment{}, fmt.Errorf("insert attachment: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Attachment{}, fmt.Errorf("commit attachment creation: %w", err)
	}
	attachment.Status = AttachmentPending
	return attachment, nil
}

// GetAttachment returns an Attachment the caller can see: READY ones in a readable
// Channel, and the caller's own pending uploads.
func (s *PostgresStore) GetAttachment(ctx context.Context, userID, attachmentID uuid.UUID) (Attachment, error) {
	var attachment Attachment
	err := scanAttachment(s.pool.QueryRow(ctx, `
		SELECT `+attachmentColumns+`
		FROM attachments a
		JOIN channel_participants cp ON cp.channel_id = a.channel_id AND cp.user_id = $2
		WHERE a.id = $1 AND (a.status = 'READY' OR a.uploader_id = $2)`, attachmentID, userID), &attachment)
	if errors.Is(err, pgx.ErrNoRows) {
		return Attachment{}, ErrNotFound
	}
	if err != nil {
		return Attachment{}, fmt.Errorf("get attachment: %w", err)
	}
	return attachment, nil
}

func (s *PostgresStore) MarkAttachmentReady(ctx context.Context, attachmentID uuid.UUID) (Attachment, error) {
	var attachment Attachment
	err := scanAttachment(s.pool.QueryRow(ctx, `
		UPDATE attachments a SET status = 'READY' WHERE a.id = $1
		RETURNING `+attachmentColumns, attachmentID), &attachment)
	if errors.Is(err, pgx.ErrNoRows) {
		return Attachment{}, ErrNotFound
	}
	if err != nil {
		return Attachment{}, fmt.Errorf("mark attachment ready: %w", err)
	}
	return attachment, nil
}

func (s *PostgresStore) DeleteAttachment(ctx context.Context, userID, attachmentID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM attachments WHERE id = $1 AND uploader_id = $2 AND message_id IS NULL`, attachmentID, userID)
	if err != nil {
		return fmt.Errorf("delete attachment: %w", err)
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM attachments WHERE id = $1 AND uploader_id = $2)`, attachmentID, userID).Scan(&exists); err != nil {
			return fmt.Errorf("check attachment: %w", err)
		}
		if exists {
			return ErrAttachmentInUse
		}
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) populateMessageAttachments(ctx context.Context, messages ...*Message) error {
	byID := make(map[uuid.UUID]*Message, len(messages))
	ids := make([]uuid.UUID, 0, len(messages))
	for _, message := range messages {
		message.Attachments = []Attachment{}
		byID[message.ID] = message
		ids = append(ids, message.ID)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+attachmentColumns+`
		FROM attachments a
		WHERE a.message_id = ANY($1)
		ORDER BY a.created_at, a.id`, ids)
	if err != nil {
		return fmt.Errorf("list message attachments: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var attachment Attachment
		if err := scanAttachment(rows, &attachment); err != nil {
			return fmt.Errorf("scan message attachment: %w", err)
		}
		if message := byID[*attachment.MessageID]; message != nil {
			message.Attachments = append(message.Attachments, attachment)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate message attachments: %w", err)
	}
	return nil
}

// ExpireAttachments deletes uploads that were never finalized (created before
// pendingBefore) and finalized ones never put on a Message (created before unusedBefore).
func (s *PostgresStore) ExpireAttachments(ctx context.Context, pendingBefore, unusedBefore time.Time) (int, error) {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM attachments
		WHERE message_id IS NULL
		  AND ((status = 'PENDING' AND created_at < $1) OR (status = 'READY' AND created_at < $2))`, pendingBefore, unusedBefore)
	if err != nil {
		return 0, fmt.Errorf("expire attachments: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (s *PostgresStore) PendingStorageDeletions(ctx context.Context, limit int) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT object_key FROM storage_deletions ORDER BY queued_at, object_key LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list storage deletions: %w", err)
	}
	defer rows.Close()
	keys := make([]string, 0, limit)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("scan storage deletion: %w", err)
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (s *PostgresStore) CompleteStorageDeletion(ctx context.Context, key string) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM storage_deletions WHERE object_key = $1`, key); err != nil {
		return fmt.Errorf("complete storage deletion: %w", err)
	}
	return nil
}
