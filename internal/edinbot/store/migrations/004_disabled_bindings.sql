-- Tracks bindings that have been disabled (e.g. channel deleted, bot kicked
-- from guild) independently of posted_messages, so we correctly handle the
-- case where the very first post fails with ErrChannelGone (in which case
-- no posted_messages row exists yet to flip a flag on).
CREATE TABLE IF NOT EXISTS discord.disabled_bindings (
    binding_id  TEXT        PRIMARY KEY,
    disabled_at TIMESTAMPTZ NOT NULL
);
