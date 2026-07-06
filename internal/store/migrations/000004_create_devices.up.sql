CREATE TABLE devices (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      uuid NOT NULL REFERENCES users (id),
    platform     text NOT NULL CHECK (platform IN ('macos', 'ios')),
    name         text NOT NULL,
    model        text NOT NULL,
    os_version   text NOT NULL,
    app_version  text NOT NULL,
    last_seen_at timestamptz NOT NULL,
    created_at   timestamptz NOT NULL,
    revoked_at   timestamptz
);

CREATE INDEX devices_user_id_idx ON devices (user_id);
