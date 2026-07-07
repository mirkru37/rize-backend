package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/go-chi/chi/v5"

	"github.com/mirkru37/rize-backend/internal/config"
)

// openAPISpecPath is the hand-maintained OpenAPI 3 spec for RIZ-51
// (documentation/api-reference.md's implementation-facing counterpart).
// It lives at the repo root's openapi/ directory (not api/ — that name is
// reserved by .gitignore for the compiled `go build -o api` binary), one
// level above cmd/api.
const openAPISpecPath = "../../openapi/openapi.yaml"

// routeKey is a normalized (method, path) pair used to compare the routes
// actually registered by newRouter against the paths/operations declared in
// openapi/openapi.yaml.
type routeKey struct {
	method string
	path   string
}

func (k routeKey) String() string {
	return fmt.Sprintf("%s %s", k.method, k.path)
}

// normalizePath strips the trailing slash chi.Walk reports for routes
// registered via a subrouter's `r.Get("/", ...)` (e.g. "/v1/projects/"),
// so the comparison isn't sensitive to that routing-library artifact. The
// spec itself is authored without trailing slashes.
func normalizePath(p string) string {
	if p != "/" && strings.HasSuffix(p, "/") {
		return strings.TrimSuffix(p, "/")
	}
	return p
}

// TestOpenAPISpecIsValid checks that openapi/openapi.yaml is a well-formed,
// semantically valid OpenAPI 3 document. This is the "invalid spec" half of
// RIZ-51's CI drift gate.
func TestOpenAPISpecIsValid(t *testing.T) {
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile(openAPISpecPath)
	if err != nil {
		t.Fatalf("failed to load %s: %v", openAPISpecPath, err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatalf("%s failed OpenAPI validation: %v", openAPISpecPath, err)
	}
}

// TestOpenAPISpecMatchesRoutes is RIZ-51's route-conformance drift check: it
// walks the actual chi router built by newRouter and asserts that its route
// table (method + path) is exactly the set of operations declared in
// openapi/openapi.yaml — no more, no less. A handler added to the router
// without a matching spec entry, or a spec entry for a route that was
// removed/renamed in code, fails this test.
//
// /metrics is special-cased: it's served by promhttp.Handler via
// r.Handle, which chi registers against every HTTP method (including
// TRACE/CONNECT), not just GET. Only GET /metrics is meaningful and
// documented; the other methods are a routing-library artifact, not part
// of the API surface.
func TestOpenAPISpecMatchesRoutes(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.Config{Environment: config.DefaultEnvironment}
	router, ok := newRouter(logger, cfg, nil).(chi.Router)
	if !ok {
		t.Fatalf("newRouter did not return a chi.Router")
	}

	actual := map[routeKey]bool{}
	err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		path := normalizePath(route)
		if path == "/metrics" && method != http.MethodGet {
			return nil
		}
		actual[routeKey{method: method, path: path}] = true
		return nil
	})
	if err != nil {
		t.Fatalf("chi.Walk failed: %v", err)
	}

	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile(openAPISpecPath)
	if err != nil {
		t.Fatalf("failed to load %s: %v", openAPISpecPath, err)
	}

	documented := map[routeKey]bool{}
	for path, item := range doc.Paths.Map() {
		for method := range item.Operations() {
			documented[routeKey{method: method, path: normalizePath(path)}] = true
		}
	}

	var missingFromSpec, extraInSpec []string
	for k := range actual {
		if !documented[k] {
			missingFromSpec = append(missingFromSpec, k.String())
		}
	}
	for k := range documented {
		if !actual[k] {
			extraInSpec = append(extraInSpec, k.String())
		}
	}
	sort.Strings(missingFromSpec)
	sort.Strings(extraInSpec)

	if len(missingFromSpec) > 0 || len(extraInSpec) > 0 {
		var b strings.Builder
		b.WriteString("openapi/openapi.yaml has drifted from the routes registered in cmd/api.newRouter:\n")
		if len(missingFromSpec) > 0 {
			b.WriteString("  routes registered in code but missing from the spec:\n")
			for _, r := range missingFromSpec {
				fmt.Fprintf(&b, "    - %s\n", r)
			}
		}
		if len(extraInSpec) > 0 {
			b.WriteString("  routes documented in the spec but not registered in code:\n")
			for _, r := range extraInSpec {
				fmt.Fprintf(&b, "    - %s\n", r)
			}
		}
		b.WriteString("Update openapi/openapi.yaml (and documentation/api-reference.md if the contract itself changed) to match.\n")
		t.Fatal(b.String())
	}
}
