-- documentation/sync-protocol.md describes a single, per-user-agnostic
-- "server's global server_seq sequence space" that clients page through
-- with keyset pagination. A per-table sequence (or per-table
-- application-assigned value) cannot guarantee that ordering holds across
-- tables, so every syncable table's server_seq column defaults to the same
-- shared sequence.
CREATE SEQUENCE server_seq_global;

ALTER TABLE users ALTER COLUMN server_seq SET DEFAULT nextval('server_seq_global');
ALTER TABLE categories ALTER COLUMN server_seq SET DEFAULT nextval('server_seq_global');
ALTER TABLE user_app_settings ALTER COLUMN server_seq SET DEFAULT nextval('server_seq_global');
ALTER TABLE projects ALTER COLUMN server_seq SET DEFAULT nextval('server_seq_global');
ALTER TABLE tags ALTER COLUMN server_seq SET DEFAULT nextval('server_seq_global');
ALTER TABLE activity_events ALTER COLUMN server_seq SET DEFAULT nextval('server_seq_global');
ALTER TABLE focus_sessions ALTER COLUMN server_seq SET DEFAULT nextval('server_seq_global');
