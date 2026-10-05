CREATE TABLE read_states (
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    channel_id UUID NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    -- Deliberately not a foreign key: the position survives deletion of that Message.
    last_read_message_id UUID NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (user_id, channel_id)
);
