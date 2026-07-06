CREATE TABLE apps (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    bundle_id            text NOT NULL,
    platform             text NOT NULL,
    name                 text NOT NULL,
    default_category_id  uuid REFERENCES categories (id),
    UNIQUE (bundle_id, platform)
);
