package store_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/mirkru37/rize-backend/internal/store"
)

func TestConstraintViolation(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantCode   string
		wantOK     bool
		wantSubstr string
	}{
		{
			name:   "not a pg error",
			err:    errors.New("boom"),
			wantOK: false,
		},
		{
			name:   "nil error",
			err:    nil,
			wantOK: false,
		},
		{
			name:     "foreign key violation",
			err:      &pgconn.PgError{Code: "23503"},
			wantCode: "FOREIGN_KEY_VIOLATION",
			wantOK:   true,
		},
		{
			name:       "unique violation without constraint name",
			err:        &pgconn.PgError{Code: "23505"},
			wantCode:   "CONFLICT",
			wantOK:     true,
			wantSubstr: "value conflicts with an existing record",
		},
		{
			name:       "unique violation with constraint name",
			err:        &pgconn.PgError{Code: "23505", ConstraintName: "tags_user_id_name_key"},
			wantCode:   "CONFLICT",
			wantOK:     true,
			wantSubstr: "tags_user_id_name_key",
		},
		{
			name:       "check violation with constraint name",
			err:        &pgconn.PgError{Code: "23514", ConstraintName: "categories_productivity_check"},
			wantCode:   "VALIDATION_ERROR",
			wantOK:     true,
			wantSubstr: "categories_productivity_check",
		},
		{
			name:     "not null violation",
			err:      &pgconn.PgError{Code: "23502"},
			wantCode: "VALIDATION_ERROR",
			wantOK:   true,
		},
		{
			name:   "unrecognized code",
			err:    &pgconn.PgError{Code: "99999"},
			wantOK: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			code, message, ok := store.ConstraintViolation(tt.err)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if code != tt.wantCode {
				t.Errorf("code = %q, want %q", code, tt.wantCode)
			}
			if tt.wantSubstr != "" && !strings.Contains(message, tt.wantSubstr) {
				t.Errorf("message = %q, want it to contain %q", message, tt.wantSubstr)
			}
		})
	}
}
