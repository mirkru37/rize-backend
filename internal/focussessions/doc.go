// Package focussessions implements the /v1/focus-sessions CRUD route
// group per documentation/api-reference.md §CRUD groups (RIZ-34).
//
// focus_sessions.device_id is NOT NULL per
// documentation/database-schema.md, so unlike projects/tags/categories,
// creating a focus session always requires a device — POST
// /v1/focus-sessions requires a device_id in the request body, resolved
// against the authenticated user via the same tenant-scoped
// GetDeviceByID lookup internal/sync's push path already uses (a
// device_id that exists but belongs to another user is indistinguishable
// from an unknown one, per documentation/security.md §Tenant Isolation).
// project_id is optional and, when present, is validated the same way via
// GetProjectByIDForUser (already defined in internal/store/queries/sync.sql
// for the push path and reused here).
package focussessions
