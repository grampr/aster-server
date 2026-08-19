CREATE TABLE guilds (
    id UUID PRIMARY KEY,
    owner_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    description TEXT CHECK (description IS NULL OR char_length(description) <= 1024),
    icon_url TEXT,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX guilds_owner_id_idx ON guilds (owner_id);

CREATE TABLE guild_members (
    guild_id UUID NOT NULL REFERENCES guilds(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    joined_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (guild_id, user_id)
);

CREATE INDEX guild_members_user_page_idx ON guild_members (user_id, joined_at DESC, guild_id DESC);

CREATE TABLE channels (
    id UUID PRIMARY KEY,
    guild_id UUID NOT NULL REFERENCES guilds(id) ON DELETE CASCADE,
    type TEXT NOT NULL CHECK (type IN ('TEXT', 'VOICE')),
    name TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    topic TEXT CHECK (topic IS NULL OR char_length(topic) <= 1024),
    position INTEGER NOT NULL CHECK (position >= 0),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CHECK (type = 'TEXT' OR topic IS NULL)
);

CREATE INDEX channels_guild_page_idx ON channels (guild_id, position, id);

CREATE TABLE messages (
    id UUID PRIMARY KEY,
    channel_id UUID NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    author_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    content TEXT NOT NULL CHECK (char_length(content) BETWEEN 1 AND 4000),
    created_at TIMESTAMPTZ NOT NULL,
    edited_at TIMESTAMPTZ
);

CREATE INDEX messages_channel_page_idx ON messages (channel_id, created_at DESC, id DESC);
CREATE INDEX messages_author_id_idx ON messages (author_id);
