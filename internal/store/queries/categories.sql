-- name: ListCategoriesForUser :many
-- GET /v1/categories per documentation/api-reference.md §CRUD groups.
-- documentation/database-schema.md's categories table: "user_id IS NULL
-- denotes a system default category available to every user; user_id set
-- denotes a user's own custom category" — so the list a user sees is the
-- union of every system default plus their own custom categories,
-- keyset-paginated together by server_seq.
SELECT * FROM categories
WHERE (user_id = $1 OR user_id IS NULL) AND deleted_at IS NULL AND server_seq > $2
ORDER BY server_seq ASC
LIMIT $3;

-- name: GetCategoryForUser :one
-- GET /v1/categories/{id}: readable if it's a system default or the
-- caller's own, per database-schema.md's categories.user_id semantics.
SELECT * FROM categories
WHERE id = $1 AND (user_id = $2 OR user_id IS NULL) AND deleted_at IS NULL;

-- name: GetOwnCategoryForUser :one
-- Authorization check used before UPDATE/DELETE: unlike GetCategoryForUser,
-- this only matches a category the caller owns (user_id = $2), never a
-- system default (user_id IS NULL). System categories are read-only via
-- the API — see internal/categories's service doc comment for why an
-- attempt to PATCH/DELETE one is reported as 404 rather than 403, matching
-- the cross-tenant 404-equivalence convention used elsewhere in this
-- ticket.
SELECT * FROM categories
WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL;

-- name: CreateCategoryForUser :one
-- POST /v1/categories always creates a user-owned category (user_id set to
-- the authenticated caller); a client cannot create a system default
-- through this endpoint. id is server-generated (gen_random_uuid()): per
-- documentation/database-schema.md's PK convention, categories are not in
-- the client-supplied-UUIDv7 list (only activity_events, focus_sessions,
-- projects, and tags are).
INSERT INTO categories (
    id, user_id, name, color, productivity, created_at, updated_at
) VALUES (
    gen_random_uuid(), $1, $2, $3, $4, now(), now()
)
RETURNING *;

-- name: UpdateOwnCategoryForUser :one
-- PATCH /v1/categories/{id}, restricted to categories the caller owns (see
-- GetOwnCategoryForUser); a system default cannot be edited through this
-- query (zero rows if id resolves to one).
UPDATE categories
SET name = $3,
    color = $4,
    productivity = $5,
    updated_at = now()
WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteOwnCategoryForUser :execrows
-- DELETE /v1/categories/{id}, restricted to categories the caller owns; a
-- system default cannot be deleted through this query (zero rows if id
-- resolves to one).
UPDATE categories
SET deleted_at = now(), updated_at = now()
WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL;
