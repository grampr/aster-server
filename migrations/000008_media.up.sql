CREATE TABLE attachments (
    id UUID PRIMARY KEY,
    uploader_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    channel_id UUID NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    object_key TEXT NOT NULL UNIQUE,
    filename TEXT NOT NULL,
    content_type TEXT NOT NULL,
    size BIGINT NOT NULL CHECK (size > 0 AND size <= 26214400),
    checksum_sha256 TEXT NOT NULL CHECK (checksum_sha256 ~ '^[0-9a-f]{64}$'),
    status TEXT NOT NULL CHECK (status IN ('PENDING', 'READY')),
    created_at TIMESTAMPTZ NOT NULL,
    finalized_at TIMESTAMPTZ
);

ALTER TABLE messages DROP CONSTRAINT messages_content_check;
ALTER TABLE messages ADD CONSTRAINT messages_content_check CHECK (char_length(content) BETWEEN 0 AND 4000);

-- Voice channels should be immediately usable by ordinary guild members.
UPDATE roles
SET permissions = permissions | 512,
    updated_at = NOW()
WHERE managed = TRUE AND name = '@everyone';

CREATE INDEX attachments_uploader_created_idx ON attachments (uploader_id, created_at DESC);
CREATE INDEX attachments_pending_idx ON attachments (created_at) WHERE status = 'PENDING';

CREATE TABLE message_attachments (
    message_id UUID NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    attachment_id UUID NOT NULL UNIQUE REFERENCES attachments(id) ON DELETE CASCADE,
    position SMALLINT NOT NULL CHECK (position >= 0 AND position < 10),
    PRIMARY KEY (message_id, attachment_id),
    UNIQUE (message_id, position)
);

CREATE TABLE voice_rooms (
    channel_id UUID PRIMARY KEY REFERENCES channels(id) ON DELETE CASCADE,
    provider_room_id TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE voice_states (
    user_id UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    channel_id UUID NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    session_id UUID NOT NULL UNIQUE,
    provider_participant_id TEXT NOT NULL,
    self_mute BOOLEAN NOT NULL,
    self_deaf BOOLEAN NOT NULL,
    self_video BOOLEAN NOT NULL,
    self_stream BOOLEAN NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX voice_states_channel_idx ON voice_states (channel_id, updated_at, user_id);
CREATE INDEX voice_states_expiry_idx ON voice_states (expires_at);
