// Package projects implements the /v1/projects CRUD route group per
// documentation/api-reference.md §CRUD groups
// (RIZ-34): GET (list, cursor-paginated), POST (create), GET/{id},
// PATCH/{id}, DELETE/{id} (soft delete). Every operation is scoped by the
// authenticated user_id from the access token, per
// documentation/security.md §Tenant Isolation.
//
// Layering follows rize-backend/CLAUDE.md: Handler decodes/validates shape
// and encodes responses; Service holds the business logic (tenant scoping,
// partial-update merge, constraint-violation mapping) and is the only
// caller of the sqlc-generated storedb.Querier.
package projects
