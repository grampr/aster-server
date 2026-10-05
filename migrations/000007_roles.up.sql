CREATE TABLE guild_roles (
    id UUID PRIMARY KEY,
    guild_id UUID NOT NULL REFERENCES guilds(id) ON DELETE CASCADE,
    name TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    color TEXT CHECK (color IS NULL OR color ~ '^#[0-9A-Fa-f]{6}$'),
    permissions INTEGER NOT NULL CHECK (permissions >= 0),
    position INTEGER NOT NULL CHECK (position >= 0),
    managed BOOLEAN NOT NULL DEFAULT FALSE,
    is_default BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL,
    UNIQUE (guild_id, id),
    CHECK (NOT is_default OR (managed AND position = 0))
);

CREATE UNIQUE INDEX guild_roles_default_idx ON guild_roles (guild_id) WHERE is_default;
CREATE INDEX guild_roles_page_idx ON guild_roles (guild_id, position, id);

CREATE TABLE guild_member_roles (
    guild_id UUID NOT NULL,
    user_id UUID NOT NULL,
    role_id UUID NOT NULL,
    PRIMARY KEY (guild_id, user_id, role_id),
    FOREIGN KEY (guild_id, user_id) REFERENCES guild_members (guild_id, user_id) ON DELETE CASCADE,
    FOREIGN KEY (guild_id, role_id) REFERENCES guild_roles (guild_id, id) ON DELETE CASCADE
);

CREATE INDEX guild_member_roles_role_idx ON guild_member_roles (role_id);

-- Every Guild has one managed default Role that applies to all Members.
-- 1795 = VIEW_CHANNEL | SEND_MESSAGES | CONNECT | SPEAK | STREAM
INSERT INTO guild_roles (id, guild_id, name, color, permissions, position, managed, is_default, created_at)
SELECT gen_random_uuid(), id, '@everyone', NULL, 1795, 0, TRUE, TRUE, created_at FROM guilds;
