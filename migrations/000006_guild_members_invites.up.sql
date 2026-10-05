ALTER TABLE guild_members
    ADD COLUMN nickname TEXT CHECK (nickname IS NULL OR char_length(nickname) BETWEEN 1 AND 64);

CREATE INDEX guild_members_page_idx ON guild_members (guild_id, joined_at, user_id);

CREATE TABLE guild_invites (
    id UUID PRIMARY KEY,
    guild_id UUID NOT NULL REFERENCES guilds(id) ON DELETE CASCADE,
    inviter_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code TEXT NOT NULL UNIQUE CHECK (code ~ '^[A-Za-z0-9_-]{16,64}$'),
    max_uses INTEGER CHECK (max_uses IS NULL OR max_uses BETWEEN 1 AND 1000),
    uses INTEGER NOT NULL DEFAULT 0 CHECK (uses >= 0),
    expires_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    CHECK (max_uses IS NULL OR uses <= max_uses)
);

CREATE INDEX guild_invites_guild_idx ON guild_invites (guild_id, created_at DESC, id DESC);
