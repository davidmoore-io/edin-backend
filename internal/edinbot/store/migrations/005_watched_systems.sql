-- Phase 3 of the /watch /unwatch system-watch feature.
-- See docs/plans/system-watch-feature.md.
--
-- One row per (channel, system) — that's the dedup unit per the operator
-- spec ("one shared message per system, /watch on a system that's already
-- watched is rejected politely"). The PRIMARY KEY is what enforces that
-- invariant — the publisher's add-watch handler treats a unique-violation
-- as ErrAlreadyWatched and surfaces a polite ephemeral.
--
-- created_by holds the Discord user ID of whoever ran /watch; it's
-- audit/trace info only — the watch is shared, not owned, so /unwatch
-- doesn't check this field. Carried for future "who started this watch?"
-- visibility if we ever surface that.
--
-- last_render is the JSON-encoded embed we last sent. The watch poller
-- diffs against last_state_hash (computed off the snapshot, not the
-- render) to decide when to edit; last_render is preserved so we can
-- inspect what's currently in Discord without re-fetching from Memgraph.

CREATE TABLE IF NOT EXISTS discord.watched_systems (
    guild_id        TEXT        NOT NULL,
    channel_id      TEXT        NOT NULL,
    system_slug     TEXT        NOT NULL,
    system_name     TEXT        NOT NULL,
    message_id      TEXT        NOT NULL,
    created_by      TEXT        NOT NULL,
    watched_at      TIMESTAMPTZ NOT NULL,
    last_updated_at TIMESTAMPTZ NOT NULL,
    last_state_hash TEXT        NOT NULL,
    last_render     JSONB       NOT NULL,
    PRIMARY KEY (channel_id, system_slug)
);

-- Index for the boot-recovery scan, which lists every watch across every
-- channel to spin up the polling goroutine. Without an index this is a
-- table scan; with this index it's a btree walk that scales with the
-- (low) total watch count.
CREATE INDEX IF NOT EXISTS idx_watched_systems_guild
    ON discord.watched_systems(guild_id);
