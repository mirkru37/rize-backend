// Command api is the rize-backend process entrypoint: it wires the HTTP
// middleware stack and routes, starts the server, and shuts it down
// gracefully on SIGINT/SIGTERM.
package main

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/mirkru37/rize-backend/internal/activities"
	"github.com/mirkru37/rize-backend/internal/auth"
	"github.com/mirkru37/rize-backend/internal/categories"
	"github.com/mirkru37/rize-backend/internal/config"
	"github.com/mirkru37/rize-backend/internal/focussessions"
	"github.com/mirkru37/rize-backend/internal/httpx"
	appmw "github.com/mirkru37/rize-backend/internal/middleware"
	"github.com/mirkru37/rize-backend/internal/projects"
	"github.com/mirkru37/rize-backend/internal/reports"
	"github.com/mirkru37/rize-backend/internal/store"
	"github.com/mirkru37/rize-backend/internal/store/storedb"
	"github.com/mirkru37/rize-backend/internal/sync"
	"github.com/mirkru37/rize-backend/internal/tags"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	if err := run(logger, cfg); err != nil {
		logger.Error("server exited with error", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger, cfg config.Config) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := store.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	if pool != nil {
		defer pool.Close()
	}

	router := newRouter(logger, cfg, pool)

	srv := &http.Server{
		Addr:              ":" + cfg.HTTPPort,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("starting server", "addr", srv.Addr, "environment", cfg.Environment)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}

	logger.Info("server shut down cleanly")
	return nil
}

// newRouter builds the Chi router with the middleware chain applied in the
// exact order mandated by documentation/architecture-backend.md §Middleware
// Stack: RequestID -> Logging -> Recoverer -> CORS -> RateLimit -> Auth ->
// RBAC. Ops endpoints (/healthz, /readyz, /metrics) are unversioned and sit
// outside that stack's CORS/rate-limit/auth concerns per
// documentation/api-reference.md §Ops.
//
// RIZ-30 fix note: chi's RealIP middleware was removed from this stack —
// see the package doc comment in internal/middleware/doc.go for why.
//
// RIZ-32: the JWT signing key drives both the Auth middleware (verification,
// via its public half) and the auth service (issuance, via the private
// key). If JWT_SIGNING_KEY is unset, an ephemeral key is generated for
// "development" only (see loadOrGenerateSigningKey) so a fresh checkout can
// exercise auth locally without extra setup; every other environment must
// configure JWT_SIGNING_KEY explicitly and newRouter panics if it doesn't
// parse, since a broken signing key is a fatal misconfiguration rather than
// something the server can run degraded through.
func newRouter(logger *slog.Logger, cfg config.Config, pool *pgxpool.Pool) http.Handler {
	r := chi.NewRouter()

	r.Use(appmw.RequestID)
	r.Use(appmw.Logging(logger))
	r.Use(appmw.Recoverer)
	r.Use(httpx.Metrics())

	r.NotFound(notFoundHandler)
	r.MethodNotAllowed(methodNotAllowedHandler)

	r.Get("/healthz", healthzHandler)
	r.Get("/readyz", readyzHandler(pool, cfg.ReadyzDBPingTimeout))
	r.Handle("/metrics", promhttp.Handler())

	signingKey, err := loadOrGenerateSigningKey(cfg)
	if err != nil {
		logger.Error("failed to load JWT signing key", "error", err)
		panic(err)
	}

	var queries storedb.Querier
	var beginner auth.Beginner
	if pool != nil {
		queries = storedb.New(pool)
		beginner = pool
	}
	authService := &auth.Service{Queries: queries, Pool: beginner, SigningKey: signingKey}
	authHandler := auth.NewHandler(authService)

	var syncBeginner sync.Beginner
	if pool != nil {
		syncBeginner = pool
	}
	syncService := &sync.Service{Queries: queries, Pool: syncBeginner}
	syncHandler := sync.NewHandler(syncService)

	projectsService := &projects.Service{Queries: queries}
	projectsHandler := projects.NewHandler(projectsService)

	tagsService := &tags.Service{Queries: queries}
	tagsHandler := tags.NewHandler(tagsService)

	categoriesService := &categories.Service{Queries: queries}
	categoriesHandler := categories.NewHandler(categoriesService)

	focusSessionsService := &focussessions.Service{Queries: queries}
	focusSessionsHandler := focussessions.NewHandler(focusSessionsService)

	activitiesService := &activities.Service{Queries: queries}
	activitiesHandler := activities.NewHandler(activitiesService)

	reportsService := &reports.Service{Queries: queries}
	reportsHandler := reports.NewHandler(reportsService)

	r.Route("/v1", func(r chi.Router) {
		r.Use(appmw.CORS(appmw.CORSConfig{AllowedOrigins: cfg.CORSAllowedOrigins}))
		r.Use(appmw.RateLimit(cfg.RateLimitRequestsPerMinute))

		authenticate := appmw.Authenticate(&signingKey.PublicKey)
		requireAdmin := appmw.RequireRole("admin")

		auth.RegisterRoutes(r, authHandler, authenticate, requireAdmin)
		sync.RegisterRoutes(r, syncHandler, authenticate)
		projects.RegisterRoutes(r, projectsHandler, authenticate)
		tags.RegisterRoutes(r, tagsHandler, authenticate)
		categories.RegisterRoutes(r, categoriesHandler, authenticate)
		focussessions.RegisterRoutes(r, focusSessionsHandler, authenticate)
		activities.RegisterRoutes(r, activitiesHandler, authenticate)
		reports.RegisterRoutes(r, reportsHandler, authenticate)
	})

	return r
}

// loadOrGenerateSigningKey resolves the RSA private key used to sign/verify
// access tokens from cfg.JWTSigningKey, or generates an ephemeral one when
// unset in the "development" environment. See config.Config.JWTSigningKey
// and auth.LoadSigningKey/auth.GenerateSigningKey for the underlying
// parsing/generation logic and the RS256 algorithm-pinning rationale.
func loadOrGenerateSigningKey(cfg config.Config) (*rsa.PrivateKey, error) {
	if cfg.JWTSigningKey != "" {
		return auth.LoadSigningKey(cfg.JWTSigningKey)
	}
	if cfg.Environment != config.DefaultEnvironment {
		return nil, fmt.Errorf("JWT_SIGNING_KEY must be set in %q", cfg.Environment)
	}
	return auth.GenerateSigningKey()
}

// notFoundHandler writes the standard RFC 7807-style Problem body for
// requests that don't match any registered route, instead of chi's default
// plain-text 404 body, per documentation/api-reference.md §Conventions
// ("every error response uses the Problem envelope").
func notFoundHandler(w http.ResponseWriter, r *http.Request) {
	httpx.WriteError(w, r, http.StatusNotFound,
		"https://api.rize-clone.example/errors/not-found",
		"Not Found",
		"The requested resource does not exist.",
	)
}

// methodNotAllowedHandler writes the standard RFC 7807-style Problem body
// for requests whose method isn't allowed on an otherwise-matched route,
// instead of chi's default plain-text 405 body.
func methodNotAllowedHandler(w http.ResponseWriter, r *http.Request) {
	httpx.WriteError(w, r, http.StatusMethodNotAllowed,
		"https://api.rize-clone.example/errors/method-not-allowed",
		"Method Not Allowed",
		"The HTTP method is not allowed for the requested resource.",
	)
}

func healthzHandler(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, r, http.StatusOK, map[string]string{"status": "ok"})
}

// readyzHandler reports readiness: if a database pool is configured it
// pings the database and reports 200/503 accordingly; if no database is
// configured it reports 200 with db: "not_configured".
func readyzHandler(pool *pgxpool.Pool, pingTimeout time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if pool == nil {
			httpx.WriteJSON(w, r, http.StatusOK, map[string]string{"status": "ok", "db": "not_configured"})
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), pingTimeout)
		defer cancel()

		if err := pool.Ping(ctx); err != nil {
			httpx.WriteJSON(w, r, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "db": "unreachable"})
			return
		}

		httpx.WriteJSON(w, r, http.StatusOK, map[string]string{"status": "ok", "db": "ok"})
	}
}
