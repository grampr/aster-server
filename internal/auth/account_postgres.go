package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) CreateEmailToken(ctx context.Context, token EmailToken, cooldown time.Duration) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin email token creation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Serialize per User so two requests cannot both pass the cooldown.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, token.UserID); err != nil {
		return false, fmt.Errorf("lock user: %w", err)
	}
	var recent bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM email_tokens WHERE user_id = $1 AND purpose = $2 AND created_at > $3)`,
		token.UserID, token.Purpose, token.CreatedAt.Add(-cooldown)).Scan(&recent); err != nil {
		return false, fmt.Errorf("check email token cooldown: %w", err)
	}
	if recent {
		return false, nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE email_tokens SET consumed_at = $3
		WHERE user_id = $1 AND purpose = $2 AND consumed_at IS NULL`, token.UserID, token.Purpose, token.CreatedAt); err != nil {
		return false, fmt.Errorf("invalidate earlier email tokens: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO email_tokens (id, user_id, purpose, email, token_hash, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		token.ID, token.UserID, token.Purpose, token.Email, token.TokenHash, token.CreatedAt, token.ExpiresAt); err != nil {
		return false, fmt.Errorf("insert email token: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit email token: %w", err)
	}
	return true, nil
}

// consumeEmailToken spends a token of the given purpose. It is spent whether or not
// the caller goes on to accept it, and only an unused, unexpired token is returned.
func consumeEmailToken(ctx context.Context, tx pgx.Tx, tokenHash []byte, purpose string, now time.Time) (EmailToken, error) {
	var token EmailToken
	err := tx.QueryRow(ctx, `
		UPDATE email_tokens SET consumed_at = $3
		WHERE token_hash = $1 AND purpose = $2 AND consumed_at IS NULL AND expires_at > $3
		RETURNING id, user_id, purpose, email, created_at, expires_at`, tokenHash, purpose, now,
	).Scan(&token.ID, &token.UserID, &token.Purpose, &token.Email, &token.CreatedAt, &token.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return EmailToken{}, ErrInvalidToken
	}
	if err != nil {
		return EmailToken{}, fmt.Errorf("consume email token: %w", err)
	}
	return token, nil
}

func (s *PostgresStore) VerifyEmail(ctx context.Context, tokenHash []byte, now time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin email verification: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	token, err := consumeEmailToken(ctx, tx, tokenHash, PurposeVerifyEmail, now)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE users SET email_verified = TRUE, updated_at = $3 WHERE id = $1 AND normalized_email = $2`, token.UserID, token.Email, now)
	if err != nil {
		return fmt.Errorf("mark email verified: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// The address changed after the token was sent; commit the spent token anyway.
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit spent token: %w", err)
		}
		return ErrInvalidToken
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit email verification: %w", err)
	}
	return nil
}

// ResetPassword replaces the Password, proves ownership of the address and ends every
// Session of the User, all in one transaction.
func (s *PostgresStore) ResetPassword(ctx context.Context, tokenHash []byte, newPasswordHash string, now time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin password reset: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	token, err := consumeEmailToken(ctx, tx, tokenHash, PurposeResetPassword, now)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE auth_identities SET password_hash = $3
		WHERE user_id = $1 AND provider = $4 AND provider_subject = $2`, token.UserID, token.Email, newPasswordHash, ProviderPassword)
	if err != nil {
		return fmt.Errorf("update password hash: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// The Password identity is gone or the address changed; the spent token stays spent.
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit spent token: %w", err)
		}
		return ErrInvalidToken
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET email_verified = TRUE, updated_at = $2 WHERE id = $1`, token.UserID, now); err != nil {
		return fmt.Errorf("mark email verified: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at = COALESCE(revoked_at, $2), updated_at = $2 WHERE user_id = $1`, token.UserID, now); err != nil {
		return fmt.Errorf("revoke sessions: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE email_tokens SET consumed_at = $3
		WHERE user_id = $1 AND purpose = $2 AND consumed_at IS NULL`, token.UserID, PurposeResetPassword, now); err != nil {
		return fmt.Errorf("invalidate other reset tokens: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit password reset: %w", err)
	}
	return nil
}

// LinkGoogle adds a Google Identity to userID. Google's own verification of the same
// address as the Account's counts as proof of ownership.
func (s *PostgresStore) LinkGoogle(ctx context.Context, input LinkGoogleInput) (User, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return User{}, fmt.Errorf("begin google link: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var normalized string
	if err := tx.QueryRow(ctx, `SELECT normalized_email FROM users WHERE id = $1 FOR UPDATE`, input.UserID).Scan(&normalized); err != nil {
		return User{}, fmt.Errorf("lock user: %w", err)
	}
	var taken bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM auth_identities
		              WHERE provider = $1 AND (provider_subject = $2 OR user_id = $3))`,
		ProviderGoogle, input.Identity.Subject, input.UserID).Scan(&taken); err != nil {
		return User{}, fmt.Errorf("check google identity: %w", err)
	}
	if taken {
		return User{}, ErrIdentityAlreadyLinked
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO auth_identities (id, user_id, provider, provider_subject, created_at)
		VALUES ($1, $2, $3, $4, $5)`, input.IdentityID, input.UserID, ProviderGoogle, input.Identity.Subject, input.Now); err != nil {
		return User{}, fmt.Errorf("insert google identity: %w", err)
	}
	if input.Identity.EmailVerified && normalizeEmail(input.Identity.Email) == normalized {
		if _, err := tx.Exec(ctx, `UPDATE users SET email_verified = TRUE, updated_at = $2 WHERE id = $1`, input.UserID, input.Now); err != nil {
			return User{}, fmt.Errorf("mark email verified: %w", err)
		}
	}
	var user User
	err = tx.QueryRow(ctx, `
		SELECT u.id, u.email, u.email_verified, u.display_name, u.avatar_url, u.created_at,
		       ARRAY(SELECT ai.provider FROM auth_identities ai WHERE ai.user_id = u.id ORDER BY ai.provider)
		FROM users u WHERE u.id = $1`, input.UserID,
	).Scan(&user.ID, &user.Email, &user.EmailVerified, &user.DisplayName, &user.AvatarURL, &user.CreatedAt, &user.AuthenticationMethods)
	if err != nil {
		return User{}, fmt.Errorf("load linked user: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, fmt.Errorf("commit google link: %w", err)
	}
	return user, nil
}

// UnlinkAuthenticationMethod removes one identity, refusing to remove the last one.
func (s *PostgresStore) UnlinkAuthenticationMethod(ctx context.Context, userID uuid.UUID, provider string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin unlink: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, userID); err != nil {
		return fmt.Errorf("lock user: %w", err)
	}
	var providers []string
	if err := tx.QueryRow(ctx, `SELECT ARRAY(SELECT provider FROM auth_identities WHERE user_id = $1)`, userID).Scan(&providers); err != nil {
		return fmt.Errorf("list identities: %w", err)
	}
	linked := false
	for _, existing := range providers {
		linked = linked || existing == provider
	}
	if !linked {
		return ErrAuthMethodNotLinked
	}
	if len(providers) <= 1 {
		return ErrLastAuthenticationMethod
	}
	if _, err := tx.Exec(ctx, `DELETE FROM auth_identities WHERE user_id = $1 AND provider = $2`, userID, provider); err != nil {
		return fmt.Errorf("delete identity: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit unlink: %w", err)
	}
	return nil
}
