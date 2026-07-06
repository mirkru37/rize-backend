-- RIZ-33: replace the per-column `DEFAULT nextval('server_seq_global')`
-- approach introduced in 000021 with a BEFORE INSERT OR UPDATE trigger on
-- every syncable table. A column DEFAULT only fires when the column is
-- omitted from an INSERT's column list, and does nothing at all for
-- UPDATE, which is why 000021 needed callers (see
-- internal/store/queries/users.sql's UpdateUserProfile/SoftDeleteUser) to
-- call nextval('server_seq_global') explicitly by hand on every UPDATE. A
-- trigger instead unconditionally assigns a fresh value from the shared
-- sequence on every INSERT and every UPDATE, so no caller — sqlc query or
-- ad-hoc SQL — can forget to bump server_seq on a write, per
-- documentation/database-schema.md ("server_seq is present on every
-- syncable table ... bumps server_seq on every write (insert or update)").
--
-- The trigger unconditionally overwrites NEW.server_seq (rather than only
-- filling it in when NULL), matching this migration's brief: "each trigger
-- setting NEW.server_seq = nextval('server_seq_global')" on every write,
-- regardless of what value (if any) the caller supplied.
CREATE FUNCTION set_server_seq() RETURNS trigger AS $$
BEGIN
    NEW.server_seq := nextval('server_seq_global');
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

ALTER TABLE users ALTER COLUMN server_seq DROP DEFAULT;
CREATE TRIGGER users_set_server_seq
    BEFORE INSERT OR UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION set_server_seq();

ALTER TABLE categories ALTER COLUMN server_seq DROP DEFAULT;
CREATE TRIGGER categories_set_server_seq
    BEFORE INSERT OR UPDATE ON categories
    FOR EACH ROW EXECUTE FUNCTION set_server_seq();

ALTER TABLE user_app_settings ALTER COLUMN server_seq DROP DEFAULT;
CREATE TRIGGER user_app_settings_set_server_seq
    BEFORE INSERT OR UPDATE ON user_app_settings
    FOR EACH ROW EXECUTE FUNCTION set_server_seq();

ALTER TABLE projects ALTER COLUMN server_seq DROP DEFAULT;
CREATE TRIGGER projects_set_server_seq
    BEFORE INSERT OR UPDATE ON projects
    FOR EACH ROW EXECUTE FUNCTION set_server_seq();

ALTER TABLE tags ALTER COLUMN server_seq DROP DEFAULT;
CREATE TRIGGER tags_set_server_seq
    BEFORE INSERT OR UPDATE ON tags
    FOR EACH ROW EXECUTE FUNCTION set_server_seq();

ALTER TABLE activity_events ALTER COLUMN server_seq DROP DEFAULT;
CREATE TRIGGER activity_events_set_server_seq
    BEFORE INSERT OR UPDATE ON activity_events
    FOR EACH ROW EXECUTE FUNCTION set_server_seq();

ALTER TABLE focus_sessions ALTER COLUMN server_seq DROP DEFAULT;
CREATE TRIGGER focus_sessions_set_server_seq
    BEFORE INSERT OR UPDATE ON focus_sessions
    FOR EACH ROW EXECUTE FUNCTION set_server_seq();
