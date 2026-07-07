package sync

import (
	"errors"
	"testing"
)

func TestParseUUIDInternal(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "valid uuid", input: "123e4567-e89b-12d3-a456-426614174000", wantErr: false},
		{name: "empty string", input: "", wantErr: true},
		{name: "malformed", input: "not-a-uuid", wantErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseUUID(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseUUID(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if tt.wantErr && !errors.Is(err, ErrValidation) {
				t.Errorf("parseUUID(%q) error = %v, want ErrValidation", tt.input, err)
			}
		})
	}
}

func TestParseOptionalUUIDInternal(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantValid bool
		wantErr   bool
	}{
		{name: "empty string is NULL", input: "", wantValid: false, wantErr: false},
		{name: "valid uuid", input: "123e4567-e89b-12d3-a456-426614174000", wantValid: true, wantErr: false},
		{name: "malformed", input: "not-a-uuid", wantErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseOptionalUUID(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseOptionalUUID(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if !tt.wantErr && got.Valid != tt.wantValid {
				t.Errorf("parseOptionalUUID(%q).Valid = %v, want %v", tt.input, got.Valid, tt.wantValid)
			}
		})
	}
}
