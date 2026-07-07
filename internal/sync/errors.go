package sync

import "errors"

// Sentinel errors returned by Service.Push for whole-request failures (as
// opposed to a single item's outcome, which is reported in-band as a
// per-item "invalid" result per documentation/sync-protocol.md §Push).
var (
	// ErrBatchTooLarge is returned when a push batch exceeds the 500-item
	// limit mandated by documentation/sync-protocol.md §Push ("A single
	// request MUST NOT contain more than 500 items").
	ErrBatchTooLarge = errors.New("sync: batch exceeds the 500-item limit")

	// ErrDeviceNotFound is returned when the request's device_id does not
	// resolve to a live device owned by the authenticated user — either
	// because it is malformed, unknown, revoked, or (per
	// documentation/security.md §Tenant Isolation) belongs to a different
	// user entirely. The request body's device_id is never trusted on its
	// own; it is only accepted once resolved against the authenticated
	// user_id from the access token.
	ErrDeviceNotFound = errors.New("sync: device not found")

	// ErrValidation is returned for a malformed request body at the
	// batch/envelope level (as opposed to a single item's payload, which is
	// reported as a per-item "invalid" result rather than failing the
	// request).
	ErrValidation = errors.New("sync: validation error")

	// ErrCursorExpired is returned by Service.pull (RIZ-72) when the
	// caller-supplied cursor is strictly below the persisted
	// sync_changelog retention horizon: the rows between that cursor and
	// the horizon have been pruned by age-based retention, so this page
	// can no longer be served without silently skipping changes. Per
	// documentation/sync-protocol.md §Device Restore from Backup, a client
	// that resets its cursor to empty and re-pulls from the beginning is
	// always safe (pulls are idempotent) — so the caller is told to do
	// exactly that, the same recovery path already defined for a lost or
	// stale local cursor, rather than being handed a page with an
	// undetectable gap in it. A first-ever pull (empty/zero cursor) never
	// triggers this: see store.PullCursor.IsZero.
	ErrCursorExpired = errors.New("sync: cursor expired, reset and re-pull from the beginning")
)
