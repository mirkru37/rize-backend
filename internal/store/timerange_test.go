package store_test

import (
	"testing"
	"time"

	"github.com/mirkru37/rize-backend/internal/store"
)

func TestValidateRange(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		from    time.Time
		to      time.Time
		wantErr bool
	}{
		{
			name:    "valid narrow range",
			from:    base,
			to:      base.Add(time.Hour),
			wantErr: false,
		},
		{
			name:    "from equals to",
			from:    base,
			to:      base,
			wantErr: false,
		},
		{
			name:    "from after to",
			from:    base.Add(time.Hour),
			to:      base,
			wantErr: true,
		},
		{
			name:    "range exactly at the max is allowed",
			from:    base,
			to:      base.Add(store.MaxReportRange),
			wantErr: false,
		},
		{
			name:    "range exceeding the max is rejected",
			from:    base,
			to:      base.Add(store.MaxReportRange + time.Hour),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			err := store.ValidateRange(tt.from, tt.to)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateRange(%v, %v) error = %v, wantErr %v", tt.from, tt.to, err, tt.wantErr)
			}
		})
	}
}
