package sync

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// TestConstraintViolationResult is a direct unit test for
// constraintViolationResult, mirroring internal/store's identical
// ConstraintViolation test — the DB-triggered call sites in
// applyActivityEvent/applyFocusSession are defense-in-depth around
// scenarios (e.g. a TOCTOU concurrently-deleted project) this package's
// integration tests can't deterministically reproduce.
func TestConstraintViolationResult(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode string
		wantOK   bool
	}{
		{name: "not a pg error", err: errors.New("boom"), wantOK: false},
		{name: "nil error", err: nil, wantOK: false},
		{name: "foreign key violation", err: &pgconn.PgError{Code: "23503"}, wantCode: "FOREIGN_KEY_VIOLATION", wantOK: true},
		{name: "unique violation", err: &pgconn.PgError{Code: "23505"}, wantCode: "VALIDATION_ERROR", wantOK: true},
		{name: "check violation", err: &pgconn.PgError{Code: "23514"}, wantCode: "VALIDATION_ERROR", wantOK: true},
		{name: "not null violation", err: &pgconn.PgError{Code: "23502"}, wantCode: "VALIDATION_ERROR", wantOK: true},
		{name: "unrecognized code", err: &pgconn.PgError{Code: "99999"}, wantOK: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			code, _, ok := constraintViolationResult(tt.err)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if tt.wantOK && code != tt.wantCode {
				t.Errorf("code = %q, want %q", code, tt.wantCode)
			}
		})
	}
}
