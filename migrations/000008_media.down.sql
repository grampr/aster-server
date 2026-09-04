DROP TABLE IF EXISTS voice_states;
DROP TABLE IF EXISTS voice_rooms;
DROP TABLE IF EXISTS message_attachments;
DROP TABLE IF EXISTS attachments;
DELETE FROM messages WHERE content = '';
ALTER TABLE messages DROP CONSTRAINT messages_content_check;
ALTER TABLE messages ADD CONSTRAINT messages_content_check CHECK (char_length(content) BETWEEN 1 AND 4000);
UPDATE roles
SET permissions = permissions & ~512,
    updated_at = NOW()
WHERE managed = TRUE AND name = '@everyone';
