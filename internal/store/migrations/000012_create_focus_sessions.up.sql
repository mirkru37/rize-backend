CREATE TABLE focus_sessions (
    id                  uuid PRIMARY KEY,
    user_id             uuid NOT NULL REFERENCES users (id),
    device_id           uuid NOT NULL REFERENCES devices (id),
    project_id          uuid REFERENCES projects (id),
    kind                text NOT NULL CHECK (kind IN ('focus', 'break', 'meeting')),
    planned_duration_s  int,
    started_at          timestamptz NOT NULL,
    ended_at            timestamptz,
    status              text NOT NULL CHECK (status IN ('running', 'completed', 'abandoned')),
    note                text,
    created_at          timestamptz NOT NULL,
    updated_at          timestamptz NOT NULL,
    deleted_at          timestamptz,
    server_seq          bigint NOT NULL
);

CREATE INDEX focus_sessions_user_id_idx ON focus_sessions (user_id);
CREATE INDEX focus_sessions_user_server_seq_idx ON focus_sessions (user_id, server_seq);
