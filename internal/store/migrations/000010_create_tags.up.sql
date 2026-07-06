CREATE TABLE tags (
    id          uuid PRIMARY KEY,
    user_id     uuid NOT NULL REFERENCES users (id),
    name        text NOT NULL,
    updated_at  timestamptz NOT NULL,
    deleted_at  timestamptz,
    server_seq  bigint NOT NULL,
    UNIQUE (user_id, name)
);
