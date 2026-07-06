-- name: CreateTagForUser :one
-- Creates a tag owned by the authenticated user, per
-- documentation/api-reference.md §CRUD groups ("POST /v1/tags"). A
-- concurrent/duplicate name for the same user surfaces as a unique
-- violation on tags' UNIQUE (user_id, name) constraint, mapped by the
-- caller via store.ConstraintViolation to a 409 Conflict.
INSERT INTO tags (
    id, user_id, name, updated_at
) VALUES (
    $1, $2, $3, now()
)
RETURNING *;

-- name: ListTagsForUser :many
-- Keyset-paginated list for GET /v1/tags, per
-- documentation/api-reference.md §Conventions. Scoped by user_id per
-- documentation/security.md §Tenant Isolation; excludes soft-deleted rows.
SELECT * FROM tags
WHERE user_id = $1 AND deleted_at IS NULL AND server_seq > $2
ORDER BY server_seq ASC
LIMIT $3;

-- name: GetTagForUser :one
-- Tenant-scoped lookup for GET /v1/tags/{id}. Zero rows for either "no
-- such tag" or "belongs to another user," per
-- documentation/security.md §Tenant Isolation, so the two cases are
-- reported identically as 404.
SELECT * FROM tags
WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL;

-- name: UpdateTagForUser :one
-- PATCH /v1/tags/{id}. See projects.sql's UpdateProjectForUser doc comment
-- for the read-then-merge-then-write pattern and the updated_at rationale.
UPDATE tags
SET name = $3,
    updated_at = now()
WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteTagForUser :execrows
-- DELETE /v1/tags/{id}. See projects.sql's SoftDeleteProjectForUser doc
-- comment for the soft-delete/tombstone and 404-equivalence rationale.
UPDATE tags
SET deleted_at = now(), updated_at = now()
WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL;
