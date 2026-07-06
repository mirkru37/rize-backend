-- name: CreateDevice :one
INSERT INTO devices (
    id, user_id, platform, name, model, os_version, app_version,
    last_seen_at, created_at
) VALUES (
    gen_random_uuid(), $1, $2, $3, $4, $5, $6,
    now(), now()
)
RETURNING *;

-- name: GetDeviceByID :one
-- Scoped by user_id per documentation/security.md §Tenant Isolation: every
-- query is scoped by user_id from the access token, so a request
-- authenticated as one user can never read another user's device row.
SELECT * FROM devices
WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL;

-- name: ListDevicesByUser :many
SELECT * FROM devices
WHERE user_id = $1 AND revoked_at IS NULL
ORDER BY created_at DESC;

-- name: TouchDeviceLastSeen :exec
-- Scoped by user_id per documentation/security.md §Tenant Isolation.
UPDATE devices
SET last_seen_at = now()
WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL;

-- name: RevokeDevice :exec
-- Scoped by user_id per documentation/security.md §Tenant Isolation.
UPDATE devices
SET revoked_at = now()
WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL;
