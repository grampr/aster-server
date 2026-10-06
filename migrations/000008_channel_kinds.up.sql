ALTER TABLE channels DROP CONSTRAINT channels_type_check;
ALTER TABLE channels DROP CONSTRAINT channels_check;
ALTER TABLE channels ALTER COLUMN guild_id DROP NOT NULL;
ALTER TABLE channels ALTER COLUMN name DROP NOT NULL;

-- parent_id is the Category of a Guild Channel or the parent Text Channel of a Thread.
ALTER TABLE channels ADD COLUMN parent_id UUID REFERENCES channels(id) ON DELETE SET NULL;
ALTER TABLE channels ADD COLUMN starter_message_id UUID REFERENCES messages(id) ON DELETE SET NULL;
ALTER TABLE channels ADD COLUMN dm_key TEXT UNIQUE;

ALTER TABLE channels ADD CONSTRAINT channels_type_check
    CHECK (type IN ('TEXT', 'VOICE', 'CATEGORY', 'THREAD', 'DIRECT'));
ALTER TABLE channels ADD CONSTRAINT channels_shape_check CHECK (
    (type = 'DIRECT' AND guild_id IS NULL AND name IS NULL AND parent_id IS NULL AND dm_key IS NOT NULL)
    OR (type <> 'DIRECT' AND guild_id IS NOT NULL AND name IS NOT NULL AND dm_key IS NULL)
);
ALTER TABLE channels ADD CONSTRAINT channels_topic_type_check CHECK (topic IS NULL OR type = 'TEXT');
ALTER TABLE channels ADD CONSTRAINT channels_category_check CHECK (type <> 'CATEGORY' OR parent_id IS NULL);

CREATE UNIQUE INDEX channels_starter_message_idx ON channels (starter_message_id) WHERE starter_message_id IS NOT NULL;
CREATE INDEX channels_thread_page_idx ON channels (parent_id, updated_at DESC, id DESC) WHERE type = 'THREAD';

CREATE TABLE dm_participants (
    channel_id UUID NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (channel_id, user_id)
);

CREATE INDEX dm_participants_user_idx ON dm_participants (user_id);

-- Every User who may read and write a Channel: Guild Members for Guild Channels and
-- Threads, the two participants for a Direct Message.
CREATE VIEW channel_participants AS
    SELECT c.id AS channel_id, gm.user_id
    FROM channels c
    JOIN guild_members gm ON gm.guild_id = c.guild_id
    UNION ALL
    SELECT channel_id, user_id FROM dm_participants;
