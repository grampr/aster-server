package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) CreateOAuthLogin(ctx context.Context, login OAuthLogin) error {
	// Attempts are useless an hour after they expire; clearing them here avoids a
	// separate cleanup job.
	if _, err := s.pool.Exec(ctx, `DELETE FROM oauth_logins WHERE expires_at < $1`, login.CreatedAt.Add(-time.Hour)); err != nil {
		return fmt.Errorf("purge oauth logins: %w", err)
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO oauth_logins (id, state_hash, nonce, code_challenge, client_state, created_at, expires_at, link_user_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		login.ID, login.StateHash, login.Nonce, login.CodeChallenge, login.ClientState, login.CreatedAt, login.ExpiresAt, login.LinkUserID); err != nil {
		return fmt.Errorf("insert oauth login: %w", err)
	}
	return nil
}

// ClaimOAuthState marks the state as used. A state works for exactly one callback.
func (s *PostgresStore) ClaimOAuthState(ctx context.Context, stateHash []byte, now time.Time) (OAuthLogin, error) {
	var login OAuthLogin
	err := s.pool.QueryRow(ctx, `
		UPDATE oauth_logins SET callback_at = $2
		WHERE state_hash = $1 AND callback_at IS NULL AND expires_at > $2
		RETURNING id, nonce, code_challenge, client_state, created_at, expires_at`, stateHash, now,
	).Scan(&login.ID, &login.Nonce, &login.CodeChallenge, &login.ClientState, &login.CreatedAt, &login.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return OAuthLogin{}, errOAuthNotFound
	}
	if err != nil {
		return OAuthLogin{}, fmt.Errorf("claim oauth state: %w", err)
	}
	return login, nil
}

func (s *PostgresStore) StoreExchangeGrant(ctx context.Context, loginID uuid.UUID, codeHash []byte, expiresAt time.Time, identity GoogleIdentity) error {
	var picture *string
	if identity.Picture != "" {
		picture = &identity.Picture
	}
	if _, err := s.pool.Exec(ctx, `
		UPDATE oauth_logins
		SET exchange_code_hash = $2, exchange_expires_at = $3, google_subject = $4, email = $5,
		    email_verified = $6, display_name = $7, avatar_url = $8
		WHERE id = $1`,
		loginID, codeHash, expiresAt, identity.Subject, identity.Email, identity.EmailVerified, identity.Name, picture); err != nil {
		return fmt.Errorf("store exchange grant: %w", err)
	}
	return nil
}

// ConsumeExchangeCode burns the Exchange Code and returns what it stood for. The code
// is spent even if the caller's PKCE check later fails.
func (s *PostgresStore) ConsumeExchangeCode(ctx context.Context, codeHash []byte, now time.Time) (ExchangeGrant, error) {
	var grant ExchangeGrant
	var picture *string
	err := s.pool.QueryRow(ctx, `
		UPDATE oauth_logins SET exchange_consumed_at = $2
		WHERE exchange_code_hash = $1 AND exchange_consumed_at IS NULL AND exchange_expires_at > $2
		RETURNING link_user_id, code_challenge, google_subject, email, email_verified, display_name, avatar_url`, codeHash, now,
	).Scan(&grant.LinkUserID, &grant.CodeChallenge, &grant.Identity.Subject, &grant.Identity.Email, &grant.Identity.EmailVerified, &grant.Identity.Name, &picture)
	if errors.Is(err, pgx.ErrNoRows) {
		return ExchangeGrant{}, errOAuthNotFound
	}
	if err != nil {
		return ExchangeGrant{}, fmt.Errorf("consume exchange code: %w", err)
	}
	if picture != nil {
		grant.Identity.Picture = *picture
	}
	return grant, nil
}

// SignInGoogle opens a Session for the Google Identity, creating the User on first use.
// It never links to an existing Account by email: that returns ErrAccountLinkRequired.
func (s *PostgresStore) SignInGoogle(ctx context.Context, input NewGoogleSignIn) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin google sign-in: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var userID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT user_id FROM auth_identities WHERE provider = $1 AND provider_subject = $2`,
		ProviderGoogle, input.Identity.Subject).Scan(&userID)
	switch {
	case err == nil:
		input.Session.UserID = userID
	case errors.Is(err, pgx.ErrNoRows):
		normalized := normalizeEmail(input.Identity.Email)
		var taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE normalized_email = $1)`, normalized).Scan(&taken); err != nil {
			return fmt.Errorf("check existing account: %w", err)
		}
		if taken {
			return ErrAccountLinkRequired
		}
		var picture *string
		if input.Identity.Picture != "" {
			picture = &input.Identity.Picture
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO users (id, email, normalized_email, email_verified, display_name, avatar_url, created_at, updated_at)
			VALUES ($1, $2, $3, TRUE, $4, $5, $6, $6)`,
			input.UserID, input.Identity.Email, normalized, input.DisplayName, picture, input.Session.CreatedAt); err != nil {
			if isEmailUniqueViolation(err) {
				return ErrAccountLinkRequired
			}
			return fmt.Errorf("insert google user: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO auth_identities (id, user_id, provider, provider_subject, created_at)
			VALUES ($1, $2, $3, $4, $5)`,
			input.IdentityID, input.UserID, ProviderGoogle, input.Identity.Subject, input.Session.CreatedAt); err != nil {
			return fmt.Errorf("insert google identity: %w", err)
		}
	default:
		return fmt.Errorf("find google identity: %w", err)
	}
	if err := insertSession(ctx, tx, input.Session); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit google sign-in: %w", err)
	}
	return nil
}
