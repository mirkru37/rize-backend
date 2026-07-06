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
	for _, k := range EnvVarNames {
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
	if cfg.ReadTimeout != DefaultReadTimeout {
		t.Errorf("ReadTimeout = %v, want %v", cfg.ReadTimeout, DefaultReadTimeout)
	}
	if cfg.WriteTimeout != DefaultWriteTimeout {
		t.Errorf("WriteTimeout = %v, want %v", cfg.WriteTimeout, DefaultWriteTimeout)
	}
	if cfg.IdleTimeout != DefaultIdleTimeout {
		t.Errorf("IdleTimeout = %v, want %v", cfg.IdleTimeout, DefaultIdleTimeout)
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

func TestLoadInvalidEnvironment(t *testing.T) {
	clearEnv(t)
	t.Setenv("ENVIRONMENT", "prod")

	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want error for unrecognized ENVIRONMENT")
	}
}

func TestLoadEnvironmentCORSWildcardGuard(t *testing.T) {
	tests := []struct {
		name        string
		environment string
		corsOrigins string
		wantErr     bool
	}{
		{
			name:        "development allows unset CORS (defaults to wildcard)",
			environment: "development",
			corsOrigins: "",
			wantErr:     false,
		},
		{
			name:        "development allows explicit wildcard",
			environment: "development",
			corsOrigins: "*",
			wantErr:     false,
		},
		{
			name:        "staging rejects wildcard default",
			environment: "staging",
			corsOrigins: "",
			wantErr:     true,
		},
		{
			name:        "staging rejects explicit wildcard",
			environment: "staging",
			corsOrigins: "*",
			wantErr:     true,
		},
		{
			name:        "staging allows explicit origin list",
			environment: "staging",
			corsOrigins: "https://staging.example.com",
			wantErr:     false,
		},
		{
			name:        "production rejects wildcard default",
			environment: "production",
			corsOrigins: "",
			wantErr:     true,
		},
		{
			name:        "production rejects wildcard mixed with explicit origins",
			environment: "production",
			corsOrigins: "https://app.example.com,*",
			wantErr:     true,
		},
		{
			name:        "production allows explicit origin list",
			environment: "production",
			corsOrigins: "https://app.example.com",
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("ENVIRONMENT", tt.environment)
			if tt.corsOrigins != "" {
				t.Setenv("CORS_ALLOWED_ORIGINS", tt.corsOrigins)
			}

			_, err := Load()
			if tt.wantErr && err == nil {
				t.Errorf("Load() error = nil, want error (environment=%q, cors=%q)", tt.environment, tt.corsOrigins)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("Load() error = %v, want nil (environment=%q, cors=%q)", err, tt.environment, tt.corsOrigins)
			}
		})
	}
}
