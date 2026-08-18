CREATE TABLE users (
    id UUID PRIMARY KEY,
    email TEXT NOT NULL,
    normalized_email TEXT NOT NULL UNIQUE,
    email_verified BOOLEAN NOT NULL DEFAULT FALSE,
    display_name TEXT NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 64),
    avatar_url TEXT,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE auth_identities (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider TEXT NOT NULL CHECK (provider <> ''),
    provider_subject TEXT NOT NULL CHECK (provider_subject <> ''),
    password_hash TEXT,
    created_at TIMESTAMPTZ NOT NULL,
    UNIQUE (provider, provider_subject),
    CHECK (
        (provider = 'PASSWORD' AND password_hash IS NOT NULL)
        OR (provider <> 'PASSWORD' AND password_hash IS NULL)
    )
);

CREATE INDEX auth_identities_user_id_idx ON auth_identities (user_id);

CREATE TABLE sessions (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_family_id UUID NOT NULL,
    access_token_hash BYTEA NOT NULL UNIQUE CHECK (octet_length(access_token_hash) = 32),
    access_expires_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CHECK (access_expires_at <= expires_at)
);

CREATE INDEX sessions_user_id_idx ON sessions (user_id);
CREATE INDEX sessions_active_expiry_idx ON sessions (expires_at) WHERE revoked_at IS NULL;

CREATE TABLE session_refresh_tokens (
    id UUID PRIMARY KEY,
    session_id UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    token_hash BYTEA NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX session_refresh_tokens_session_id_idx ON session_refresh_tokens (session_id);
