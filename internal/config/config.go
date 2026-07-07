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

	// DefaultReadTimeout bounds how long the server waits to read an
	// entire request (headers and body) before aborting it, protecting
	// against slow-client resource exhaustion.
	DefaultReadTimeout = 10 * time.Second
	// DefaultWriteTimeout bounds how long the server waits to write a
	// response before aborting the connection.
	DefaultWriteTimeout = 30 * time.Second
	// DefaultIdleTimeout bounds how long the server keeps an idle
	// keep-alive connection open before closing it.
	DefaultIdleTimeout = 120 * time.Second

	// DefaultAuthLockoutThreshold is used when AUTH_LOCKOUT_THRESHOLD is
	// not set: the number of consecutive failed login attempts against a
	// single account that triggers a lockout, per RIZ-59 /
	// documentation/security.md §API hardening ("brute-force lockout on
	// login").
	DefaultAuthLockoutThreshold = 10
	// DefaultAuthLockoutBaseDuration is used when AUTH_LOCKOUT_BASE_DURATION
	// is not set: the lockout duration applied the first time an account is
	// locked. Each subsequent lockout on the same account doubles the
	// previous duration, capped at DefaultAuthLockoutMaxDuration.
	DefaultAuthLockoutBaseDuration = 15 * time.Minute
	// DefaultAuthLockoutMaxDuration is used when AUTH_LOCKOUT_MAX_DURATION
	// is not set: the ceiling the doubling lockout duration is capped at.
	DefaultAuthLockoutMaxDuration = 24 * time.Hour
)

// EnvVarNames lists every environment variable Load reads. It exists so
// tooling and tests (see internal/config/env_example_test.go) can verify
// .env.example stays in sync with what Load actually consumes; it has no
// effect on Load's own behavior.
var EnvVarNames = []string{
	"PORT",
	"ENVIRONMENT",
	"DATABASE_URL",
	"JWT_SIGNING_KEY",
	"CORS_ALLOWED_ORIGINS",
	"RATE_LIMIT_REQUESTS_PER_MINUTE",
	"SHUTDOWN_TIMEOUT_SECONDS",
	"AUTH_LOCKOUT_THRESHOLD",
	"AUTH_LOCKOUT_BASE_DURATION",
	"AUTH_LOCKOUT_MAX_DURATION",
}

// Config holds rize-backend's runtime configuration, loaded from
// environment variables at process startup.
type Config struct {
	// HTTPPort is the TCP port the HTTP server listens on.
	HTTPPort string
	// Environment names the deployment environment: one of "development",
	// "staging", or "production" (Load rejects any other value). Besides
	// being informational (used in logging), it gates the wildcard-CORS
	// validation below — only "development" may resolve to a wildcard
	// CORSAllowedOrigins.
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
	// only; Load fails fast if it resolves to a wildcard in any
	// environment other than "development" (including via the unset-var
	// default), so a deployed environment cannot start without an
	// explicit origin list.
	CORSAllowedOrigins []string

	// RateLimitRequestsPerMinute is the per-IP request budget enforced by
	// the rate-limit middleware.
	RateLimitRequestsPerMinute int

	// ShutdownTimeout bounds graceful shutdown.
	ShutdownTimeout time.Duration
	// ReadyzDBPingTimeout bounds the /readyz database ping.
	ReadyzDBPingTimeout time.Duration

	// ReadTimeout bounds how long the HTTP server waits to read an entire
	// request before aborting it.
	ReadTimeout time.Duration
	// WriteTimeout bounds how long the HTTP server waits to write a
	// response before aborting the connection.
	WriteTimeout time.Duration
	// IdleTimeout bounds how long the HTTP server keeps an idle
	// keep-alive connection open before closing it.
	IdleTimeout time.Duration

	// AuthLockoutThreshold is the number of consecutive failed login
	// attempts against a single account that triggers a lockout, per
	// documentation/security.md §API hardening.
	AuthLockoutThreshold int
	// AuthLockoutBaseDuration is the lockout duration applied the first
	// time an account is locked; each subsequent lockout on the same
	// account doubles the previous duration, capped at
	// AuthLockoutMaxDuration.
	AuthLockoutBaseDuration time.Duration
	// AuthLockoutMaxDuration caps the doubling lockout duration.
	AuthLockoutMaxDuration time.Duration
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
		ReadTimeout:                DefaultReadTimeout,
		WriteTimeout:               DefaultWriteTimeout,
		IdleTimeout:                DefaultIdleTimeout,
		AuthLockoutThreshold:       DefaultAuthLockoutThreshold,
		AuthLockoutBaseDuration:    DefaultAuthLockoutBaseDuration,
		AuthLockoutMaxDuration:     DefaultAuthLockoutMaxDuration,
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

	if v := os.Getenv("AUTH_LOCKOUT_THRESHOLD"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return Config{}, fmt.Errorf("invalid AUTH_LOCKOUT_THRESHOLD %q: %w", v, err)
		}
		if n <= 0 {
			return Config{}, fmt.Errorf("invalid AUTH_LOCKOUT_THRESHOLD %q: must be positive", v)
		}
		cfg.AuthLockoutThreshold = n
	}

	if v := os.Getenv("AUTH_LOCKOUT_BASE_DURATION"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("invalid AUTH_LOCKOUT_BASE_DURATION %q: %w", v, err)
		}
		if d <= 0 {
			return Config{}, fmt.Errorf("invalid AUTH_LOCKOUT_BASE_DURATION %q: must be positive", v)
		}
		cfg.AuthLockoutBaseDuration = d
	}

	if v := os.Getenv("AUTH_LOCKOUT_MAX_DURATION"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("invalid AUTH_LOCKOUT_MAX_DURATION %q: %w", v, err)
		}
		if d <= 0 {
			return Config{}, fmt.Errorf("invalid AUTH_LOCKOUT_MAX_DURATION %q: must be positive", v)
		}
		cfg.AuthLockoutMaxDuration = d
	}

	if cfg.AuthLockoutMaxDuration < cfg.AuthLockoutBaseDuration {
		return Config{}, fmt.Errorf(
			"invalid AUTH_LOCKOUT_MAX_DURATION %q: must be >= AUTH_LOCKOUT_BASE_DURATION %q",
			cfg.AuthLockoutMaxDuration, cfg.AuthLockoutBaseDuration,
		)
	}

	if cfg.HTTPPort == "" {
		return Config{}, fmt.Errorf("PORT must not be empty")
	}

	if !validEnvironments[cfg.Environment] {
		return Config{}, fmt.Errorf("invalid ENVIRONMENT %q: must be one of development, staging, production", cfg.Environment)
	}

	if cfg.Environment != DefaultEnvironment && containsWildcard(cfg.CORSAllowedOrigins) {
		return Config{}, fmt.Errorf(
			"CORS_ALLOWED_ORIGINS must not contain %q outside %q; per documentation/security.md §API hardening, "+
				"CORS must be locked to known origins in any deployed environment (got environment %q)",
			corsWildcard, DefaultEnvironment, cfg.Environment,
		)
	}

	return cfg, nil
}

// corsWildcard is the CORS "allow every origin" value that is only
// tolerated in the development environment; see containsWildcard's use in
// Load.
const corsWildcard = "*"

// validEnvironments is the closed set of values ENVIRONMENT may take. Load's
// wildcard-CORS guard below depends on knowing exactly which value means
// "development", so an unrecognized ENVIRONMENT value is rejected outright
// rather than silently treated as non-development (or as development).
var validEnvironments = map[string]bool{
	DefaultEnvironment: true,
	"staging":          true,
	"production":       true,
}

// containsWildcard reports whether origins contains the CORS wildcard
// value.
func containsWildcard(origins []string) bool {
	for _, o := range origins {
		if o == corsWildcard {
			return true
		}
	}
	return false
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
