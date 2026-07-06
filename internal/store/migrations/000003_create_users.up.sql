CREATE TABLE users (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email          citext UNIQUE,
    password_hash  text,
    apple_user_id  text UNIQUE,
    display_name   text NOT NULL,
    role           text NOT NULL DEFAULT 'user' CHECK (role IN ('user', 'admin')),
    timezone       text NOT NULL,
    created_at     timestamptz NOT NULL,
    updated_at     timestamptz NOT NULL,
    deleted_at     timestamptz,
    server_seq     bigint NOT NULL
);
