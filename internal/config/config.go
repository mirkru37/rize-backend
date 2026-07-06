// Package config loads rize-backend's process configuration from
// environment variables. There is no config-file format: every setting is
// read from the environment, with sane defaults for local development, per
// documentation/architecture-backend.md §Config & Observability.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultHTTPPort is used when PORT is not set.
	DefaultHTTPPort = "8080"
	// DefaultEnvironment is used when ENVIRONMENT is not set.
	DefaultEnvironment = "development"
	// DefaultCORSAllowedOrigins is used when CORS_ALLOWED_ORIGINS is not
	// set. It allows every origin, which is acceptable for local
	// development but must be overridden with an explicit origin list in
	// any deployed environment (see security.md §API hardening: "CORS is
	// locked to known origins").
	DefaultCORSAllowedOrigins = "*"
	// DefaultRateLimitRequestsPerMinute is used when
	// RATE_LIMIT_REQUESTS_PER_MINUTE is not set. documentation/security.md
	// suggests different per-scope limits (10/min per-IP on auth routes,
	// 60/min per-user on sync/reports) but flags both as unconfirmed
	// ("suggested," not final) and architecture-backend.md leaves the
	// rate-limiter's overall scope/thresholds as an open question. This
	// default (100 req/min per-IP, applied to the whole stack rather than
	// per-route) is a reasonable placeholder until that is resolved.
	DefaultRateLimitRequestsPerMinute = 100

	// DefaultShutdownTimeout bounds how long graceful shutdown waits for
	// in-flight requests to finish.
	DefaultShutdownTimeout = 10 * time.Second
	// DefaultReadyzDBPingTimeout bounds how long /readyz waits on a
	// database ping before reporting unavailable.
	DefaultReadyzDBPingTimeout = 5 * time.Second
)

// Config holds rize-backend's runtime configuration, loaded from
// environment variables at process startup.
type Config struct {
	// HTTPPort is the TCP port the HTTP server listens on.
	HTTPPort string
	// Environment names the deployment environment, e.g. "development",
	// "staging", "production". It is informational (used in logging and,
	// in the future, to gate environment-specific behavior) and does not
	// itself change config validation.
	Environment string
	// DatabaseURL is the PostgreSQL/TimescaleDB connection string. It may
	// be empty in local scaffolding contexts (e.g. running the binary
	// without a database); /readyz reports "not_configured" in that case.
	DatabaseURL string

	// JWTSigningKey is the key material used to sign and verify access
	// tokens (JWTs), per documentation/security.md §Token model. That
	// document pins the token model (15-minute JWT access tokens; opaque,
	// hashed-at-rest, 30-day refresh tokens) but leaves the exact signing
	// algorithm (RS256 or EdDSA) as an open question, so this field is
	// deliberately untyped key material (e.g. a PEM-encoded key) rather
	// than a fixed-format HMAC secret. No JWT issuance/verification logic
	// is implemented by this ticket (RIZ-30); this field only exists so a
	// future auth ticket has a config slot to read from. Refresh tokens
	// are opaque strings, not JWTs, and are stored hashed at rest, so
	// there is no corresponding "refresh signing secret" in
	// documentation/security.md's model.
	JWTSigningKey string

	// CORSAllowedOrigins is the list of origins permitted to make
	// cross-origin requests, per documentation/security.md §API hardening
	// ("CORS is locked to known origins ... there is no wildcard (*) CORS
	// origin in any environment"). Defaults to "*" for local development
	// only; any deployed environment must set this explicitly.
	CORSAllowedOrigins []string

	// RateLimitRequestsPerMinute is the per-IP request budget enforced by
	// the rate-limit middleware.
	RateLimitRequestsPerMinute int

	// ShutdownTimeout bounds graceful shutdown.
	ShutdownTimeout time.Duration
	// ReadyzDBPingTimeout bounds the /readyz database ping.
	ReadyzDBPingTimeout time.Duration
}

// Load builds a Config from environment variables, applying defaults for
// anything unset. It returns an error if a set environment variable has an
// invalid value (e.g. a non-numeric rate limit).
func Load() (Config, error) {
	cfg := Config{
		HTTPPort:                   getEnvDefault("PORT", DefaultHTTPPort),
		Environment:                getEnvDefault("ENVIRONMENT", DefaultEnvironment),
		DatabaseURL:                os.Getenv("DATABASE_URL"),
		JWTSigningKey:              os.Getenv("JWT_SIGNING_KEY"),
		CORSAllowedOrigins:         parseCSV(getEnvDefault("CORS_ALLOWED_ORIGINS", DefaultCORSAllowedOrigins)),
		RateLimitRequestsPerMinute: DefaultRateLimitRequestsPerMinute,
		ShutdownTimeout:            DefaultShutdownTimeout,
		ReadyzDBPingTimeout:        DefaultReadyzDBPingTimeout,
	}

	if v := os.Getenv("RATE_LIMIT_REQUESTS_PER_MINUTE"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return Config{}, fmt.Errorf("invalid RATE_LIMIT_REQUESTS_PER_MINUTE %q: %w", v, err)
		}
		if n <= 0 {
			return Config{}, fmt.Errorf("invalid RATE_LIMIT_REQUESTS_PER_MINUTE %q: must be positive", v)
		}
		cfg.RateLimitRequestsPerMinute = n
	}

	if v := os.Getenv("SHUTDOWN_TIMEOUT_SECONDS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return Config{}, fmt.Errorf("invalid SHUTDOWN_TIMEOUT_SECONDS %q: %w", v, err)
		}
		if n <= 0 {
			return Config{}, fmt.Errorf("invalid SHUTDOWN_TIMEOUT_SECONDS %q: must be positive", v)
		}
		cfg.ShutdownTimeout = time.Duration(n) * time.Second
	}

	if cfg.HTTPPort == "" {
		return Config{}, fmt.Errorf("PORT must not be empty")
	}

	return cfg, nil
}

func getEnvDefault(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func parseCSV(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
