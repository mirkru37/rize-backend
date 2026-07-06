-- name: CreateUser :one
-- Callers pass the desired role explicitly ('user' by default); the
-- database-side DEFAULT 'user' on this column only applies when the column
-- is omitted from an INSERT's column list, which sqlc's generated,
-- fully-columned INSERT never does.
INSERT INTO users (
    id, email, password_hash, apple_user_id, display_name, role, timezone,
    created_at, updated_at, server_seq
) VALUES (
    gen_random_uuid(), $1, $2, $3, $4, $5, $6,
    now(), now(), $7
)
RETURNING *;

-- name: GetUserByID :one
SELECT * FROM users
WHERE id = $1 AND deleted_at IS NULL;

-- name: GetUserByEmail :one
SELECT * FROM users
WHERE email = $1 AND deleted_at IS NULL;

-- name: GetUserByAppleUserID :one
SELECT * FROM users
WHERE apple_user_id = $1 AND deleted_at IS NULL;

-- name: UpdateUserProfile :one
UPDATE users
SET display_name = $2,
    timezone = $3,
    updated_at = now(),
    server_seq = $4
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteUser :exec
UPDATE users
SET deleted_at = now(),
    server_seq = $2
WHERE id = $1 AND deleted_at IS NULL;
