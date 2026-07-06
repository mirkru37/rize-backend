package sync

import (
	"context"
	"fmt"
	"time"

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
// pulled the same way. sync-protocol.md's own §Entity Classes table
// likewise never lists a bare "categories" entity among the mutable/LWW
// class (only "category overrides (user_app_settings)" appears there).
//
// This is a direct contradiction between the two source-of-truth documents
// (database-schema.md says categories sync via server_seq keyset
// pagination like every other syncable table; sync-protocol.md's Entity
// Classes table and Pull response example both omit it) that RIZ-34's
// brief does not resolve. Per this task's instructions, that sub-point is
// STOPPED ON rather than guessed at: "categories" is deliberately left out
// of the pull response below. See the PR description for this ticket for
// the explicit callout.
//
// "aggregates" (server-derived rollups) is also omitted: no aggregation
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
// beyond this page (hasMore), and lastSeq, the server_seq boundary this
// entity type's page reached (or the incoming cursor, unchanged, if this
// entity type had zero rows to return).
type pageResult struct {
	upserts    []any
	tombstones []any
	hasMore    bool
	lastSeq    int64
}

// pull implements GET /v1/sync/changes for userID (from the authenticated
// access token — never a query/body parameter, per
// documentation/security.md §Tenant Isolation), per
// documentation/sync-protocol.md §Pull.
//
// Every entity type in pullEntityTypes is queried independently for up to
// limit+1 rows with server_seq > cursor, scoped to userID; limit+1 lets
// this method detect "more rows exist beyond this page" without a second
// round trip. The combined next_cursor is the minimum of the per-type
// boundaries among types that still have more rows pending (so no row is
// ever skipped), or the maximum boundary across all types when every type
// is fully drained (so the cursor still advances as far as safely
// possible). Because pulls are idempotent (documentation/sync-protocol.md:
// "requesting the same cursor twice returns the same page, and applying
// that page twice is a no-op"), this conservative boundary can cause a
// small amount of redundant redelivery across page boundaries for an
// already-drained entity type, which is explicitly safe per that
// guarantee — it never causes a skip.
func (s *Service) pull(ctx context.Context, userID, rawCursor string, rawLimit int) (pullResponse, error) {
	uid, err := parseUUID(userID)
	if err != nil {
		return pullResponse{}, fmt.Errorf("%w: invalid authenticated user id", ErrValidation)
	}

	cursor, err := store.DecodeCursor(rawCursor)
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

	aeResult, err := s.pullActivityEvents(ctx, uid, cursor, limit)
	if err != nil {
		return pullResponse{}, fmt.Errorf("sync: pull activity_events: %w", err)
	}
	results["activity_events"] = aeResult

	fsResult, err := s.pullFocusSessions(ctx, uid, cursor, limit)
	if err != nil {
		return pullResponse{}, fmt.Errorf("sync: pull focus_sessions: %w", err)
	}
	results["focus_sessions"] = fsResult

	prResult, err := s.pullProjects(ctx, uid, cursor, limit)
	if err != nil {
		return pullResponse{}, fmt.Errorf("sync: pull projects: %w", err)
	}
	results["projects"] = prResult

	tgResult, err := s.pullTags(ctx, uid, cursor, limit)
	if err != nil {
		return pullResponse{}, fmt.Errorf("sync: pull tags: %w", err)
	}
	results["tags"] = tgResult

	uasResult, err := s.pullUserAppSettings(ctx, uid, cursor, limit)
	if err != nil {
		return pullResponse{}, fmt.Errorf("sync: pull user_app_settings: %w", err)
	}
	results["user_app_settings"] = uasResult

	hasMore := false
	minPendingSeq := int64(-1)
	maxSeq := cursor
	for _, r := range results {
		if r.lastSeq > maxSeq {
			maxSeq = r.lastSeq
		}
		if r.hasMore {
			hasMore = true
			if minPendingSeq == -1 || r.lastSeq < minPendingSeq {
				minPendingSeq = r.lastSeq
			}
		}
	}

	nextSeq := maxSeq
	if hasMore {
		nextSeq = minPendingSeq
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
		NextCursor: store.EncodeCursor(nextSeq),
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

func (s *Service) pullActivityEvents(ctx context.Context, uid pgtype.UUID, cursor int64, limit int) (pageResult, error) {
	rows, err := s.Queries.ListActivityEventChangesForUser(ctx, storedb.ListActivityEventChangesForUserParams{
		UserID:    uid,
		ServerSeq: cursor,
		Limit:     store.LimitParam(limit + 1),
	})
	if err != nil {
		return pageResult{}, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	result := pageResult{hasMore: hasMore, lastSeq: cursor}
	for _, row := range rows {
		result.lastSeq = row.ServerSeq
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

func (s *Service) pullFocusSessions(ctx context.Context, uid pgtype.UUID, cursor int64, limit int) (pageResult, error) {
	rows, err := s.Queries.ListFocusSessionChangesForUser(ctx, storedb.ListFocusSessionChangesForUserParams{
		UserID:    uid,
		ServerSeq: cursor,
		Limit:     store.LimitParam(limit + 1),
	})
	if err != nil {
		return pageResult{}, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	result := pageResult{hasMore: hasMore, lastSeq: cursor}
	for _, row := range rows {
		result.lastSeq = row.ServerSeq
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

func (s *Service) pullProjects(ctx context.Context, uid pgtype.UUID, cursor int64, limit int) (pageResult, error) {
	rows, err := s.Queries.ListProjectChangesForUser(ctx, storedb.ListProjectChangesForUserParams{
		UserID:    uid,
		ServerSeq: cursor,
		Limit:     store.LimitParam(limit + 1),
	})
	if err != nil {
		return pageResult{}, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	result := pageResult{hasMore: hasMore, lastSeq: cursor}
	for _, row := range rows {
		result.lastSeq = row.ServerSeq
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

func (s *Service) pullTags(ctx context.Context, uid pgtype.UUID, cursor int64, limit int) (pageResult, error) {
	rows, err := s.Queries.ListTagChangesForUser(ctx, storedb.ListTagChangesForUserParams{
		UserID:    uid,
		ServerSeq: cursor,
		Limit:     store.LimitParam(limit + 1),
	})
	if err != nil {
		return pageResult{}, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	result := pageResult{hasMore: hasMore, lastSeq: cursor}
	for _, row := range rows {
		result.lastSeq = row.ServerSeq
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

func (s *Service) pullUserAppSettings(ctx context.Context, uid pgtype.UUID, cursor int64, limit int) (pageResult, error) {
	rows, err := s.Queries.ListUserAppSettingChangesForUser(ctx, storedb.ListUserAppSettingChangesForUserParams{
		UserID:    uid,
		ServerSeq: cursor,
		Limit:     store.LimitParam(limit + 1),
	})
	if err != nil {
		return pageResult{}, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	result := pageResult{hasMore: hasMore, lastSeq: cursor}
	for _, row := range rows {
		result.lastSeq = row.ServerSeq
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
