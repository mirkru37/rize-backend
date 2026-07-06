-- event_tags carries user_id/started_at alongside entity_id so its foreign
-- key can reference activity_events' composite primary key
-- (user_id, started_at, event_id): activity_events is a TimescaleDB
-- hypertable, so no unique key on event_id alone exists for a simple FK to
-- target, per documentation/database-schema.md §event_tags / session_tags.
CREATE TABLE event_tags (
    user_id     uuid NOT NULL,
    started_at  timestamptz NOT NULL,
    entity_id   uuid NOT NULL,
    tag_id      uuid NOT NULL REFERENCES tags (id),
    PRIMARY KEY (entity_id, tag_id),
    CONSTRAINT event_tags_activity_event_fk
        FOREIGN KEY (user_id, started_at, entity_id)
        REFERENCES activity_events (user_id, started_at, event_id)
);

-- session_tags.entity_id references focus_sessions(id), a simple
-- non-composite primary key.
CREATE TABLE session_tags (
    entity_id  uuid NOT NULL REFERENCES focus_sessions (id),
    tag_id     uuid NOT NULL REFERENCES tags (id),
    PRIMARY KEY (entity_id, tag_id)
);

CREATE INDEX event_tags_tag_id_idx ON event_tags (tag_id);
CREATE INDEX session_tags_tag_id_idx ON session_tags (tag_id);
