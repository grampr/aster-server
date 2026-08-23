CREATE TABLE message_reactions (
    message_id UUID NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    emoji TEXT NOT NULL CHECK (char_length(emoji) BETWEEN 1 AND 64),
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (message_id, user_id, emoji)
);

CREATE INDEX message_reactions_message_emoji_idx
    ON message_reactions (message_id, emoji);
