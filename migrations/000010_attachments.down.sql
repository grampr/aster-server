DROP TABLE attachments;
DROP FUNCTION enqueue_attachment_deletion();
DROP TABLE storage_deletions;
DELETE FROM messages WHERE content = '';
ALTER TABLE messages DROP CONSTRAINT messages_content_check;
ALTER TABLE messages ADD CONSTRAINT messages_content_check CHECK (char_length(content) BETWEEN 1 AND 4000);
