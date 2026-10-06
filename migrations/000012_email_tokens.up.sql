-- One-time tokens sent by email. Only hashes are stored, and each token works once.
CREATE TABLE email_tokens (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    purpose TEXT NOT NULL CHECK (purpose IN ('VERIFY_EMAIL', 'RESET_PASSWORD')),
    -- The normalized address the token was sent to; it must still match when used.
    email TEXT NOT NULL,
    token_hash BYTEA NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    created_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ
);

CREATE INDEX email_tokens_user_idx ON email_tokens (user_id, purpose, created_at DESC);

-- A Google Login attempt started with an Access Token links to that User instead of
-- signing in.
ALTER TABLE oauth_logins ADD COLUMN link_user_id UUID REFERENCES users(id) ON DELETE CASCADE;
