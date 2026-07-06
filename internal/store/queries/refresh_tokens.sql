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

-- name: ListActiveRefreshTokensByUser :many
SELECT * FROM refresh_tokens
WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > now()
ORDER BY issued_at DESC;
