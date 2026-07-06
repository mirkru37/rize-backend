CREATE TABLE sync_cursors (
    device_id       uuid PRIMARY KEY REFERENCES devices (id),
    last_pull_seq   bigint NOT NULL,
    last_push_at    timestamptz NOT NULL,
    updated_at      timestamptz NOT NULL
);
