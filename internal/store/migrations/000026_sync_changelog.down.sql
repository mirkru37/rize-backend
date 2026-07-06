DROP TRIGGER IF EXISTS categories_log_change ON categories;
DROP FUNCTION IF EXISTS log_category_change();

DROP TRIGGER IF EXISTS user_app_settings_log_change ON user_app_settings;
DROP FUNCTION IF EXISTS log_user_app_setting_change();

DROP TRIGGER IF EXISTS tags_log_change ON tags;
DROP FUNCTION IF EXISTS log_tag_change();

DROP TRIGGER IF EXISTS projects_log_change ON projects;
DROP FUNCTION IF EXISTS log_project_change();

DROP TRIGGER IF EXISTS focus_sessions_log_change ON focus_sessions;
DROP FUNCTION IF EXISTS log_focus_session_change();

DROP TRIGGER IF EXISTS activity_events_log_change ON activity_events;
DROP FUNCTION IF EXISTS log_activity_event_change();

DROP TABLE IF EXISTS sync_changelog;
