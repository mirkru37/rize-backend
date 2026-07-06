-- RIZ-34: GET /v1/sync/changes keyset-paginates each syncable table with
-- `WHERE user_id = $1 AND server_seq > $2 ORDER BY server_seq`, the same
-- access pattern activity_events (000011) and focus_sessions (000012)
-- already carry a `(user_id, server_seq)` index for. projects, tags, and
-- user_app_settings are pulled the same way (per
-- documentation/sync-protocol.md §Entity Classes and
-- documentation/database-schema.md's "server_seq is present on every
-- syncable table" convention) but were never given the matching index, so
-- this migration adds it for those three tables. This is an additive,
-- performance-only index — it does not change any documented table shape.
CREATE INDEX projects_user_server_seq_idx ON projects (user_id, server_seq);
CREATE INDEX tags_user_server_seq_idx ON tags (user_id, server_seq);
CREATE INDEX user_app_settings_user_server_seq_idx ON user_app_settings (user_id, server_seq);
