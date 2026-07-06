package sync

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/mirkru37/rize-backend/internal/store"
	"github.com/mirkru37/rize-backend/internal/store/storedb"
)

// defaultPullLimit and maxPullLimit bound the page size
// documentation/sync-protocol.md §Pull leaves as a caller-supplied,
// undocumented-default `limit`. No numeric default/ceiling is specified in
// the docs (api-reference.md's Conventions section only says "callers pass
// limit ... the server returns the page of results"), so this package
// picks a default of 200 and caps at 500 — the same 500 ceiling
// sync-protocol.md's push side already uses as its batch-size limit — to
// keep a single page's response size bounded. This is documented here as
// an assumption rather than a doc contract.
const (
	defaultPullLimit = 200
	maxPullLimit     = 500
)

// pullEntityTypes lists the entity types this pull implementation
// populates in the "changes" object, in the fixed order documentation/
// database-schema.md's "server_seq is present on every syncable table"
// convention establishes, and MUST match migration 000026's set of tables
// with a *_log_change trigger writing into sync_changelog — every
// entity_type value ListChangelogPage can return has an entry here (see
// resolveChangelogEntry's default case, which errors on anything else).
//
// "categories" is included per the RIZ-34 follow-up that reconciled
// sync-protocol.md's Pull worked example with database-schema.md's
// Conventions section (see ListCategoryChangesForUser's — now
// GetCategoryForChangelogEntry's — doc comment for the delivered-rows
// scoping: system defaults plus the caller's own categories).
//
// "aggregates" (server-derived rollups) is still omitted: no aggregation
// service exists yet in this codebase (internal/reports is an empty
// package stub) to source it from — out of scope for this ticket.
var pullEntityTypes = []string{
	"activity_events",
	"focus_sessions",
	"projects",
	"tags",
	"user_app_settings",
	"categories",
}

// changeSet is the wire shape of one entity type's page within
// GET /v1/sync/changes's "changes" object.
type changeSet struct {
	Upserts    []any `json:"upserts"`
	Tombstones []any `json:"tombstones"`
}

// pullResponse mirrors documentation/sync-protocol.md §Pull's response
// schema.
type pullResponse struct {
	Changes    map[string]changeSet `json:"changes"`
	NextCursor string               `json:"next_cursor"`
	HasMore    bool                 `json:"has_more"`
}

// activityEventUpsertDTO / activityEventTombstoneDTO mirror
// documentation/sync-protocol.md §Pull's activity_events upsert/tombstone
// shape.
type activityEventUpsertDTO struct {
	EventID     string  `json:"event_id"`
	StartedAt   string  `json:"started_at"`
	EndedAt     string  `json:"ended_at"`
	AppBundleID *string `json:"app_bundle_id,omitempty"`
	Category    *string `json:"category,omitempty"`
	Precision   string  `json:"precision"`
	ServerSeq   int64   `json:"server_seq"`
}

type activityEventTombstoneDTO struct {
	EventID   string `json:"event_id"`
	ServerSeq int64  `json:"server_seq"`
}

// focusSessionUpsertDTO / focusSessionTombstoneDTO: the doc's pull example
// elides the focus_sessions upsert/tombstone shape
// (`"focus_sessions": { "upserts": [ /* ... */ ] }`), so this mirrors the
// field set documentation/sync-protocol.md §Push's focus_session data
// object already defines for the same entity, which is the closest
// documented shape available.
type focusSessionUpsertDTO struct {
	ID               string  `json:"id"`
	UpdatedAt        string  `json:"updated_at"`
	StartedAt        string  `json:"started_at"`
	EndedAt          *string `json:"ended_at,omitempty"`
	ProjectID        *string `json:"project_id,omitempty"`
	Kind             string  `json:"kind"`
	Status           string  `json:"status"`
	PlannedDurationS *int32  `json:"planned_duration_s,omitempty"`
	Note             *string `json:"note,omitempty"`
	ServerSeq        int64   `json:"server_seq"`
}

type focusSessionTombstoneDTO struct {
	ID        string `json:"id"`
	ServerSeq int64  `json:"server_seq"`
}

type projectUpsertDTO struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Color      string  `json:"color"`
	ArchivedAt *string `json:"archived_at,omitempty"`
	UpdatedAt  string  `json:"updated_at"`
	ServerSeq  int64   `json:"server_seq"`
}

type projectTombstoneDTO struct {
	ID        string `json:"id"`
	ServerSeq int64  `json:"server_seq"`
}

type tagUpsertDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	UpdatedAt string `json:"updated_at"`
	ServerSeq int64  `json:"server_seq"`
}

type tagTombstoneDTO struct {
	ID        string `json:"id"`
	ServerSeq int64  `json:"server_seq"`
}

// userAppSettingUpsertDTO: there is no tombstone counterpart —
// user_app_settings has no deleted_at/deleted column (see
// documentation/database-schema.md), so its changeSet.Tombstones is always
// an empty slice.
type userAppSettingUpsertDTO struct {
	AppID      string  `json:"app_id"`
	CategoryID *string `json:"category_id,omitempty"`
	Excluded   bool    `json:"excluded"`
	UpdatedAt  string  `json:"updated_at"`
	ServerSeq  int64   `json:"server_seq"`
}

// categoryUpsertDTO / categoryTombstoneDTO mirror the field set
// internal/categories's CRUD DTOs already expose for a category row
// (documentation/api-reference.md §CRUD groups), plus server_seq. UserID is
// omitted from the wire shape: sync-protocol.md's other per-user entities
// (projects, tags, ...) don't echo user_id either since it's implicit in
// "this is the authenticated caller's pull stream" — a system default
// category (user_id IS NULL) has no owner to report anyway.
type categoryUpsertDTO struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Color        string `json:"color"`
	Productivity int16  `json:"productivity"`
	UpdatedAt    string `json:"updated_at"`
	ServerSeq    int64  `json:"server_seq"`
}

type categoryTombstoneDTO struct {
	ID        string `json:"id"`
	ServerSeq int64  `json:"server_seq"`
}

func formatTs(ts pgtype.Timestamptz) string {
	if !ts.Valid {
		return ""
	}
	return ts.Time.UTC().Format(time.RFC3339)
}

func formatOptionalTs(ts pgtype.Timestamptz) *string {
	if !ts.Valid {
		return nil
	}
	s := ts.Time.UTC().Format(time.RFC3339)
	return &s
}

func formatOptionalUUID(id pgtype.UUID) *string {
	if !id.Valid {
		return nil
	}
	s := id.String()
	return &s
}

// nonNilSlice returns an empty (rather than nil) []any, so the JSON
// encoding is always "[]" and never "null" for an entity type with no
// changes in this page.
func nonNilSlice(items []any) []any {
	if items == nil {
		return []any{}
	}
	return items
}

// Beginner starts a transaction with explicit options. *pgxpool.Pool
// satisfies this interface (its BeginTx method). It is narrowed to just
// BeginTx (rather than depending on the full pool) per this codebase's
// existing internal/auth.Beginner convention.
type Beginner interface {
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
}

// pullSnapshotTxOptions is the isolation level a pull's changelog-page
// query and its per-entity resolve lookups all share: REPEATABLE READ so
// every one of them observes one fixed MVCC snapshot (and therefore one
// fixed xid_before_snapshot_horizon horizon, see migration 000024) instead
// of each being its own READ COMMITTED statement with a potentially
// different snapshot — and, just as importantly, so a resolve lookup
// against an entity table always sees the exact same committed state the
// changelog page it's resolving was itself read from. ReadOnly since a
// pull never writes.
var pullSnapshotTxOptions = pgx.TxOptions{
	IsoLevel:   pgx.RepeatableRead,
	AccessMode: pgx.ReadOnly,
}

// runInPullSnapshot runs fn with a storedb.Querier scoped to a single
// REPEATABLE READ, READ ONLY transaction when s.Pool is configured (always
// true in production; see cmd/api/main.go's wiring), so every query fn
// issues sees the same MVCC snapshot per this file's H1 fix.
//
// When s.Pool is nil (only expected for a Service wired without a real
// *pgxpool.Pool, e.g. a hand-built unit-test fixture that doesn't exercise
// this fix), fn instead runs directly against s.Queries with no shared
// transaction — the same non-transactional fallback internal/auth.Service
// already uses for its Pool-optional operations.
func (s *Service) runInPullSnapshot(ctx context.Context, fn func(q storedb.Querier) error) error {
	if s.Pool == nil {
		return fn(s.Queries)
	}

	tx, err := s.Pool.BeginTx(ctx, pullSnapshotTxOptions)
	if err != nil {
		return fmt.Errorf("sync: begin pull snapshot transaction: %w", err)
	}
	// A pull never writes, so there's nothing to commit; rolling back is
	// enough to release the snapshot/transaction either way (including on
	// the success path, once fn has already read everything it needs).
	defer func() { _ = tx.Rollback(ctx) }()

	return fn(storedb.New(tx))
}

// changelogEntry is one deduplicated, resolved row destined for the
// response: exactly one of upsert/tombstone is set (never both), matching
// resolveChangelogEntry's contract.
type changelogEntry struct {
	entityType string
	upsert     any
	tombstone  any
}

// pull implements GET /v1/sync/changes for userID (from the authenticated
// access token — never a query/body parameter, per
// documentation/security.md §Tenant Isolation), per
// documentation/sync-protocol.md §Pull.
//
// RIZ-34 (pivot): unlike the prior design, which paginated each of the six
// syncable tables independently and directly by
// (xmin_xid8(that table's own xmin), server_seq) — fatal for
// activity_events, a compressed TimescaleDB hypertable that rejects any
// xmin reference against a compressed chunk (migration 000024/000025's
// approach broke permanently the first time a chunk crossed the 30-day
// compression policy) — this implementation paginates a SINGLE feed,
// sync_changelog (migration 000026): one append-only, never-hypertable,
// never-compressed outbox row per write to any of the six syncable tables,
// appended by a same-transaction trigger so the changelog row's own xmin
// (not the entity table's) carries the commit-ordering property the pull
// cursor depends on.
//
// One query (ListChangelogPage) fetches up to limit+1 raw changelog rows
// keyset-paginated by (xid8, server_seq) scoped to userID (or NULL, for
// system-category rows); limit+1 lets this method detect "more rows exist
// beyond this page" without a second round trip. next_cursor is simply the
// last raw changelog row's (xid8, server_seq) tuple BEFORE dedup — i.e. the
// literal boundary of what ListChangelogPage returned — so a re-pull at
// that exact cursor can never re-scan a changelog row already consumed,
// and (per migration 000025's gap-free-by-construction invariant, which
// applies identically here since it only depends on the tuple being
// unique and monotonic in commit order) can never skip one either. This
// also removes the prior design's per-entity-type
// min-pending/max-delivered aggregation entirely: there is exactly one
// feed and one cursor now.
//
// Multiple changelog rows can name the same entity within one page (e.g.
// an activity_event created then tombstoned, or a project updated twice)
// — this method dedupes by (entity_type, entity_id), keeping only the
// LATEST occurrence in page order, then resolves each surviving entity
// exactly once to its CURRENT state via a point lookup (GetProjectFor-
// ChangelogEntry and friends) rather than replaying the stale intermediate
// row the changelog entry itself carries no payload for. A resolve lookup
// that finds zero rows (the entity vanished entirely) is skipped rather
// than erroring: this codebase has no hard-delete path for any syncable
// table today (deletes are soft, via deleted_at/deleted, which a resolve
// lookup finds and reports as a tombstone, not zero rows), so an
// unresolvable entity_id can only mean a future, currently nonexistent
// hard-delete path — silently omitting it from this page is safe because
// the client's local state for that id is simply never updated again,
// which is the same externally-observable outcome a hard delete without
// any tombstone semantics would produce anyway.
//
// Every query (the changelog page and every per-entity resolve lookup)
// runs inside a single REPEATABLE READ, READ ONLY transaction (see
// runInPullSnapshot), so they all see the exact same MVCC snapshot.
func (s *Service) pull(ctx context.Context, userID, rawCursor string, rawLimit int) (pullResponse, error) {
	uid, err := parseUUID(userID)
	if err != nil {
		return pullResponse{}, fmt.Errorf("%w: invalid authenticated user id", ErrValidation)
	}

	cursor, err := store.DecodePullCursor(rawCursor)
	if err != nil {
		return pullResponse{}, fmt.Errorf("%w: invalid cursor", ErrValidation)
	}

	limit := rawLimit
	if limit <= 0 {
		limit = defaultPullLimit
	}
	if limit > maxPullLimit {
		limit = maxPullLimit
	}

	var (
		hasMore bool
		nextPos = cursor
		ordered []string
		byKey   = map[string]changelogEntry{}
	)

	err = s.runInPullSnapshot(ctx, func(q storedb.Querier) error {
		rows, err := q.ListChangelogPage(ctx, storedb.ListChangelogPageParams{
			UserID:     uid,
			CursorXid8: pgtype.Uint64{Uint64: cursor.Xid8, Valid: true},
			CursorSeq:  cursor.ServerSeq,
			PageLimit:  store.LimitParam(limit + 1),
		})
		if err != nil {
			return fmt.Errorf("sync: list changelog page: %w", err)
		}

		hasMore = len(rows) > limit
		if hasMore {
			rows = rows[:limit]
		}
		if len(rows) > 0 {
			last := rows[len(rows)-1]
			nextPos = store.PullCursor{Xid8: last.Xid8.Uint64, ServerSeq: last.ServerSeq}
		}

		// Dedupe by (entity_type, entity_id), keeping the latest occurrence
		// (rows are already in ascending (xid8, server_seq) order, so a
		// later loop iteration always overwrites an earlier one for the
		// same key) while preserving first-seen order for a deterministic
		// response.
		for _, row := range rows {
			key := row.EntityType + ":" + row.EntityID.String()
			if _, seen := byKey[key]; !seen {
				ordered = append(ordered, key)
			}
			resolved, found, err := resolveChangelogEntry(ctx, q, uid, row)
			if err != nil {
				return fmt.Errorf("sync: resolve changelog entry (%s %s): %w", row.EntityType, row.EntityID.String(), err)
			}
			if !found {
				// See this method's doc comment: the entity vanished
				// entirely (no hard-delete path exists today, so this is
				// not expected in steady state, but is safe to skip).
				delete(byKey, key)
				continue
			}
			byKey[key] = resolved
		}
		return nil
	})
	if err != nil {
		return pullResponse{}, err
	}

	changes := make(map[string]changeSet, len(pullEntityTypes))
	upserts := make(map[string][]any, len(pullEntityTypes))
	tombstones := make(map[string][]any, len(pullEntityTypes))
	for _, key := range ordered {
		entry, ok := byKey[key]
		if !ok {
			// Deleted from byKey above (resolve found zero rows).
			continue
		}
		if entry.tombstone != nil {
			tombstones[entry.entityType] = append(tombstones[entry.entityType], entry.tombstone)
			continue
		}
		upserts[entry.entityType] = append(upserts[entry.entityType], entry.upsert)
	}
	for _, entityType := range pullEntityTypes {
		changes[entityType] = changeSet{
			Upserts:    nonNilSlice(upserts[entityType]),
			Tombstones: nonNilSlice(tombstones[entityType]),
		}
	}

	return pullResponse{
		Changes:    changes,
		NextCursor: store.EncodePullCursor(nextPos),
		HasMore:    hasMore,
	}, nil
}

// resolveChangelogEntry resolves one sync_changelog row to its entity's
// CURRENT state (an upsert or a tombstone), per pull's doc comment. found
// is false when the point lookup returns zero rows (see pull's doc comment
// for why that's safe to treat as "omit this entity from the page").
func resolveChangelogEntry(ctx context.Context, q storedb.Querier, uid pgtype.UUID, row storedb.ListChangelogPageRow) (changelogEntry, bool, error) {
	switch row.EntityType {
	case "activity_events":
		r, err := q.GetActivityEventForChangelogEntry(ctx, storedb.GetActivityEventForChangelogEntryParams{
			UserID:    uid,
			EventID:   row.EntityID,
			StartedAt: row.EventStartedAt,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return changelogEntry{}, false, nil
		}
		if err != nil {
			return changelogEntry{}, false, err
		}
		if r.Deleted {
			return changelogEntry{
				entityType: row.EntityType,
				tombstone: activityEventTombstoneDTO{
					EventID:   r.EventID.String(),
					ServerSeq: r.ServerSeq,
				},
			}, true, nil
		}
		return changelogEntry{
			entityType: row.EntityType,
			upsert: activityEventUpsertDTO{
				EventID:     r.EventID.String(),
				StartedAt:   formatTs(r.StartedAt),
				EndedAt:     formatTs(r.EndedAt),
				AppBundleID: r.AppBundleID,
				Category:    r.CategoryName,
				Precision:   r.Precision,
				ServerSeq:   r.ServerSeq,
			},
		}, true, nil

	case "focus_sessions":
		r, err := q.GetFocusSessionForChangelogEntry(ctx, storedb.GetFocusSessionForChangelogEntryParams{
			UserID: uid,
			ID:     row.EntityID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return changelogEntry{}, false, nil
		}
		if err != nil {
			return changelogEntry{}, false, err
		}
		if r.DeletedAt.Valid {
			return changelogEntry{
				entityType: row.EntityType,
				tombstone: focusSessionTombstoneDTO{
					ID:        r.ID.String(),
					ServerSeq: r.ServerSeq,
				},
			}, true, nil
		}
		return changelogEntry{
			entityType: row.EntityType,
			upsert: focusSessionUpsertDTO{
				ID:               r.ID.String(),
				UpdatedAt:        formatTs(r.UpdatedAt),
				StartedAt:        formatTs(r.StartedAt),
				EndedAt:          formatOptionalTs(r.EndedAt),
				ProjectID:        formatOptionalUUID(r.ProjectID),
				Kind:             r.Kind,
				Status:           r.Status,
				PlannedDurationS: r.PlannedDurationS,
				Note:             r.Note,
				ServerSeq:        r.ServerSeq,
			},
		}, true, nil

	case "projects":
		r, err := q.GetProjectForChangelogEntry(ctx, storedb.GetProjectForChangelogEntryParams{
			UserID: uid,
			ID:     row.EntityID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return changelogEntry{}, false, nil
		}
		if err != nil {
			return changelogEntry{}, false, err
		}
		if r.DeletedAt.Valid {
			return changelogEntry{
				entityType: row.EntityType,
				tombstone: projectTombstoneDTO{
					ID:        r.ID.String(),
					ServerSeq: r.ServerSeq,
				},
			}, true, nil
		}
		return changelogEntry{
			entityType: row.EntityType,
			upsert: projectUpsertDTO{
				ID:         r.ID.String(),
				Name:       r.Name,
				Color:      r.Color,
				ArchivedAt: formatOptionalTs(r.ArchivedAt),
				UpdatedAt:  formatTs(r.UpdatedAt),
				ServerSeq:  r.ServerSeq,
			},
		}, true, nil

	case "tags":
		r, err := q.GetTagForChangelogEntry(ctx, storedb.GetTagForChangelogEntryParams{
			UserID: uid,
			ID:     row.EntityID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return changelogEntry{}, false, nil
		}
		if err != nil {
			return changelogEntry{}, false, err
		}
		if r.DeletedAt.Valid {
			return changelogEntry{
				entityType: row.EntityType,
				tombstone: tagTombstoneDTO{
					ID:        r.ID.String(),
					ServerSeq: r.ServerSeq,
				},
			}, true, nil
		}
		return changelogEntry{
			entityType: row.EntityType,
			upsert: tagUpsertDTO{
				ID:        r.ID.String(),
				Name:      r.Name,
				UpdatedAt: formatTs(r.UpdatedAt),
				ServerSeq: r.ServerSeq,
			},
		}, true, nil

	case "user_app_settings":
		// entity_id on the changelog row is app_id (see migration 000026's
		// comment); no tombstone path (no deleted_at/deleted column).
		r, err := q.GetUserAppSettingForChangelogEntry(ctx, storedb.GetUserAppSettingForChangelogEntryParams{
			UserID: uid,
			AppID:  row.EntityID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return changelogEntry{}, false, nil
		}
		if err != nil {
			return changelogEntry{}, false, err
		}
		return changelogEntry{
			entityType: row.EntityType,
			upsert: userAppSettingUpsertDTO{
				AppID:      r.AppID.String(),
				CategoryID: formatOptionalUUID(r.CategoryID),
				Excluded:   r.Excluded,
				UpdatedAt:  formatTs(r.UpdatedAt),
				ServerSeq:  r.ServerSeq,
			},
		}, true, nil

	case "categories":
		r, err := q.GetCategoryForChangelogEntry(ctx, storedb.GetCategoryForChangelogEntryParams{
			ID:     row.EntityID,
			UserID: uid,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return changelogEntry{}, false, nil
		}
		if err != nil {
			return changelogEntry{}, false, err
		}
		if r.DeletedAt.Valid {
			return changelogEntry{
				entityType: row.EntityType,
				tombstone: categoryTombstoneDTO{
					ID:        r.ID.String(),
					ServerSeq: r.ServerSeq,
				},
			}, true, nil
		}
		return changelogEntry{
			entityType: row.EntityType,
			upsert: categoryUpsertDTO{
				ID:           r.ID.String(),
				Name:         r.Name,
				Color:        r.Color,
				Productivity: r.Productivity,
				UpdatedAt:    formatTs(r.UpdatedAt),
				ServerSeq:    r.ServerSeq,
			},
		}, true, nil

	default:
		// Should never happen: every entity_type value sync_changelog can
		// contain comes from one of migration 000026's six *_log_change
		// triggers, all listed in pullEntityTypes above.
		return changelogEntry{}, false, fmt.Errorf("sync: unknown changelog entity_type %q", row.EntityType)
	}
}
