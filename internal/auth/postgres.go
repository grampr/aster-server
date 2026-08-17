package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

func (s *PostgresStore) CreatePasswordUser(ctx context.Context, input NewPasswordUser) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin registration transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx, `
		INSERT INTO users (id, email, normalized_email, display_name, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $5)`,
		input.UserID, input.Email, input.NormalizedEmail, input.DisplayName, input.Session.CreatedAt,
	)
	if isEmailUniqueViolation(err) {
		return ErrEmailAlreadyRegistered
	}
	if err != nil {
		return fmt.Errorf("insert user: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO auth_identities (id, user_id, provider, provider_subject, password_hash, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		input.IdentityID, input.UserID, ProviderPassword, input.NormalizedEmail, input.PasswordHash, input.Session.CreatedAt,
	)
	if isEmailUniqueViolation(err) {
		return ErrEmailAlreadyRegistered
	}
	if err != nil {
		return fmt.Errorf("insert password identity: %w", err)
	}
	if err := insertSession(ctx, tx, input.Session); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit registration transaction: %w", err)
	}
	return nil
}

func (s *PostgresStore) FindPasswordIdentity(ctx context.Context, normalizedEmail string) (PasswordIdentity, error) {
	var identity PasswordIdentity
	err := s.pool.QueryRow(ctx, `
		SELECT u.id, u.email, u.email_verified, u.display_name, u.avatar_url, u.created_at,
		       ai.password_hash,
		       ARRAY(SELECT linked.provider FROM auth_identities linked WHERE linked.user_id = u.id ORDER BY linked.provider)
		FROM auth_identities ai
		JOIN users u ON u.id = ai.user_id
		WHERE ai.provider = $1 AND ai.provider_subject = $2`,
		ProviderPassword, normalizedEmail,
	).Scan(
		&identity.User.ID, &identity.User.Email, &identity.User.EmailVerified,
		&identity.User.DisplayName, &identity.User.AvatarURL, &identity.User.CreatedAt,
		&identity.PasswordHash, &identity.User.AuthenticationMethods,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return PasswordIdentity{}, errIdentityNotFound
	}
	if err != nil {
		return PasswordIdentity{}, fmt.Errorf("find password identity: %w", err)
	}
	return identity, nil
}

func (s *PostgresStore) CreateSession(ctx context.Context, session NewSession) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin session transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := insertSession(ctx, tx, session); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit session transaction: %w", err)
	}
	return nil
}

func (s *PostgresStore) RotateSession(ctx context.Context, currentRefreshHash []byte, next RotatedSession) (uuid.UUID, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, fmt.Errorf("begin token rotation transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var sessionID uuid.UUID
	var consumedAt, revokedAt *time.Time
	var refreshExpiresAt, sessionExpiresAt time.Time
	err = tx.QueryRow(ctx, `
		SELECT rt.session_id, rt.consumed_at, rt.expires_at, s.revoked_at, s.expires_at
		FROM session_refresh_tokens rt
		JOIN sessions s ON s.id = rt.session_id
		WHERE rt.token_hash = $1
		FOR UPDATE OF rt, s`, currentRefreshHash,
	).Scan(&sessionID, &consumedAt, &refreshExpiresAt, &revokedAt, &sessionExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrInvalidRefreshToken
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("lock refresh token: %w", err)
	}

	if consumedAt != nil || revokedAt != nil || !next.RotatedAt.Before(refreshExpiresAt) || !next.RotatedAt.Before(sessionExpiresAt) {
		_, revokeErr := tx.Exec(ctx, `UPDATE sessions SET revoked_at = COALESCE(revoked_at, $2) WHERE id = $1`, sessionID, next.RotatedAt)
		if revokeErr != nil {
			return uuid.Nil, fmt.Errorf("revoke replayed session: %w", revokeErr)
		}
		if err := tx.Commit(ctx); err != nil {
			return uuid.Nil, fmt.Errorf("commit session revocation: %w", err)
		}
		return uuid.Nil, ErrInvalidRefreshToken
	}

	if _, err := tx.Exec(ctx, `UPDATE session_refresh_tokens SET consumed_at = $2 WHERE token_hash = $1`, currentRefreshHash, next.RotatedAt); err != nil {
		return uuid.Nil, fmt.Errorf("consume refresh token: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE sessions
		SET access_token_hash = $2, access_expires_at = $3, expires_at = $4, updated_at = $5
		WHERE id = $1`,
		sessionID, next.AccessTokenHash, next.AccessExpiresAt, next.RefreshExpiresAt, next.RotatedAt,
	); err != nil {
		return uuid.Nil, fmt.Errorf("update session tokens: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO session_refresh_tokens (id, session_id, token_hash, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5)`,
		next.RefreshTokenID, sessionID, next.RefreshTokenHash, next.RefreshExpiresAt, next.RotatedAt,
	); err != nil {
		return uuid.Nil, fmt.Errorf("insert rotated refresh token: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, fmt.Errorf("commit token rotation: %w", err)
	}
	return sessionID, nil
}

func (s *PostgresStore) RevokeSession(ctx context.Context, accessHash, refreshHash []byte, now time.Time) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE sessions s
		SET revoked_at = COALESCE(s.revoked_at, $3), updated_at = $3
		WHERE s.access_token_hash = $1
		  AND s.revoked_at IS NULL
		  AND EXISTS (
		      SELECT 1 FROM session_refresh_tokens rt
		      WHERE rt.session_id = s.id AND rt.token_hash = $2 AND rt.consumed_at IS NULL
		  )`, accessHash, refreshHash, now,
	)
	if err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrUnauthorized
	}
	return nil
}

func (s *PostgresStore) FindUserByAccessToken(ctx context.Context, accessHash []byte, now time.Time) (User, error) {
	var user User
	err := s.pool.QueryRow(ctx, `
		SELECT u.id, u.email, u.email_verified, u.display_name, u.avatar_url, u.created_at,
		       ARRAY(SELECT ai.provider FROM auth_identities ai WHERE ai.user_id = u.id ORDER BY ai.provider)
		FROM sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.access_token_hash = $1
		  AND s.revoked_at IS NULL
		  AND s.access_expires_at > $2
		  AND s.expires_at > $2`, accessHash, now,
	).Scan(
		&user.ID, &user.Email, &user.EmailVerified, &user.DisplayName,
		&user.AvatarURL, &user.CreatedAt, &user.AuthenticationMethods,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrUnauthorized
	}
	if err != nil {
		return User{}, fmt.Errorf("find user by access token: %w", err)
	}
	return user, nil
}

func insertSession(ctx context.Context, tx pgx.Tx, session NewSession) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO sessions (
			id, user_id, token_family_id, access_token_hash, access_expires_at,
			expires_at, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $7)`,
		session.ID, session.UserID, session.TokenFamilyID, session.AccessTokenHash,
		session.AccessExpiresAt, session.RefreshExpiresAt, session.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO session_refresh_tokens (id, session_id, token_hash, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5)`,
		session.RefreshTokenID, session.ID, session.RefreshTokenHash,
		session.RefreshExpiresAt, session.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert refresh token: %w", err)
	}
	return nil
}

func isEmailUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	return pgErr.ConstraintName == "users_normalized_email_key" ||
		pgErr.ConstraintName == "auth_identities_provider_provider_subject_key"
}
