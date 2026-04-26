CREATE TABLE IF NOT EXISTS discord.poll_cycles (
    ticked_at      TIMESTAMPTZ NOT NULL,
    binding_id     TEXT        NOT NULL,
    status         TEXT        NOT NULL,
    attempts       INT         NOT NULL,
    item_count     INT         NOT NULL DEFAULT 0,
    duration_ms    INT         NOT NULL,
    last_error     TEXT,
    PRIMARY KEY (ticked_at, binding_id)
);

SELECT create_hypertable('discord.poll_cycles', by_range('ticked_at'), if_not_exists => TRUE);
SELECT add_retention_policy('discord.poll_cycles', INTERVAL '90 days', if_not_exists => TRUE);

CREATE TABLE IF NOT EXISTS discord.diagnose_reports (
    triggered_at        TIMESTAMPTZ NOT NULL,
    binding_id          TEXT        NOT NULL,
    report              JSONB       NOT NULL,
    posted_message_id   TEXT,
    PRIMARY KEY (triggered_at, binding_id)
);

SELECT create_hypertable('discord.diagnose_reports', by_range('triggered_at'), if_not_exists => TRUE);
SELECT add_retention_policy('discord.diagnose_reports', INTERVAL '180 days', if_not_exists => TRUE);
