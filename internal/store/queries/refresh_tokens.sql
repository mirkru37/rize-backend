-- name: CreateRefreshToken :one
INSERT INTO refresh_tokens (
    id, user_id, device_id, token_hash, family_id, issued_at, expires_at
) VALUES (
    gen_random_uuid(), $1, $2, $3, $4, now(), $5
)
RETURNING *;

-- name: GetRefreshTokenByHash :one
SELECT * FROM refresh_tokens
WHERE token_hash = $1;

-- name: RotateRefreshToken :one
UPDATE refresh_tokens
SET revoked_at = now(),
    replaced_by = $2
WHERE id = $1 AND revoked_at IS NULL
RETURNING *;

-- name: RevokeRefreshTokenFamily :exec
UPDATE refresh_tokens
SET revoked_at = now()
WHERE family_id = $1 AND revoked_at IS NULL;

-- name: RevokeRefreshTokenFamilyForUser :exec
-- Scoped by user_id per documentation/security.md §Tenant Isolation: used by
-- logout, where the caller is already authenticated and the family being
-- revoked must belong to them.
UPDATE refresh_tokens
SET revoked_at = now()
WHERE family_id = $1 AND user_id = $2 AND revoked_at IS NULL;

-- name: RevokeRefreshTokensByDevice :exec
-- Scoped by user_id per documentation/security.md §Tenant Isolation. Used by
-- DELETE /v1/devices/{id}, which must revoke every refresh token ever issued
-- to that device (not just the currently active family) per
-- documentation/security.md §Token model.
UPDATE refresh_tokens
SET revoked_at = now()
WHERE device_id = $1 AND user_id = $2 AND revoked_at IS NULL;

-- name: ListActiveRefreshTokensByUser :many
SELECT * FROM refresh_tokens
WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > now()
ORDER BY issued_at DESC;
