package config

import (
	"os"
	"testing"
	"time"
)

// clearEnv unsets every environment variable Load reads, so each test
// starts from a clean slate regardless of the ambient environment or test
// execution order, restoring the original values on test cleanup.
func clearEnv(t *testing.T) {
	t.Helper()
	keys := []string{
		"PORT",
		"ENVIRONMENT",
		"DATABASE_URL",
		"JWT_SIGNING_KEY",
		"CORS_ALLOWED_ORIGINS",
		"RATE_LIMIT_REQUESTS_PER_MINUTE",
		"SHUTDOWN_TIMEOUT_SECONDS",
	}
	for _, k := range keys {
		orig, had := os.LookupEnv(k)
		_ = os.Unsetenv(k)
		t.Cleanup(func() {
			if had {
				_ = os.Setenv(k, orig)
			} else {
				_ = os.Unsetenv(k)
			}
		})
	}
}

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.HTTPPort != DefaultHTTPPort {
		t.Errorf("HTTPPort = %q, want %q", cfg.HTTPPort, DefaultHTTPPort)
	}
	if cfg.Environment != DefaultEnvironment {
		t.Errorf("Environment = %q, want %q", cfg.Environment, DefaultEnvironment)
	}
	if cfg.DatabaseURL != "" {
		t.Errorf("DatabaseURL = %q, want empty", cfg.DatabaseURL)
	}
	if cfg.JWTSigningKey != "" {
		t.Errorf("JWTSigningKey = %q, want empty", cfg.JWTSigningKey)
	}
	if len(cfg.CORSAllowedOrigins) != 1 || cfg.CORSAllowedOrigins[0] != "*" {
		t.Errorf("CORSAllowedOrigins = %v, want [*]", cfg.CORSAllowedOrigins)
	}
	if cfg.RateLimitRequestsPerMinute != DefaultRateLimitRequestsPerMinute {
		t.Errorf("RateLimitRequestsPerMinute = %d, want %d", cfg.RateLimitRequestsPerMinute, DefaultRateLimitRequestsPerMinute)
	}
	if cfg.ShutdownTimeout != DefaultShutdownTimeout {
		t.Errorf("ShutdownTimeout = %v, want %v", cfg.ShutdownTimeout, DefaultShutdownTimeout)
	}
}

func TestLoadEnvOverrides(t *testing.T) {
	clearEnv(t)

	t.Setenv("PORT", "9090")
	t.Setenv("ENVIRONMENT", "production")
	t.Setenv("DATABASE_URL", "postgres://user:example-not-a-real-secret@localhost:5432/rize") //nolint:gosec // test fixture, not a real credential
	t.Setenv("JWT_SIGNING_KEY", "test-key-material")
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://app.example.com, https://admin.example.com")
	t.Setenv("RATE_LIMIT_REQUESTS_PER_MINUTE", "42")
	t.Setenv("SHUTDOWN_TIMEOUT_SECONDS", "30")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.HTTPPort != "9090" {
		t.Errorf("HTTPPort = %q, want %q", cfg.HTTPPort, "9090")
	}
	if cfg.Environment != "production" {
		t.Errorf("Environment = %q, want %q", cfg.Environment, "production")
	}
	if cfg.DatabaseURL != "postgres://user:example-not-a-real-secret@localhost:5432/rize" { //nolint:gosec // test fixture, not a real credential
		t.Errorf("DatabaseURL = %q, want the override", cfg.DatabaseURL)
	}
	if cfg.JWTSigningKey != "test-key-material" {
		t.Errorf("JWTSigningKey = %q, want %q", cfg.JWTSigningKey, "test-key-material")
	}
	wantOrigins := []string{"https://app.example.com", "https://admin.example.com"}
	if len(cfg.CORSAllowedOrigins) != len(wantOrigins) {
		t.Fatalf("CORSAllowedOrigins = %v, want %v", cfg.CORSAllowedOrigins, wantOrigins)
	}
	for i, o := range wantOrigins {
		if cfg.CORSAllowedOrigins[i] != o {
			t.Errorf("CORSAllowedOrigins[%d] = %q, want %q", i, cfg.CORSAllowedOrigins[i], o)
		}
	}
	if cfg.RateLimitRequestsPerMinute != 42 {
		t.Errorf("RateLimitRequestsPerMinute = %d, want 42", cfg.RateLimitRequestsPerMinute)
	}
	if cfg.ShutdownTimeout != 30*time.Second {
		t.Errorf("ShutdownTimeout = %v, want 30s", cfg.ShutdownTimeout)
	}
}

func TestLoadInvalidRateLimit(t *testing.T) {
	clearEnv(t)
	t.Setenv("RATE_LIMIT_REQUESTS_PER_MINUTE", "not-a-number")

	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want error for invalid rate limit")
	}
}

func TestLoadInvalidShutdownTimeout(t *testing.T) {
	clearEnv(t)
	t.Setenv("SHUTDOWN_TIMEOUT_SECONDS", "-5")

	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want error for non-positive shutdown timeout")
	}
}
