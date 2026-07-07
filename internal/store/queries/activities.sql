-- name: ListActivityEventsForUser :many
-- GET /v1/activities per documentation/api-reference.md §Activities &
-- reports: raw tracked events for the authenticated user, filterable by
-- time range, app, category, project, device, and precision. Scoped by
-- user_id per documentation/security.md §Tenant Isolation. Soft-deleted
-- (deleted = true) rows are excluded, matching the CRUD groups' convention
-- of excluding tombstoned rows from a "current state" list.
--
-- Keyset-paginated on (started_at, event_id) ascending rather than the
-- server_seq cursor other CRUD lists use: an activity-events list is
-- naturally consumed in chronological order over an explicit time range,
-- and started_at is the hypertable's own partitioning column, so ordering
-- on it lets Postgres use the activity_events_user_started_idx index
-- instead of the server_seq index. The initial page passes
-- cursor_started_at = '-infinity' and cursor_event_id =
-- '00000000-0000-0000-0000-000000000000' (see internal/activities'
-- cursor.go), which sorts before every real row.
--
-- sqlc.narg filters: a NULL argument (an omitted query-string filter)
-- disables that filter's predicate entirely via the "IS NULL OR" branch.
SELECT * FROM activity_events
WHERE user_id = $1
  AND deleted = false
  AND started_at >= sqlc.arg(from_ts) AND started_at < sqlc.arg(to_ts)
  AND (sqlc.narg(app_id)::uuid IS NULL OR app_id = sqlc.narg(app_id))
  AND (sqlc.narg(category_id)::uuid IS NULL OR category_id = sqlc.narg(category_id))
  AND (sqlc.narg(project_id)::uuid IS NULL OR project_id = sqlc.narg(project_id))
  AND (sqlc.narg(device_id)::uuid IS NULL OR device_id = sqlc.narg(device_id))
  AND (sqlc.narg(precision)::text IS NULL OR precision = sqlc.narg(precision))
  AND (started_at, event_id) > (sqlc.arg(cursor_started_at)::timestamptz, sqlc.arg(cursor_event_id)::uuid)
ORDER BY started_at ASC, event_id ASC
LIMIT sqlc.arg(page_limit);

-- name: RawActivityEventsForReport :many
-- The report layer's raw-event pass, per documentation/architecture-backend.md
-- §Aggregation Strategy: used (a) always for the current/partial period,
-- and (b) as the only path for filters (device_id, precision) or
-- dimensions (project) the continuous aggregates cannot serve. As of
-- migrations 000034-000036, daily_app_totals/daily_category_totals do
-- carry a device_id column, but CategoryTotalsForRange/AppTotalsForRange
-- below only use it for a same-device overlap *cap* (a window-length
-- upper bound, not an exact merge — see those queries' comments); neither
-- query can honor an explicit device_id filter or a precision filter (no
-- precision column exists on any cagg), and no project-dimensioned
-- aggregate exists at all, so a device_id/precision-filtered or
-- project-dimensioned request still falls back to this raw pass — see
-- internal/reports' service.go doc comment for the full resolution.
--
-- Selects events overlapping [from_ts, to_ts) — not merely started within
-- it — so an event that begins before the window and ends inside it (or
-- vice versa) is still included for trimming, which clips each event to
-- the window before merging. Joins apps/categories/projects so the report
-- layer never needs a second round trip to resolve display names.
SELECT
    e.device_id,
    e.app_id,
    a.name AS app_name,
    a.bundle_id AS app_bundle_id,
    e.category_id,
    c.name AS category_name,
    e.project_id,
    p.name AS project_name,
    e.started_at,
    e.ended_at
FROM activity_events e
LEFT JOIN apps a ON a.id = e.app_id
LEFT JOIN categories c ON c.id = e.category_id
LEFT JOIN projects p ON p.id = e.project_id
WHERE e.user_id = $1
  AND e.deleted = false
  AND e.started_at < sqlc.arg(to_ts) AND e.ended_at > sqlc.arg(from_ts)
  AND (sqlc.narg(app_id)::uuid IS NULL OR e.app_id = sqlc.narg(app_id))
  AND (sqlc.narg(category_id)::uuid IS NULL OR e.category_id = sqlc.narg(category_id))
  AND (sqlc.narg(project_id)::uuid IS NULL OR e.project_id = sqlc.narg(project_id))
  AND (sqlc.narg(device_id)::uuid IS NULL OR e.device_id = sqlc.narg(device_id))
  AND (sqlc.narg(precision)::text IS NULL OR e.precision = sqlc.narg(precision))
ORDER BY e.device_id, e.started_at;

-- name: CategoryTotalsForRange :many
-- Closed-period fast path for reports/daily, reports/categories, and
-- reports/summary's category breakdown: sums the daily_category_totals
-- continuous aggregate over [from_day, to_day) per
-- documentation/architecture-backend.md §Aggregation Strategy. Only used
-- when the request has no device_id/precision filter — see
-- internal/reports/service.go.
--
-- Bounds (does NOT reproduce) the raw-event path's same-device overlap
-- handling (documentation/sync-protocol.md §Overlap Rules;
-- internal/reports/trim.go implements the actual interval merge for raw
-- events): daily_category_totals is grouped by device_id (migration
-- 000035), so each device's total_s is first summed across the requested
-- days, then capped at window_seconds — the length of [from_day, to_day)
-- in seconds — before being added into the category total. This is an
-- UPPER BOUND on same-device overlap inflation, not the raw path's exact
-- merged duration: the cap only removes overlap that pushes a device's
-- naive summed total_s above the window's own length, so it agrees with
-- the raw path exactly when a device's merged coverage spans the whole
-- window, but can still overstate the total otherwise. For example, over
-- a 24h window with same-device events covering 00:00-06:00 and
-- 03:00-09:00 (merged coverage 9h, naive sum 12h), the raw path returns
-- 9h while this query returns min(12h, 24h) = 12h. Cross-device overlap
-- is still not trimmed either way: each device's capped total is summed
-- into the category total independently, same as the raw path.
-- window_seconds must be > 0 (validated by the caller — see
-- internal/reports/dimension.go's windowSeconds) so LEAST(x, 0) can't
-- silently zero every total.
SELECT
    pd.category_id,
    c.name AS category_name,
    sum(LEAST(pd.device_total_s, sqlc.arg(window_seconds)::bigint))::bigint AS total_s
FROM (
    SELECT
        d.category_id,
        d.device_id,
        sum(d.total_s) AS device_total_s
    FROM daily_category_totals d
    WHERE d.user_id = $1 AND d.day >= sqlc.arg(from_day)::timestamptz AND d.day < sqlc.arg(to_day)::timestamptz
    GROUP BY d.category_id, d.device_id
) pd
LEFT JOIN categories c ON c.id = pd.category_id
GROUP BY pd.category_id, c.name;

-- name: AppTotalsForRange :many
-- Closed-period fast path for reports/apps: sums the daily_app_totals
-- continuous aggregate over [from_day, to_day) per
-- documentation/architecture-backend.md §Aggregation Strategy. Only used
-- when the request has no device_id/precision filter — see
-- internal/reports/service.go.
--
-- Same per-device window-capping as CategoryTotalsForRange above, using
-- daily_app_totals' device_id column (migration 000034) — an upper bound
-- on same-device overlap inflation for closed periods, not a
-- reproduction of the raw path's exact interval merge. See
-- CategoryTotalsForRange's comment for the worked counterexample where
-- the two paths diverge. window_seconds must be > 0 for the same reason
-- noted there.
SELECT
    pd.app_id,
    a.name AS app_name,
    a.bundle_id AS app_bundle_id,
    sum(LEAST(pd.device_total_s, sqlc.arg(window_seconds)::bigint))::bigint AS total_s
FROM (
    SELECT
        d.app_id,
        d.device_id,
        sum(d.total_s) AS device_total_s
    FROM daily_app_totals d
    WHERE d.user_id = $1 AND d.day >= sqlc.arg(from_day)::timestamptz AND d.day < sqlc.arg(to_day)::timestamptz
    GROUP BY d.app_id, d.device_id
) pd
LEFT JOIN apps a ON a.id = pd.app_id
GROUP BY pd.app_id, a.name, a.bundle_id;
