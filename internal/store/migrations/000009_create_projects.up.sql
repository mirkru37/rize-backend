CREATE TABLE projects (
    id           uuid PRIMARY KEY,
    user_id      uuid NOT NULL REFERENCES users (id),
    name         text NOT NULL,
    color        text NOT NULL,
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL,
    archived_at  timestamptz,
    deleted_at   timestamptz,
    server_seq   bigint NOT NULL
);

CREATE INDEX projects_user_id_idx ON projects (user_id);
