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

func (s *PostgresStore) CreateGoogleLoginAttempt(ctx context.Context, input NewGoogleLoginAttempt) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM google_login_attempts WHERE expires_at <= $1`, input.CreatedAt); err != nil {
		return fmt.Errorf("delete expired google login attempts: %w", err)
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO google_login_attempts (
			id, oauth_state_hash, client_state, code_challenge, redirect_uri,
			nonce, provider_code_verifier, expires_at, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		input.ID, input.OAuthStateHash, input.ClientState, input.CodeChallenge, input.RedirectURI,
		input.Nonce, input.ProviderCodeVerifier, input.ExpiresAt, input.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert google login attempt: %w", err)
	}
	return nil
}

func (s *PostgresStore) DeleteGoogleLoginAttempt(ctx context.Context, attemptID uuid.UUID) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM google_login_attempts WHERE id = $1`, attemptID); err != nil {
		return fmt.Errorf("delete google login attempt: %w", err)
	}
	return nil
}

func (s *PostgresStore) ConsumeGoogleLoginAttempt(ctx context.Context, stateHash []byte, now time.Time) (GoogleLoginAttempt, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return GoogleLoginAttempt{}, fmt.Errorf("begin google callback transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var attempt GoogleLoginAttempt
	err = tx.QueryRow(ctx, `
		SELECT id, client_state, code_challenge, redirect_uri, nonce, provider_code_verifier
		FROM google_login_attempts
		WHERE oauth_state_hash = $1 AND consumed_at IS NULL AND expires_at > $2
		FOR UPDATE`, stateHash, now,
	).Scan(
		&attempt.ID, &attempt.ClientState, &attempt.CodeChallenge, &attempt.RedirectURI,
		&attempt.Nonce, &attempt.ProviderCodeVerifier,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return GoogleLoginAttempt{}, ErrInvalidOAuthCallback
	}
	if err != nil {
		return GoogleLoginAttempt{}, fmt.Errorf("lock google login attempt: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE google_login_attempts SET consumed_at = $2 WHERE id = $1`, attempt.ID, now); err != nil {
		return GoogleLoginAttempt{}, fmt.Errorf("consume google login attempt: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return GoogleLoginAttempt{}, fmt.Errorf("commit google callback: %w", err)
	}
	return attempt, nil
}

func (s *PostgresStore) CreateGoogleExchangeGrant(ctx context.Context, input NewGoogleExchangeGrant) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO google_exchange_grants (
			id, attempt_id, code_hash, provider_subject, email, normalized_email,
			email_verified, display_name, avatar_url, expires_at, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		input.ID, input.AttemptID, input.CodeHash, input.Identity.Subject, input.Identity.Email,
		input.Identity.Email, input.Identity.EmailVerified, input.Identity.DisplayName,
		input.Identity.AvatarURL, input.ExpiresAt, input.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert google exchange grant: %w", err)
	}
	return nil
}

func (s *PostgresStore) ExchangeGoogleGrant(ctx context.Context, input GoogleSessionExchange) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin google exchange transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var grantID, attemptID uuid.UUID
	var consumedAt *time.Time
	var expiresAt time.Time
	var storedChallenge, subject, email, normalizedEmail, displayName string
	var emailVerified bool
	var avatarURL *string
	err = tx.QueryRow(ctx, `
		SELECT g.id, g.attempt_id, g.consumed_at, g.expires_at, a.code_challenge,
		       g.provider_subject, g.email, g.normalized_email, g.email_verified,
		       g.display_name, g.avatar_url
		FROM google_exchange_grants g
		JOIN google_login_attempts a ON a.id = g.attempt_id
		WHERE g.code_hash = $1
		FOR UPDATE OF g`, input.CodeHash,
	).Scan(
		&grantID, &attemptID, &consumedAt, &expiresAt, &storedChallenge, &subject, &email,
		&normalizedEmail, &emailVerified, &displayName, &avatarURL,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInvalidAuthorizationGrant
	}
	if err != nil {
		return fmt.Errorf("lock google exchange grant: %w", err)
	}
	if consumedAt != nil {
		return ErrInvalidAuthorizationGrant
	}
	if _, err := tx.Exec(ctx, `UPDATE google_exchange_grants SET consumed_at = $2 WHERE id = $1`, grantID, input.ExchangedAt); err != nil {
		return fmt.Errorf("consume google exchange grant: %w", err)
	}
	if !input.ExchangedAt.Before(expiresAt) || storedChallenge != input.CodeChallenge || !emailVerified {
		if err := deleteGoogleGrantAndAttempt(ctx, tx, grantID, attemptID); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit rejected google exchange: %w", err)
		}
		return ErrInvalidAuthorizationGrant
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "google-subject:"+subject); err != nil {
		return fmt.Errorf("lock google subject: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "google-email:"+normalizedEmail); err != nil {
		return fmt.Errorf("lock google email: %w", err)
	}

	var userID uuid.UUID
	err = tx.QueryRow(ctx, `
		SELECT user_id FROM auth_identities WHERE provider = $1 AND provider_subject = $2`,
		ProviderGoogle, subject,
	).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		var existingUserID uuid.UUID
		emailErr := tx.QueryRow(ctx, `SELECT id FROM users WHERE normalized_email = $1`, normalizedEmail).Scan(&existingUserID)
		if emailErr == nil {
			if err := deleteGoogleGrantAndAttempt(ctx, tx, grantID, attemptID); err != nil {
				return err
			}
			if err := tx.Commit(ctx); err != nil {
				return fmt.Errorf("commit account link requirement: %w", err)
			}
			return ErrAccountLinkRequired
		}
		if !errors.Is(emailErr, pgx.ErrNoRows) {
			return fmt.Errorf("check google email ownership: %w", emailErr)
		}
		userID, err = newUUIDv7()
		if err != nil {
			return err
		}
		identityID, err := newUUIDv7()
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO users (id, email, normalized_email, email_verified, display_name, avatar_url, created_at, updated_at)
			VALUES ($1, $2, $3, TRUE, $4, $5, $6, $6)`,
			userID, email, normalizedEmail, displayName, avatarURL, input.ExchangedAt,
		)
		if err != nil {
			return fmt.Errorf("insert google user: %w", err)
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO auth_identities (id, user_id, provider, provider_subject, created_at)
			VALUES ($1, $2, $3, $4, $5)`,
			identityID, userID, ProviderGoogle, subject, input.ExchangedAt,
		)
		if err != nil {
			return fmt.Errorf("insert google identity: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("find google identity: %w", err)
	}
	input.Session.UserID = userID
	if err := insertSession(ctx, tx, input.Session); err != nil {
		return err
	}
	if err := deleteGoogleGrantAndAttempt(ctx, tx, grantID, attemptID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit google exchange: %w", err)
	}
	return nil
}

func deleteGoogleGrantAndAttempt(ctx context.Context, tx pgx.Tx, grantID, attemptID uuid.UUID) error {
	if _, err := tx.Exec(ctx, `DELETE FROM google_exchange_grants WHERE id = $1`, grantID); err != nil {
		return fmt.Errorf("delete google exchange grant: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM google_login_attempts WHERE id = $1`, attemptID); err != nil {
		return fmt.Errorf("delete google login attempt: %w", err)
	}
	return nil
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
