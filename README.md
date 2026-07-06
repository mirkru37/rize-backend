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

```
docker compose up -d db        # timescaledb image
migrate -path internal/store/migrations -database "$DATABASE_URL" up
go run ./cmd/api
```

## Documentation

Contracts live in the master repo and are the source of truth:

- [Backend architecture](../documentation/architecture-backend.md)
- [API reference](../documentation/api-reference.md)
- [Database schema](../documentation/database-schema.md)
- [Sync protocol](../documentation/sync-protocol.md)
- [Security requirements](../documentation/security.md)

## Git flow

One Linear ticket (`RIZ-<n>`) → one branch `feat/RIZ-<n>-<slug>` (or `fix/`, `docs/`, `chore/`) → one PR titled `[RIZ-<n>] <summary>` into `main`, linking the ticket. Conventional Commits referencing `RIZ-<n>`.
