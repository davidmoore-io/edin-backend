CREATE TABLE IF NOT EXISTS discord.posted_messages (
    binding_id        TEXT        NOT NULL,
    identity          TEXT        NOT NULL,
    guild_id          TEXT        NOT NULL,
    channel_id        TEXT        NOT NULL,
    message_id        TEXT        NOT NULL,
    state_hash        TEXT        NOT NULL,
    last_render       JSONB       NOT NULL,
    posted_at         TIMESTAMPTZ NOT NULL,
    last_edited_at    TIMESTAMPTZ,
    last_seen_at      TIMESTAMPTZ NOT NULL,
    struck_at         TIMESTAMPTZ,
    unstruck_at       TIMESTAMPTZ,
    disabled_at       TIMESTAMPTZ,
    PRIMARY KEY (binding_id, identity)
);

CREATE INDEX IF NOT EXISTS idx_posted_messages_seen
    ON discord.posted_messages (binding_id, last_seen_at);

CREATE INDEX IF NOT EXISTS idx_posted_messages_struck
    ON discord.posted_messages (binding_id) WHERE struck_at IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_posted_messages_disabled
    ON discord.posted_messages (binding_id) WHERE disabled_at IS NOT NULL;
