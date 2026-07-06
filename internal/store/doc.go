// Package store will implement the repository layer: sqlc-generated queries
// and a thin pgx-backed data-access API used by the auth, sync, and reports
// services, plus the pgx connection pool used for the /readyz database
// check. It also owns the golang-migrate migration files under
// internal/store/migrations, applied forward-only against PostgreSQL 16
// with the TimescaleDB extension. Repositories know how to read and write
// rows; they hold no business logic, which lives in the service packages
// that call them.
package store
