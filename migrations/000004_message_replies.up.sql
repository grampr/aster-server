ALTER TABLE messages
    ADD COLUMN reply_to_message_id UUID;

CREATE INDEX messages_reply_to_message_id_idx
    ON messages (reply_to_message_id)
    WHERE reply_to_message_id IS NOT NULL;
