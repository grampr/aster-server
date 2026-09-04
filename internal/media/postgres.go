package media

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresStore struct{ pool *pgxpool.Pool }

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

func (s *PostgresStore) Create(ctx context.Context, attachment Attachment, pendingLimit int, byteLimit int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin attachment creation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, attachment.UploaderID.String()); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM attachments WHERE uploader_id=$1 AND status='PENDING' AND created_at<$2`, attachment.UploaderID, attachment.CreatedAt.Add(-24*time.Hour)); err != nil {
		return fmt.Errorf("expire pending attachments: %w", err)
	}
	var pending int
	var stored int64
	if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status='PENDING'), COALESCE(sum(size) FILTER (WHERE status='READY'),0) FROM attachments WHERE uploader_id=$1`, attachment.UploaderID).Scan(&pending, &stored); err != nil {
		return err
	}
	if pending >= pendingLimit || stored+attachment.Size > byteLimit {
		return ErrQuota
	}
	_, err = tx.Exec(ctx, `INSERT INTO attachments(id,uploader_id,channel_id,object_key,filename,content_type,size,checksum_sha256,status,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, attachment.ID, attachment.UploaderID, attachment.ChannelID, attachment.ObjectKey, attachment.Filename, attachment.ContentType, attachment.Size, attachment.ChecksumSHA256, attachment.Status, attachment.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert attachment: %w", err)
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) ExpirePending(ctx context.Context, userID uuid.UUID, before time.Time) ([]string, error) {
	rows, err := s.pool.Query(ctx, `DELETE FROM attachments WHERE uploader_id=$1 AND status='PENDING' AND created_at<$2 RETURNING object_key`, userID, before)
	if err != nil {
		return nil, fmt.Errorf("expire pending attachments: %w", err)
	}
	defer rows.Close()
	keys := []string{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (s *PostgresStore) Get(ctx context.Context, id uuid.UUID) (Attachment, error) {
	var item Attachment
	err := s.pool.QueryRow(ctx, `SELECT id,uploader_id,channel_id,object_key,filename,content_type,size,checksum_sha256,status,created_at,finalized_at FROM attachments WHERE id=$1`, id).Scan(&item.ID, &item.UploaderID, &item.ChannelID, &item.ObjectKey, &item.Filename, &item.ContentType, &item.Size, &item.ChecksumSHA256, &item.Status, &item.CreatedAt, &item.FinalizedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Attachment{}, ErrNotFound
	}
	if err != nil {
		return Attachment{}, fmt.Errorf("get attachment: %w", err)
	}
	return item, nil
}

func (s *PostgresStore) Finalize(ctx context.Context, userID, id uuid.UUID, finalizedAt time.Time) (Attachment, error) {
	var item Attachment
	err := s.pool.QueryRow(ctx, `UPDATE attachments SET status='READY',finalized_at=$3 WHERE id=$1 AND uploader_id=$2 RETURNING id,uploader_id,channel_id,object_key,filename,content_type,size,checksum_sha256,status,created_at,finalized_at`, id, userID, finalizedAt).Scan(&item.ID, &item.UploaderID, &item.ChannelID, &item.ObjectKey, &item.Filename, &item.ContentType, &item.Size, &item.ChecksumSHA256, &item.Status, &item.CreatedAt, &item.FinalizedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Attachment{}, ErrNotFound
	}
	if err != nil {
		return Attachment{}, fmt.Errorf("finalize attachment: %w", err)
	}
	return item, nil
}

func (s *PostgresStore) Delete(ctx context.Context, userID, id uuid.UUID) (string, error) {
	var key string
	err := s.pool.QueryRow(ctx, `DELETE FROM attachments a WHERE a.id=$1 AND a.uploader_id=$2 AND NOT EXISTS(SELECT 1 FROM message_attachments ma WHERE ma.attachment_id=a.id) RETURNING object_key`, id, userID).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		var owner uuid.UUID
		if findErr := s.pool.QueryRow(ctx, `SELECT uploader_id FROM attachments WHERE id=$1`, id).Scan(&owner); errors.Is(findErr, pgx.ErrNoRows) {
			return "", ErrNotFound
		} else if findErr != nil {
			return "", findErr
		}
		if owner != userID {
			return "", ErrNotFound
		}
		return "", ErrForbidden
	}
	if err != nil {
		return "", fmt.Errorf("delete attachment: %w", err)
	}
	return key, nil
}
