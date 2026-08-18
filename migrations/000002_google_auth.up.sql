CREATE TABLE google_login_attempts (
    id UUID PRIMARY KEY,
    oauth_state_hash BYTEA NOT NULL UNIQUE CHECK (octet_length(oauth_state_hash) = 32),
    client_state TEXT NOT NULL CHECK (char_length(client_state) BETWEEN 43 AND 128),
    code_challenge TEXT NOT NULL CHECK (char_length(code_challenge) = 43),
    redirect_uri TEXT NOT NULL CHECK (redirect_uri = 'aster://auth/callback'),
    nonce TEXT NOT NULL CHECK (char_length(nonce) >= 43),
    provider_code_verifier TEXT NOT NULL CHECK (char_length(provider_code_verifier) BETWEEN 43 AND 128),
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX google_login_attempts_expiry_idx ON google_login_attempts (expires_at) WHERE consumed_at IS NULL;

CREATE TABLE google_exchange_grants (
    id UUID PRIMARY KEY,
    attempt_id UUID NOT NULL UNIQUE REFERENCES google_login_attempts(id) ON DELETE CASCADE,
    code_hash BYTEA NOT NULL UNIQUE CHECK (octet_length(code_hash) = 32),
    provider_subject TEXT NOT NULL CHECK (provider_subject <> ''),
    email TEXT NOT NULL,
    normalized_email TEXT NOT NULL,
    email_verified BOOLEAN NOT NULL CHECK (email_verified),
    display_name TEXT NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 64),
    avatar_url TEXT,
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX google_exchange_grants_expiry_idx ON google_exchange_grants (expires_at) WHERE consumed_at IS NULL;
