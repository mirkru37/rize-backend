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

## Documentation

Contracts live in the master repo and are the source of truth:

- [Backend architecture](../documentation/architecture-backend.md)
- [API reference](../documentation/api-reference.md)
- [Database schema](../documentation/database-schema.md)
- [Sync protocol](../documentation/sync-protocol.md)
- [Security requirements](../documentation/security.md)

## Git flow

One Linear ticket (`RIZ-<n>`) → one branch `feat/RIZ-<n>-<slug>` (or `fix/`, `docs/`, `chore/`) → one PR titled `[RIZ-<n>] <summary>` into `main`, linking the ticket. Conventional Commits referencing `RIZ-<n>`.
