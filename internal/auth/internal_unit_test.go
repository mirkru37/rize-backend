package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"testing"
	"time"
)

func TestStrPtr(t *testing.T) {
	if got := strPtr(""); got != nil {
		t.Errorf("strPtr(\"\") = %v, want nil", got)
	}
	got := strPtr("hello")
	if got == nil || *got != "hello" {
		t.Errorf("strPtr(\"hello\") = %v, want a pointer to \"hello\"", got)
	}
}

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

func TestValidEmailInternal(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "valid, trimmed", input: "  alice@example.com  ", want: "alice@example.com", wantErr: false},
		{name: "empty", input: "", wantErr: true},
		{name: "blank (whitespace only)", input: "   ", wantErr: true},
		{name: "malformed", input: "not-an-email", wantErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			got, err := validEmail(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validEmail(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("validEmail(%q) = %q, want %q", tt.input, got, tt.want)
			}
			if tt.wantErr && !errors.Is(err, ErrValidation) {
				t.Errorf("validEmail(%q) error = %v, want ErrValidation", tt.input, err)
			}
		})
	}
}

func TestVerifyPasswordInternal(t *testing.T) {
	hash, err := HashPassword("correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	tests := []struct {
		name     string
		hash     string
		password string
		want     bool
		wantErr  bool
	}{
		{name: "correct password", hash: hash, password: "correct-horse-battery-staple", want: true},
		{name: "wrong password", hash: hash, password: "wrong-password", want: false},
		{name: "malformed hash", hash: "not-a-real-argon2-hash", password: "anything", wantErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			got, err := VerifyPassword(tt.hash, tt.password)
			if (err != nil) != tt.wantErr {
				t.Fatalf("VerifyPassword() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("VerifyPassword() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestVerifyAccessTokenRejectsNonRSAAlgorithm(t *testing.T) {
	// VerifyAccessToken must reject a token signed with a different
	// algorithm family entirely (as opposed to the middleware-level tests,
	// which only cover an RS256 token signed with the wrong RSA key).
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}

	// A token whose header claims HS256 but is otherwise well-formed: the
	// library's own alg-mismatch check must reject it before ever
	// consulting the supplied RSA public key.
	const hs256Token = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9." +
		"eyJzdWIiOiJ1c2VyLTEyMyJ9." +
		"dGFtcGVyZWQtc2lnbmF0dXJl"

	if _, err := VerifyAccessToken(&key.PublicKey, hs256Token); err == nil {
		t.Fatal("expected an error for a token signed with a non-RSA algorithm")
	}
}

func TestGenerateRefreshTokenInternal(t *testing.T) {
	a, err := GenerateRefreshToken()
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}
	b, err := GenerateRefreshToken()
	if err != nil {
		t.Fatalf("GenerateRefreshToken (second call): %v", err)
	}
	if a == b {
		t.Error("two calls to GenerateRefreshToken produced the same token")
	}
	if len(a) == 0 {
		t.Error("GenerateRefreshToken returned an empty token")
	}
}

func TestIssueAccessTokenInternal(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}

	token, err := IssueAccessToken(key, "user-1", "admin", "device-1", time.Now())
	if err != nil {
		t.Fatalf("IssueAccessToken: %v", err)
	}

	claims, err := VerifyAccessToken(&key.PublicKey, token)
	if err != nil {
		t.Fatalf("VerifyAccessToken: %v", err)
	}
	if claims.Subject != "user-1" || claims.Role != "admin" || claims.DeviceID != "device-1" {
		t.Errorf("claims = %+v, want Subject=user-1 Role=admin DeviceID=device-1", claims)
	}
}
