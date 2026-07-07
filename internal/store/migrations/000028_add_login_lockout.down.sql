ALTER TABLE users
    DROP COLUMN failed_login_attempts,
    DROP COLUMN lockout_count,
    DROP COLUMN locked_until;
