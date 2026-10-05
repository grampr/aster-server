-- A Message may consist of attachments alone, so an empty body is allowed.
ALTER TABLE messages DROP CONSTRAINT messages_content_check;
ALTER TABLE messages ADD CONSTRAINT messages_content_check CHECK (char_length(content) <= 4000);

CREATE TABLE attachments (
    id UUID PRIMARY KEY,
    channel_id UUID NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    uploader_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    message_id UUID REFERENCES messages(id) ON DELETE CASCADE,
    filename TEXT NOT NULL CHECK (char_length(filename) BETWEEN 1 AND 255),
    content_type TEXT NOT NULL CHECK (char_length(content_type) BETWEEN 3 AND 255),
    size BIGINT NOT NULL CHECK (size BETWEEN 1 AND 26214400),
    checksum_sha256 TEXT NOT NULL CHECK (checksum_sha256 ~ '^[0-9a-f]{64}$'),
    status TEXT NOT NULL CHECK (status IN ('PENDING', 'READY')),
    object_key TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL,
    CHECK (message_id IS NULL OR status = 'READY')
);

CREATE INDEX attachments_message_idx ON attachments (message_id) WHERE message_id IS NOT NULL;
CREATE INDEX attachments_unused_idx ON attachments (uploader_id, created_at) WHERE message_id IS NULL;

-- Objects are removed asynchronously, so deleting a row (directly or through a
-- cascade from a Message, Channel, Guild or User) never leaves an Object behind.
CREATE TABLE storage_deletions (
    object_key TEXT PRIMARY KEY,
    queued_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE FUNCTION enqueue_attachment_deletion() RETURNS trigger AS $$
BEGIN
    INSERT INTO storage_deletions (object_key) VALUES (OLD.object_key) ON CONFLICT DO NOTHING;
    RETURN OLD;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER attachments_enqueue_deletion
    AFTER DELETE ON attachments
    FOR EACH ROW EXECUTE FUNCTION enqueue_attachment_deletion();
