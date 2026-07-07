package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strconv"
	"testing"
	"time"
)

// TestServeGracefulShutdown asserts serve() returns nil once its context is
// canceled, having shut the server down cleanly, exercising the ctx.Done()
// branch and the subsequent graceful-shutdown path — the part of run()
// that's otherwise only reachable via an actual OS signal.
func TestServeGracefulShutdown(t *testing.T) {
	cfg := testConfig()
	cfg.HTTPPort = "0" // ask the OS for a free ephemeral port

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- serve(ctx, slog.Default(), cfg, nil)
	}()

	// Give the server a moment to start listening before triggering
	// shutdown, so this exercises the "running, then told to stop" path
	// rather than possibly racing ListenAndServe's own startup.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("serve() = %v, want nil (clean shutdown)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve() did not return within 5s of ctx cancellation")
	}
}

// TestServeListenAndServeError asserts serve() surfaces a listen failure
// (here: the configured port is already bound by another listener) as an
// error via the errCh branch, rather than blocking forever or panicking.
func TestServeListenAndServeError(t *testing.T) {
	// Bind on all interfaces (":0"), matching serve()'s own
	// Addr: ":"+cfg.HTTPPort, so the subsequent ListenAndServe on the same
	// port is guaranteed to collide regardless of platform-specific
	// 127.0.0.1-vs-0.0.0.0 binding semantics. This binds an OS-assigned
	// ephemeral port purely to occupy it for the duration of this test,
	// not a service exposed to the network.
	listener, err := net.Listen("tcp", ":0") //nolint:gosec // test fixture, occupies a port to force a collision
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer func() {
		if err := listener.Close(); err != nil {
			t.Logf("listener.Close: %v", err)
		}
	}()

	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)

	cfg := testConfig()
	cfg.HTTPPort = port

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = serve(ctx, slog.Default(), cfg, nil)
	if err == nil {
		t.Fatal("serve() = nil, want an error (port already in use)")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("serve() = %v, want a listen error surfaced promptly, not a context timeout", err)
	}
}

// TestRunReturnsPoolCreationError exercises run()'s
// store.NewPool-error-return branch — the one part of run() reachable
// without an actual OS signal — using a malformed DATABASE_URL that
// store.NewPool rejects synchronously (pgxpool.ParseConfig failure)
// before ever attempting a connection, so this stays fast and
// deterministic.
func TestRunReturnsPoolCreationError(t *testing.T) {
	cfg := testConfig()
	cfg.DatabaseURL = "://not-a-valid-dsn"

	err := run(slog.Default(), cfg)
	if err == nil {
		t.Fatal("run() with a malformed DATABASE_URL = nil, want an error")
	}
}
