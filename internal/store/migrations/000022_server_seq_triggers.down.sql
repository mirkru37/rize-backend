DROP TRIGGER focus_sessions_set_server_seq ON focus_sessions;
ALTER TABLE focus_sessions ALTER COLUMN server_seq SET DEFAULT nextval('server_seq_global');

DROP TRIGGER activity_events_set_server_seq ON activity_events;
ALTER TABLE activity_events ALTER COLUMN server_seq SET DEFAULT nextval('server_seq_global');

DROP TRIGGER tags_set_server_seq ON tags;
ALTER TABLE tags ALTER COLUMN server_seq SET DEFAULT nextval('server_seq_global');

DROP TRIGGER projects_set_server_seq ON projects;
ALTER TABLE projects ALTER COLUMN server_seq SET DEFAULT nextval('server_seq_global');

DROP TRIGGER user_app_settings_set_server_seq ON user_app_settings;
ALTER TABLE user_app_settings ALTER COLUMN server_seq SET DEFAULT nextval('server_seq_global');

DROP TRIGGER categories_set_server_seq ON categories;
ALTER TABLE categories ALTER COLUMN server_seq SET DEFAULT nextval('server_seq_global');

DROP TRIGGER users_set_server_seq ON users;
ALTER TABLE users ALTER COLUMN server_seq SET DEFAULT nextval('server_seq_global');

DROP FUNCTION set_server_seq();
