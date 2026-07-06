// Package categories implements the /v1/categories CRUD route group per
// documentation/api-reference.md §CRUD groups (RIZ-34).
//
// documentation/database-schema.md's categories table: "user_id IS NULL
// denotes a system default category available to every user; user_id set
// denotes a user's own custom category." This package follows that
// exactly:
//
//   - GET (list) and GET/{id} are readable for both system defaults and
//     the caller's own categories — a user needs to see the full set of
//     categories available to them, including the ones they didn't
//     create.
//   - POST always creates a user-owned category (user_id = the
//     authenticated caller); there is no way to create a system default
//     through this API.
//   - PATCH/{id} and DELETE/{id} only ever succeed against a category the
//     caller owns. Attempting either against a system default (or another
//     user's category, or an id that doesn't exist at all) reports the
//     same 404 Not Found — system categories are effectively read-only via
//     this API, and (per documentation/security.md §Tenant Isolation's
//     404-equivalence convention used throughout this ticket) a caller
//     cannot distinguish "that category is a read-only system default"
//     from "that category doesn't belong to you" from "that category
//     doesn't exist."
package categories
