-- One row per Google Login attempt. The OAuth state and the Aster Exchange Code are
-- stored only as hashes and can each be used once.
CREATE TABLE oauth_logins (
    id UUID PRIMARY KEY,
    state_hash BYTEA NOT NULL UNIQUE CHECK (octet_length(state_hash) = 32),
    nonce TEXT NOT NULL,
    code_challenge TEXT NOT NULL,
    client_state TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    callback_at TIMESTAMPTZ,
    exchange_code_hash BYTEA UNIQUE CHECK (exchange_code_hash IS NULL OR octet_length(exchange_code_hash) = 32),
    exchange_expires_at TIMESTAMPTZ,
    exchange_consumed_at TIMESTAMPTZ,
    google_subject TEXT,
    email TEXT,
    email_verified BOOLEAN,
    display_name TEXT,
    avatar_url TEXT
);

CREATE INDEX oauth_logins_expires_idx ON oauth_logins (expires_at);
