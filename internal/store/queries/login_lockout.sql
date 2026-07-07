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
-- capped at max_duration_seconds. The doubling/capping arithmetic is done
-- entirely in the seconds (float8) domain — LEAST() runs BEFORE the value
-- is ever converted to an interval — because converting the uncapped
-- doubled value to an interval first can overflow Postgres's interval
-- range once lockout_count gets large (observed at lockout_count >= 34
-- under concurrent-attempt bursts), which would make this UPDATE error
-- out and turn a login into a 500 instead of the standard 401 envelope —
-- itself a reachable no-oracle break.
--
-- Both the lockout_count escalation and the locked_until assignment are
-- additionally guarded on "not currently locked"
-- (locked_until IS NULL OR locked_until <= @now): without this guard,
-- concurrent failed attempts that all land after the row is already
-- locked (each serialized in turn by this UPDATE's row lock, but each
-- still satisfying "failed_login_attempts + 1 >= threshold" since the
-- counter only grows) would each re-escalate lockout_count once per
-- attempt rather than once per lockout episode, inflating the doubling
-- exponent far faster than one escalation per actual lockout — which is
-- exactly what fed the interval-overflow bug above.
--
-- @now is supplied by the caller (the service's injectable clock) rather
-- than Postgres's own now(), so the lockout window is deterministic and
-- testable under a fake clock.
UPDATE users
SET
    failed_login_attempts = failed_login_attempts + 1,
    lockout_count = CASE
        WHEN (locked_until IS NULL OR locked_until <= sqlc.arg(now)::timestamptz)
            AND failed_login_attempts + 1 >= sqlc.arg(threshold)::int THEN lockout_count + 1
        ELSE lockout_count
    END,
    locked_until = CASE
        WHEN (locked_until IS NULL OR locked_until <= sqlc.arg(now)::timestamptz)
            AND failed_login_attempts + 1 >= sqlc.arg(threshold)::int THEN
            sqlc.arg(now)::timestamptz + (
                LEAST(
                    sqlc.arg(base_duration_seconds)::float8 * power(2::float8, lockout_count::float8),
                    sqlc.arg(max_duration_seconds)::float8
                ) * interval '1 second'
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
