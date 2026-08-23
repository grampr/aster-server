DROP INDEX IF EXISTS messages_reply_to_message_id_idx;
ALTER TABLE messages DROP COLUMN IF EXISTS reply_to_message_id;
