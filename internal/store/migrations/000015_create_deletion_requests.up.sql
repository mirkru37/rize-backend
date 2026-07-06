CREATE TABLE deletion_requests (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id        uuid NOT NULL REFERENCES users (id),
    requested_at   timestamptz NOT NULL,
    execute_after  timestamptz NOT NULL,
    executed_at    timestamptz
);

CREATE INDEX deletion_requests_user_id_idx ON deletion_requests (user_id);
