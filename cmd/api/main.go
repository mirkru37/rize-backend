// Command api is the rize-backend process entrypoint: it wires the HTTP
// middleware stack and routes, starts the server, and shuts it down
// gracefully on SIGINT/SIGTERM.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultPort         = "8080"
	shutdownTimeout     = 10 * time.Second
	readyzDBPingTimeout = 5 * time.Second
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	if err := run(logger); err != nil {
		logger.Error("server exited with error", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var pool *pgxpool.Pool
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		p, err := pgxpool.New(ctx, dsn)
		if err != nil {
			return err
		}
		pool = p
		defer pool.Close()
	}

	router := newRouter(logger, pool)

	port := os.Getenv("PORT")
	if port == "" {
		port = defaultPort
	}

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("starting server", "addr", srv.Addr)
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

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}

	logger.Info("server shut down cleanly")
	return nil
}

// newRouter builds the Chi router with the middleware chain applied in
// order: RequestID -> RealIP -> structured logging -> Recoverer.
func newRouter(logger *slog.Logger, pool *pgxpool.Pool) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP) //nolint:staticcheck // RealIP is required by the mandated middleware order; the service does not sit behind an untrusted proxy at this scaffolding stage.
	r.Use(requestLogger(logger))
	r.Use(middleware.Recoverer)

	r.Get("/healthz", healthzHandler)
	r.Get("/readyz", readyzHandler(pool))

	return r
}

// requestLogger returns middleware that logs each request with structured
// (slog) key-value fields once the request has completed.
func requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, req.ProtoMajor)

			next.ServeHTTP(ww, req)

			logger.Info("request",
				"method", req.Method,
				"path", req.URL.Path,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", middleware.GetReqID(req.Context()),
			)
		})
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func healthzHandler(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyzHandler reports readiness: if DATABASE_URL is configured it pings
// the database via the pgx pool and reports 200/503 accordingly; if no
// database is configured it reports 200 with db: "not_configured".
func readyzHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if pool == nil {
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "db": "not_configured"})
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), readyzDBPingTimeout)
		defer cancel()

		if err := pool.Ping(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "db": "unreachable"})
			return
		}

		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "db": "ok"})
	}
}
