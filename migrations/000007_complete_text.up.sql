ALTER TABLE channels DROP CONSTRAINT IF EXISTS channels_type_check;
ALTER TABLE channels DROP CONSTRAINT IF EXISTS channels_check;
ALTER TABLE channels ALTER COLUMN guild_id DROP NOT NULL;
ALTER TABLE channels ALTER COLUMN name DROP NOT NULL;
ALTER TABLE channels ADD COLUMN parent_id UUID REFERENCES channels(id) ON DELETE CASCADE;
ALTER TABLE channels ADD COLUMN starter_message_id UUID REFERENCES messages(id) ON DELETE SET NULL;
ALTER TABLE channels ADD CONSTRAINT channels_type_check CHECK (type IN ('TEXT', 'VOICE', 'CATEGORY', 'THREAD', 'DIRECT'));
ALTER TABLE channels ADD CONSTRAINT channels_shape_check CHECK (
    (type = 'DIRECT' AND guild_id IS NULL AND name IS NULL AND topic IS NULL)
    OR
    (type <> 'DIRECT' AND guild_id IS NOT NULL AND name IS NOT NULL)
);
ALTER TABLE channels ADD CONSTRAINT channels_parent_check CHECK (
    (type = 'THREAD' AND parent_id IS NOT NULL)
    OR type <> 'THREAD'
);

CREATE TABLE direct_channel_members (
    channel_id UUID NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    joined_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (channel_id, user_id)
);

CREATE INDEX direct_channel_members_user_idx ON direct_channel_members (user_id, channel_id);

CREATE TABLE channel_read_states (
    channel_id UUID NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    last_read_message_id UUID NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (channel_id, user_id)
);

CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE INDEX messages_content_trgm_idx ON messages USING gin (content gin_trgm_ops);
CREATE INDEX messages_search_page_idx ON messages (created_at DESC, id DESC);
