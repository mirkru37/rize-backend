-- activity_events is the append-only, high-volume time-series table for
-- tracked activity, implemented as a TimescaleDB hypertable partitioned on
-- started_at with a 7-day chunk interval, per
-- documentation/database-schema.md §activity_events / §Hypertable Notes.
CREATE TABLE activity_events (
    event_id       uuid NOT NULL,
    user_id        uuid NOT NULL REFERENCES users (id),
    device_id      uuid NOT NULL REFERENCES devices (id),
    started_at     timestamptz NOT NULL,
    ended_at       timestamptz NOT NULL,
    duration_s     int GENERATED ALWAYS AS (EXTRACT(EPOCH FROM (ended_at - started_at))::int) STORED,
    type           text NOT NULL CHECK (type IN ('app_active', 'idle', 'locked', 'mobile_usage', 'manual')),
    source         text NOT NULL CHECK (source IN ('desktop', 'mobile', 'manual')),
    precision      text NOT NULL DEFAULT 'exact' CHECK (precision IN ('exact', 'approximate')),
    app_id         uuid REFERENCES apps (id),
    raw_bundle_id  text,
    window_title   text,
    url            text,
    category_id    uuid REFERENCES categories (id),
    project_id     uuid REFERENCES projects (id),
    deleted        boolean NOT NULL DEFAULT false,
    inserted_at    timestamptz NOT NULL,
    server_seq     bigint NOT NULL,
    -- TimescaleDB requires the partitioning column (started_at) to be part
    -- of any primary key / unique constraint on a hypertable.
    PRIMARY KEY (user_id, started_at, event_id),
    -- Idempotency constraint: lets an upload retry safely re-submit the
    -- same event without creating a duplicate row.
    --
    -- No standalone UNIQUE (event_id) constraint exists on this table:
    -- TimescaleDB requires every unique constraint on a hypertable to
    -- include the partitioning column (started_at), so event_id alone
    -- cannot be made unique. This is why event_tags references the
    -- composite primary key rather than a simple event_id foreign key.
    CONSTRAINT activity_events_idempotency_key UNIQUE (user_id, event_id, started_at)
);

SELECT create_hypertable('activity_events', 'started_at', chunk_time_interval => INTERVAL '7 days');

CREATE INDEX activity_events_user_started_idx ON activity_events (user_id, started_at DESC);
CREATE INDEX activity_events_user_app_started_idx ON activity_events (user_id, app_id, started_at);
CREATE INDEX activity_events_user_server_seq_idx ON activity_events (user_id, server_seq);

-- Compression policy: chunks older than 30 days are compressed.
ALTER TABLE activity_events SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'user_id'
);

SELECT add_compression_policy('activity_events', INTERVAL '30 days');
