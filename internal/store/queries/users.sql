-- name: CreateUser :one
-- Callers pass the desired role explicitly ('user' by default); the
-- database-side DEFAULT 'user' on this column only applies when the column
-- is omitted from an INSERT's column list, which sqlc's generated,
-- fully-columned INSERT never does.
--
-- server_seq is intentionally omitted from the column list so the
-- table-level DEFAULT nextval('server_seq_global') assigns it, keeping
-- every syncable table's server_seq drawn from the same global sequence
-- space per documentation/sync-protocol.md.
INSERT INTO users (
    id, email, password_hash, apple_user_id, display_name, role, timezone,
    created_at, updated_at
) VALUES (
    gen_random_uuid(), $1, $2, $3, $4, $5, $6,
    now(), now()
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
-- server_seq is bumped from the same global sequence used by inserts
-- (table DEFAULT only applies to INSERTs, so UPDATEs draw from it
-- explicitly) per documentation/sync-protocol.md.
UPDATE users
SET display_name = $2,
    timezone = $3,
    updated_at = now(),
    server_seq = nextval('server_seq_global')
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteUser :exec
UPDATE users
SET deleted_at = now(),
    server_seq = nextval('server_seq_global')
WHERE id = $1 AND deleted_at IS NULL;
