package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool builds a pgx connection pool from a PostgreSQL/TimescaleDB
// connection string. It returns (nil, nil) when databaseURL is empty, so
// callers (e.g. cmd/api) can run without a database configured — /readyz
// reports "not_configured" in that case per
// documentation/architecture-backend.md.
func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	if databaseURL == "" {
		return nil, nil
	}

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("store: create connection pool: %w", err)
	}

	return pool, nil
}
