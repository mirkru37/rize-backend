# rize-backend

Backend service for Rize-Clone: authentication, activity-event ingestion, cross-device sync, and reporting API. Part of the [Rize-Clone](../README.md) master repo.

## Stack

- **Go 1.23+** with the **Chi** router (stdlib-compatible, middleware-based)
- **PostgreSQL 16 + TimescaleDB** — `activity_events` is a hypertable; reports are served from continuous aggregates
- **sqlc/pgx** for data access, **golang-migrate** for forward-only migrations
- **golang-jwt** for access tokens; opaque rotating refresh tokens

## Package layout

```
cmd/api/                 entrypoint
internal/auth/           Sign in with Apple, email/password, tokens, RBAC
internal/sync/           batched ingest + cursor-based pull
internal/reports/        aggregation/reporting endpoints
internal/store/          repositories, sqlc queries, migrations
internal/middleware/     request ID, logging, rate limit, auth
```

## Quick start

The whole backend runs with Docker — a `Dockerfile` (multi-stage Go build) and a compose file with api + TimescaleDB + a one-shot migration service are required parts of this repo:

```
docker compose up               # db + migrations + api
```

For faster iteration, run the binary on the host against the compose-managed database:

```
docker compose up -d db        # timescaledb image
migrate -path internal/store/migrations -database "$DATABASE_URL" up
go run ./cmd/api
```

## Configuration

All runtime configuration is read from environment variables by `internal/config` — there is no config-file format. `.env.example` at the repo root documents every variable `config.Load()` reads, with safe example values and a comment explaining each one.

```
cp .env.example .env
```

`internal/config/env_example_test.go` enforces that `.env.example` stays complete: it fails if `config.Load()` reads a variable that `.env.example` doesn't document.

- **Running the binary on the host** (`go run ./cmd/api`): the process does not auto-load `.env` (no dotenv library is wired in), so export the variables into your shell first, e.g. `set -a && source .env && set +a`.
- **`docker compose up`**: the `api` service reads `.env` if present, via `env_file: { path: .env, required: false }` — so `JWT_SIGNING_KEY`, `CORS_ALLOWED_ORIGINS`, and other variables from a copied `.env` are picked up by compose. `PORT` and `DATABASE_URL` are still set explicitly in the `environment:` block, and compose gives `environment:` precedence over `env_file:`, so those two stay compose-overridden by design — the container needs `PORT=8080` and the container-network `DATABASE_URL` (host `db`), not a localhost DSN from a host-oriented `.env`.

## Sync changelog retention (RIZ-72)

`sync_changelog` (the append-only outbox `GET /v1/sync/changes` paginates — see [Sync protocol](../documentation/sync-protocol.md)) is pruned automatically by an in-process background job (`internal/sync.Pruner`, started as a ticker goroutine in `cmd/api/main.go`), not by any request-path code:

- Rows older than `SYNC_CHANGELOG_MAX_AGE` (hours; default 2160 = 90 days) are eligible for deletion. 90 days comfortably bounds the commit-ordered pull cursor's xid8 wraparound-safety window (migrations `000024`/`000025`) while giving a long-dormant device's stale local cursor a generous grace period.
- The pruner wakes up every `SYNC_CHANGELOG_PRUNE_INTERVAL_SECONDS` (default 3600) and deletes at most `SYNC_CHANGELOG_PRUNE_BATCH_SIZE` (default 5000) rows per tick, so one tick never holds a long-running transaction.
- Each prune batch's furthest `(xid8, server_seq)` position is recorded in the single-row `sync_changelog_horizon` table (migration `000027`), transactionally with the delete that produced it (select + delete + horizon-advance all happen in one database transaction — see `Pruner.PruneOnce`'s doc comment for why that's what makes it atomic).
- `GET /v1/sync/changes` checks a non-empty caller cursor against that horizon before serving a page: a cursor strictly below the horizon can no longer be served a gap-free page (the rows between it and the horizon were pruned) and the request fails with `410 Gone` (`cursor-expired`) instead of silently skipping changes. Per [Sync protocol](../documentation/sync-protocol.md)'s Device Restore from Backup recovery path, the correct client response is to reset the cursor to empty and re-pull from the beginning — always safe, since pulls are idempotent. A first-ever pull (empty cursor) is never affected.
- Device-level cursor tracking (the `sync_cursors` table) is intentionally untouched by this feature — see backlog ticket RIZ-78.

See `internal/sync/retention.go` for the concurrency-safety argument (why a prune can never strand an already-in-flight pull) and `internal/sync/retention_test.go` for the covering tests.

## Documentation

Contracts live in the master repo and are the source of truth:

- [Backend architecture](../documentation/architecture-backend.md)
- [API reference](../documentation/api-reference.md)
- [Database schema](../documentation/database-schema.md)
- [Sync protocol](../documentation/sync-protocol.md)
- [Security requirements](../documentation/security.md)

## Git flow

One Linear ticket (`RIZ-<n>`) → one branch `feat/RIZ-<n>-<slug>` (or `fix/`, `docs/`, `chore/`) → one PR titled `[RIZ-<n>] <summary>` into `main`, linking the ticket. Conventional Commits referencing `RIZ-<n>`.
