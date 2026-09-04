ALTER TABLE guild_members
    ADD COLUMN nickname TEXT CHECK (nickname IS NULL OR char_length(nickname) BETWEEN 1 AND 64);

CREATE TABLE roles (
    id UUID PRIMARY KEY,
    guild_id UUID NOT NULL REFERENCES guilds(id) ON DELETE CASCADE,
    name TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    color TEXT CHECK (color IS NULL OR color ~ '^#[0-9A-Fa-f]{6}$'),
    permissions BIGINT NOT NULL DEFAULT 0 CHECK (permissions BETWEEN 0 AND 2147483647),
    position INTEGER NOT NULL CHECK (position >= 0),
    managed BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE UNIQUE INDEX roles_guild_position_idx ON roles (guild_id, position, id);
CREATE UNIQUE INDEX roles_everyone_idx ON roles (guild_id) WHERE managed;

CREATE TABLE guild_member_roles (
    guild_id UUID NOT NULL,
    user_id UUID NOT NULL,
    role_id UUID NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    assigned_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (guild_id, user_id, role_id),
    FOREIGN KEY (guild_id, user_id) REFERENCES guild_members(guild_id, user_id) ON DELETE CASCADE
);

CREATE INDEX guild_member_roles_role_idx ON guild_member_roles (role_id);

CREATE TABLE guild_invites (
    id UUID PRIMARY KEY,
    guild_id UUID NOT NULL REFERENCES guilds(id) ON DELETE CASCADE,
    inviter_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code TEXT NOT NULL UNIQUE CHECK (char_length(code) BETWEEN 16 AND 64),
    uses INTEGER NOT NULL DEFAULT 0 CHECK (uses >= 0),
    max_uses INTEGER CHECK (max_uses IS NULL OR max_uses BETWEEN 1 AND 1000),
    expires_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX guild_invites_guild_created_idx ON guild_invites (guild_id, created_at DESC);
CREATE INDEX guild_invites_active_code_idx ON guild_invites (code) WHERE revoked_at IS NULL;

INSERT INTO roles (id, guild_id, name, permissions, position, managed, created_at, updated_at)
SELECT gen_random_uuid(), id, '@everyone', 387, 0, TRUE, created_at, created_at
FROM guilds;
