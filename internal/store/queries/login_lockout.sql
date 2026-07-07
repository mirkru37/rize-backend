-- name: RecordFailedLoginAttempt :one
-- Atomically records a failed password check against an existing account
-- and, if the post-increment attempt count reaches @threshold, escalates
-- into (or re-escalates) a lockout — per RIZ-59 / documentation/security.md
-- §API hardening ("brute-force lockout on login").
--
-- Everything is computed from the row's own current values inside this
-- single UPDATE statement (failed_login_attempts, lockout_count), rather
-- than read in Go and written back separately, so concurrent logins
-- against the same account can never race a read-modify-write: Postgres's
-- row-level write lock on this UPDATE serializes concurrent callers, and
-- each one sees the effect of the previous one's commit.
--
-- The lockout duration is base_duration_seconds doubled once per prior
-- lockout (lockout_count, read BEFORE this attempt's own increment),
-- capped at max_duration_seconds. @now is supplied by the caller (the
-- service's injectable clock) rather than Postgres's own now(), so the
-- lockout window is deterministic and testable under a fake clock.
UPDATE users
SET
    failed_login_attempts = failed_login_attempts + 1,
    lockout_count = CASE
        WHEN failed_login_attempts + 1 >= sqlc.arg(threshold)::int THEN lockout_count + 1
        ELSE lockout_count
    END,
    locked_until = CASE
        WHEN failed_login_attempts + 1 >= sqlc.arg(threshold)::int THEN
            sqlc.arg(now)::timestamptz + LEAST(
                sqlc.arg(base_duration_seconds)::float8 * power(2::float8, lockout_count::float8) * interval '1 second',
                sqlc.arg(max_duration_seconds)::float8 * interval '1 second'
            )
        ELSE locked_until
    END,
    updated_at = sqlc.arg(now)::timestamptz
WHERE id = sqlc.arg(id) AND deleted_at IS NULL
RETURNING *;

-- name: ResetLoginLockout :exec
-- Clears failed-attempt/lockout-escalation state on a successful login,
-- per RIZ-59's reset semantics ("counter and lockout-escalation reset on
-- successful login after expiry").
UPDATE users
SET
    failed_login_attempts = 0,
    lockout_count = 0,
    locked_until = NULL,
    updated_at = sqlc.arg(now)::timestamptz
WHERE id = sqlc.arg(id) AND deleted_at IS NULL;
