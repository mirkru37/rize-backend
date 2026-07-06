# rize-backend

Go backend for Rize-Clone: auth, ingestion, sync, reporting. Stack: Go 1.23+, Chi, PostgreSQL 16 + TimescaleDB, sqlc/pgx, golang-migrate, golang-jwt.

## Rules

- **Consult `../documentation/` before changing any contract.** `database-schema.md`, `api-reference.md`, and `sync-protocol.md` are the source of truth; a contract change requires the doc to be updated in the same cycle.
- Layering: `handlers → services → repositories`. Handlers stay thin (decode, validate, call service, encode); business logic lives in services.
- Data access through sqlc-generated queries; no hand-rolled SQL in services.
- Errors are wrapped with context (`fmt.Errorf("...: %w", err)`); RFC 7807 error bodies at the HTTP boundary.
- Every query is scoped by `user_id` from the access token — cross-tenant access paths are a HIGH-severity review finding.
- Migrations: golang-migrate, forward-only, paired with a `database-schema.md` update.
- Tests: table-driven; new functionality must be covered before a PR opens.
- Containerization is a hard requirement: keep the `Dockerfile` (multi-stage Go build) buildable and `docker compose up` (api + TimescaleDB + migrations) working end-to-end; a change that breaks either is a HIGH-severity review finding.

## Commands

```
go build ./...        # compile
go test ./...         # tests
golangci-lint run     # lint
docker compose up -d  # local TimescaleDB
```

## Git flow

One Linear ticket (`RIZ-<n>`) → one branch `feat/RIZ-<n>-<slug>` (or `fix/`, `docs/`, `chore/`) → one PR `[RIZ-<n>] <summary>` into `main`, linking the ticket. Conventional Commits referencing `RIZ-<n>`. Never open a PR with failing tests or lint.
