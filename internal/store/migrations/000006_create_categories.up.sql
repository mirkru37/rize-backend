CREATE TABLE categories (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      uuid REFERENCES users (id),
    name         text NOT NULL,
    color        text NOT NULL,
    productivity smallint NOT NULL CHECK (productivity BETWEEN -2 AND 2),
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL,
    deleted_at   timestamptz,
    server_seq   bigint NOT NULL
);

CREATE INDEX categories_user_id_idx ON categories (user_id);
