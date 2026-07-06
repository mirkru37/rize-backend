CREATE TABLE user_app_settings (
    user_id      uuid NOT NULL REFERENCES users (id),
    app_id       uuid NOT NULL REFERENCES apps (id),
    category_id  uuid REFERENCES categories (id),
    excluded     boolean NOT NULL DEFAULT false,
    updated_at   timestamptz NOT NULL,
    server_seq   bigint NOT NULL,
    PRIMARY KEY (user_id, app_id)
);
