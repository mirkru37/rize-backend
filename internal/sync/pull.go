package sync

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/mirkru37/rize-backend/internal/store"
	"github.com/mirkru37/rize-backend/internal/store/storedb"
)

// defaultPullLimit and maxPullLimit bound the per-entity-type page size
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
// populates in the "changes" object, in the fixed order they're written to
// the response.
//
// documentation/sync-protocol.md's §Pull worked example's "changes" object
// includes six keys: activity_events, focus_sessions, projects, tags,
// user_app_settings, and aggregates. "categories" is not one of the six
// keys shown there, YET documentation/database-schema.md's Conventions
// section explicitly states: "server_seq is present on every syncable
// table, including users and categories: a display-name change or a
// custom-category edit propagates to other devices via the same
// keyset-pagination mechanism as user_app_settings, projects, tags,
// activity_events, and focus_sessions" — which asserts categories ARE
// pulled the same way.
//
// That contradiction has since been resolved (RIZ-34 follow-up): category
// rows ARE syncable per database-schema.md, so "categories" is included
// below; sync-protocol.md's Entity Classes table / Pull worked example are
// being reconciled to list it explicitly in a parallel documentation pass.
// See ListCategoryChangesForUser's doc comment for the delivered-rows
// scoping (system defaults + the caller's own categories).
//
// "aggregates" (server-derived rollups) is still omitted: no aggregation
// service exists yet in this codebase (internal/reports is an empty
// package stub) to source it from, and RIZ-34's brief scopes this ticket
// to "sync pull + CRUD route groups," not implementing the reports/
// aggregation epic. Computing and exposing aggregates via this endpoint is
// out of scope here and tracked separately.
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

// pageResult is the outcome of fetching one entity type's page: the
// upserts/tombstones to write into the response, whether more rows exist
// beyond this page (hasMore), and lastPos, the (xid8, server_seq) tuple
// boundary this entity type's page reached (or the incoming cursor,
// unchanged, if this entity type had zero rows to return). See
// migration 000025 and store.PullCursor for why the boundary is a tuple
// rather than a bare server_seq.
type pageResult struct {
	upserts    []any
	tombstones []any
	hasMore    bool
	lastPos    store.PullCursor
}

// pullCursorLess reports whether a is strictly before b in the tuple order
// (xid8, server_seq) that migration 000025's keyset pagination uses.
func pullCursorLess(a, b store.PullCursor) bool {
	if a.Xid8 != b.Xid8 {
		return a.Xid8 < b.Xid8
	}
	return a.ServerSeq < b.ServerSeq
}

// pull implements GET /v1/sync/changes for userID (from the authenticated
// access token — never a query/body parameter, per
// documentation/security.md §Tenant Isolation), per
// documentation/sync-protocol.md §Pull.
//
// Every entity type in pullEntityTypes is queried independently for up to
// limit+1 rows keyset-paginated by the tuple (xid8, server_seq) — NOT
// server_seq alone, see migration 000025 and store.PullCursor — scoped to
// userID; limit+1 lets this method detect "more rows exist beyond this
// page" without a second round trip. The combined next_cursor is the
// minimum (in tuple order) of the per-type boundaries among types that
// still have more rows pending (so no row is ever skipped), or the maximum
// boundary across all types when every type is fully drained (so the
// cursor still advances as far as safely possible). Because pulls are
// idempotent (documentation/sync-protocol.md: "requesting the same cursor
// twice returns the same page, and applying that page twice is a
// no-op"), this conservative boundary can cause a small amount of
// redundant redelivery across page boundaries for an already-drained
// entity type, which is explicitly safe per that guarantee — it never
// causes a skip.
//
// All six per-entity-type queries run inside a single REPEATABLE READ,
// READ ONLY transaction (see runInPullSnapshot), so every one of them sees
// the exact same MVCC snapshot — and therefore the exact same
// pg_snapshot_xmin/xmax horizon each query's xid8/xid_before_snapshot_horizon
// predicates (migrations 000024/000025) gate and order on. Without a
// shared snapshot, two queries issued as separate READ COMMITTED
// statements could each observe a different horizon and a different
// widened xid8 for the same physical xmin, reopening the same "advance
// next_cursor past a row that turns out to still be uncommitted" gap this
// fix closes. See migration 000025's comment for the full gap-free-by-
// construction invariant that lets next_cursor safely advance only over
// rows the horizon gate excludes, now anchored to the same tuple used for
// ordering.
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

	results := make(map[string]pageResult, len(pullEntityTypes))

	err = s.runInPullSnapshot(ctx, func(q storedb.Querier) error {
		aeResult, err := pullActivityEvents(ctx, q, uid, cursor, limit)
		if err != nil {
			return fmt.Errorf("sync: pull activity_events: %w", err)
		}
		results["activity_events"] = aeResult

		fsResult, err := pullFocusSessions(ctx, q, uid, cursor, limit)
		if err != nil {
			return fmt.Errorf("sync: pull focus_sessions: %w", err)
		}
		results["focus_sessions"] = fsResult

		prResult, err := pullProjects(ctx, q, uid, cursor, limit)
		if err != nil {
			return fmt.Errorf("sync: pull projects: %w", err)
		}
		results["projects"] = prResult

		tgResult, err := pullTags(ctx, q, uid, cursor, limit)
		if err != nil {
			return fmt.Errorf("sync: pull tags: %w", err)
		}
		results["tags"] = tgResult

		uasResult, err := pullUserAppSettings(ctx, q, uid, cursor, limit)
		if err != nil {
			return fmt.Errorf("sync: pull user_app_settings: %w", err)
		}
		results["user_app_settings"] = uasResult

		catResult, err := pullCategories(ctx, q, uid, cursor, limit)
		if err != nil {
			return fmt.Errorf("sync: pull categories: %w", err)
		}
		results["categories"] = catResult

		return nil
	})
	if err != nil {
		return pullResponse{}, err
	}

	hasMore := false
	var minPendingPos store.PullCursor
	havePendingPos := false
	maxPos := cursor
	for _, r := range results {
		if pullCursorLess(maxPos, r.lastPos) {
			maxPos = r.lastPos
		}
		if r.hasMore {
			hasMore = true
			if !havePendingPos || pullCursorLess(r.lastPos, minPendingPos) {
				minPendingPos = r.lastPos
				havePendingPos = true
			}
		}
	}

	nextPos := maxPos
	if hasMore {
		nextPos = minPendingPos
	}

	changes := make(map[string]changeSet, len(pullEntityTypes))
	for _, entityType := range pullEntityTypes {
		r := results[entityType]
		changes[entityType] = changeSet{
			Upserts:    nonNilSlice(r.upserts),
			Tombstones: nonNilSlice(r.tombstones),
		}
	}

	return pullResponse{
		Changes:    changes,
		NextCursor: store.EncodePullCursor(nextPos),
		HasMore:    hasMore,
	}, nil
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

// pullSnapshotTxOptions is the isolation level every pull's per-entity-type
// queries share: REPEATABLE READ so all of them observe one fixed MVCC
// snapshot (and therefore one fixed xid_before_snapshot_horizon horizon,
// see migration 000024) instead of each being its own READ COMMITTED
// statement with a potentially different snapshot. ReadOnly since a pull
// never writes.
var pullSnapshotTxOptions = pgx.TxOptions{
	IsoLevel:   pgx.RepeatableRead,
	AccessMode: pgx.ReadOnly,
}

// runInPullSnapshot runs fn with a storedb.Querier scoped to a single
// REPEATABLE READ, READ ONLY transaction when s.Pool is configured (always
// true in production; see cmd/api/main.go's wiring), so every per-entity-
// type query fn issues sees the same MVCC snapshot per this file's H1 fix.
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

func pullActivityEvents(ctx context.Context, q storedb.Querier, uid pgtype.UUID, cursor store.PullCursor, limit int) (pageResult, error) {
	rows, err := q.ListActivityEventChangesForUser(ctx, storedb.ListActivityEventChangesForUserParams{
		UserID:     uid,
		CursorXid8: pgtype.Uint64{Uint64: cursor.Xid8, Valid: true},
		CursorSeq:  cursor.ServerSeq,
		PageLimit:  store.LimitParam(limit + 1),
	})
	if err != nil {
		return pageResult{}, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	result := pageResult{hasMore: hasMore, lastPos: cursor}
	for _, row := range rows {
		result.lastPos = store.PullCursor{Xid8: row.Xid8.Uint64, ServerSeq: row.ServerSeq}
		if row.Deleted {
			result.tombstones = append(result.tombstones, activityEventTombstoneDTO{
				EventID:   row.EventID.String(),
				ServerSeq: row.ServerSeq,
			})
			continue
		}
		result.upserts = append(result.upserts, activityEventUpsertDTO{
			EventID:     row.EventID.String(),
			StartedAt:   formatTs(row.StartedAt),
			EndedAt:     formatTs(row.EndedAt),
			AppBundleID: row.AppBundleID,
			Category:    row.CategoryName,
			Precision:   row.Precision,
			ServerSeq:   row.ServerSeq,
		})
	}
	return result, nil
}

func pullFocusSessions(ctx context.Context, q storedb.Querier, uid pgtype.UUID, cursor store.PullCursor, limit int) (pageResult, error) {
	rows, err := q.ListFocusSessionChangesForUser(ctx, storedb.ListFocusSessionChangesForUserParams{
		UserID:     uid,
		CursorXid8: pgtype.Uint64{Uint64: cursor.Xid8, Valid: true},
		CursorSeq:  cursor.ServerSeq,
		PageLimit:  store.LimitParam(limit + 1),
	})
	if err != nil {
		return pageResult{}, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	result := pageResult{hasMore: hasMore, lastPos: cursor}
	for _, row := range rows {
		result.lastPos = store.PullCursor{Xid8: row.Xid8.Uint64, ServerSeq: row.ServerSeq}
		if row.DeletedAt.Valid {
			result.tombstones = append(result.tombstones, focusSessionTombstoneDTO{
				ID:        row.ID.String(),
				ServerSeq: row.ServerSeq,
			})
			continue
		}
		result.upserts = append(result.upserts, focusSessionUpsertDTO{
			ID:               row.ID.String(),
			UpdatedAt:        formatTs(row.UpdatedAt),
			StartedAt:        formatTs(row.StartedAt),
			EndedAt:          formatOptionalTs(row.EndedAt),
			ProjectID:        formatOptionalUUID(row.ProjectID),
			Kind:             row.Kind,
			Status:           row.Status,
			PlannedDurationS: row.PlannedDurationS,
			Note:             row.Note,
			ServerSeq:        row.ServerSeq,
		})
	}
	return result, nil
}

func pullProjects(ctx context.Context, q storedb.Querier, uid pgtype.UUID, cursor store.PullCursor, limit int) (pageResult, error) {
	rows, err := q.ListProjectChangesForUser(ctx, storedb.ListProjectChangesForUserParams{
		UserID:     uid,
		CursorXid8: pgtype.Uint64{Uint64: cursor.Xid8, Valid: true},
		CursorSeq:  cursor.ServerSeq,
		PageLimit:  store.LimitParam(limit + 1),
	})
	if err != nil {
		return pageResult{}, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	result := pageResult{hasMore: hasMore, lastPos: cursor}
	for _, row := range rows {
		result.lastPos = store.PullCursor{Xid8: row.Xid8.Uint64, ServerSeq: row.ServerSeq}
		if row.DeletedAt.Valid {
			result.tombstones = append(result.tombstones, projectTombstoneDTO{
				ID:        row.ID.String(),
				ServerSeq: row.ServerSeq,
			})
			continue
		}
		result.upserts = append(result.upserts, projectUpsertDTO{
			ID:         row.ID.String(),
			Name:       row.Name,
			Color:      row.Color,
			ArchivedAt: formatOptionalTs(row.ArchivedAt),
			UpdatedAt:  formatTs(row.UpdatedAt),
			ServerSeq:  row.ServerSeq,
		})
	}
	return result, nil
}

func pullTags(ctx context.Context, q storedb.Querier, uid pgtype.UUID, cursor store.PullCursor, limit int) (pageResult, error) {
	rows, err := q.ListTagChangesForUser(ctx, storedb.ListTagChangesForUserParams{
		UserID:     uid,
		CursorXid8: pgtype.Uint64{Uint64: cursor.Xid8, Valid: true},
		CursorSeq:  cursor.ServerSeq,
		PageLimit:  store.LimitParam(limit + 1),
	})
	if err != nil {
		return pageResult{}, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	result := pageResult{hasMore: hasMore, lastPos: cursor}
	for _, row := range rows {
		result.lastPos = store.PullCursor{Xid8: row.Xid8.Uint64, ServerSeq: row.ServerSeq}
		if row.DeletedAt.Valid {
			result.tombstones = append(result.tombstones, tagTombstoneDTO{
				ID:        row.ID.String(),
				ServerSeq: row.ServerSeq,
			})
			continue
		}
		result.upserts = append(result.upserts, tagUpsertDTO{
			ID:        row.ID.String(),
			Name:      row.Name,
			UpdatedAt: formatTs(row.UpdatedAt),
			ServerSeq: row.ServerSeq,
		})
	}
	return result, nil
}

func pullUserAppSettings(ctx context.Context, q storedb.Querier, uid pgtype.UUID, cursor store.PullCursor, limit int) (pageResult, error) {
	rows, err := q.ListUserAppSettingChangesForUser(ctx, storedb.ListUserAppSettingChangesForUserParams{
		UserID:     uid,
		CursorXid8: pgtype.Uint64{Uint64: cursor.Xid8, Valid: true},
		CursorSeq:  cursor.ServerSeq,
		PageLimit:  store.LimitParam(limit + 1),
	})
	if err != nil {
		return pageResult{}, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	result := pageResult{hasMore: hasMore, lastPos: cursor}
	for _, row := range rows {
		result.lastPos = store.PullCursor{Xid8: row.Xid8.Uint64, ServerSeq: row.ServerSeq}
		// No tombstone path: user_app_settings has no deleted_at/deleted
		// column (see documentation/database-schema.md), so every row here
		// is an upsert.
		result.upserts = append(result.upserts, userAppSettingUpsertDTO{
			AppID:      row.AppID.String(),
			CategoryID: formatOptionalUUID(row.CategoryID),
			Excluded:   row.Excluded,
			UpdatedAt:  formatTs(row.UpdatedAt),
			ServerSeq:  row.ServerSeq,
		})
	}
	return result, nil
}

// pullCategories delivers ListCategoryChangesForUser's page (see that
// query's doc comment for the delivered-rows scoping — system defaults
// plus the caller's own categories).
func pullCategories(ctx context.Context, q storedb.Querier, uid pgtype.UUID, cursor store.PullCursor, limit int) (pageResult, error) {
	rows, err := q.ListCategoryChangesForUser(ctx, storedb.ListCategoryChangesForUserParams{
		UserID:     uid,
		CursorXid8: pgtype.Uint64{Uint64: cursor.Xid8, Valid: true},
		CursorSeq:  cursor.ServerSeq,
		PageLimit:  store.LimitParam(limit + 1),
	})
	if err != nil {
		return pageResult{}, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	result := pageResult{hasMore: hasMore, lastPos: cursor}
	for _, row := range rows {
		result.lastPos = store.PullCursor{Xid8: row.Xid8.Uint64, ServerSeq: row.ServerSeq}
		if row.DeletedAt.Valid {
			result.tombstones = append(result.tombstones, categoryTombstoneDTO{
				ID:        row.ID.String(),
				ServerSeq: row.ServerSeq,
			})
			continue
		}
		result.upserts = append(result.upserts, categoryUpsertDTO{
			ID:           row.ID.String(),
			Name:         row.Name,
			Color:        row.Color,
			Productivity: row.Productivity,
			UpdatedAt:    formatTs(row.UpdatedAt),
			ServerSeq:    row.ServerSeq,
		})
	}
	return result, nil
}
