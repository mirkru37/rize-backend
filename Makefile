.PHONY: build test coverage lint vuln run ci deploy-bootstrap-check api-docs api-docs-lint api-docs-check

# RIZ-69: single source of truth for the minimum acceptable total statement
# coverage, mirrored by .github/workflows/ci.yml's COVERAGE_THRESHOLD env
# var (see that file's comment: set to 80 per explicit user decision,
# 2026-07-07). Override locally with `make coverage COVERAGE_THRESHOLD=95`.
COVERAGE_THRESHOLD ?= 80

build:
	go build ./...

test:
	go test -race ./...

# coverage mirrors CI's Test job locally: run the full suite (DB-backed
# tests skip themselves if DATABASE_URL is unset — `make coverage
# DATABASE_URL=postgres://rize:rize@localhost:5432/rize?sslmode=disable`
# after `docker compose up -d db migrate` to include them), strip
# sqlc-generated internal/store/storedb from the profile (see the workflow
# comment for why), print the per-function table, emit an HTML report, and
# fail if total statement coverage is below COVERAGE_THRESHOLD.
coverage:
	go test -race -coverprofile=coverage.out -covermode=atomic ./...
	grep -v '/internal/store/storedb/' coverage.out > coverage.out.filtered
	mv coverage.out.filtered coverage.out
	go tool cover -func=coverage.out
	go tool cover -html=coverage.out -o coverage.html
	@total=$$(go tool cover -func=coverage.out | grep '^total:' | awk '{print $$3}' | tr -d '%'); \
	echo "Total coverage: $$total% (threshold: $(COVERAGE_THRESHOLD)%)"; \
	awk -v t="$$total" -v thresh="$(COVERAGE_THRESHOLD)" 'BEGIN { if (t+0 < thresh+0) { exit 1 } }' \
		|| { echo "coverage $$total% is below the $(COVERAGE_THRESHOLD)% threshold"; exit 1; }

lint:
	golangci-lint run

vuln:
	govulncheck ./...

run:
	go run ./cmd/api

ci: build test lint vuln

# RIZ-51: openapi/openapi.yaml is the hand-maintained OpenAPI 3 spec for the
# routes registered in cmd/api.newRouter; cmd/api/openapi_conformance_test.go
# (run as part of `make test` / `go test ./...`) is the drift check — it
# fails if the spec and the actual route table disagree. These targets
# render/lint/preview the spec locally; they shell out to @redocly/cli via
# npx rather than vendoring a Node toolchain into this Go repo, matching
# .github/workflows/api-docs.yml.
api-docs-lint:
	npx --yes @redocly/cli@2.37.0 lint openapi/openapi.yaml

# Renders the static Redoc docs site to site/index.html for local preview
# (open it directly in a browser; nothing here needs a running server).
api-docs:
	mkdir -p site
	npx --yes @redocly/cli@2.37.0 build-docs openapi/openapi.yaml -o site/index.html
	@echo "Docs rendered to site/index.html"

# api-docs-check is the local equivalent of the CI docs job's drift gate:
# lint the spec, run the route-conformance test, and render the docs site
# (failing the render is itself a signal the spec is malformed beyond what
# lint/validate catch).
api-docs-check: api-docs-lint
	go test ./cmd/api/... -run TestOpenAPI -v
	$(MAKE) api-docs

# deploy-bootstrap-check prints which of the GCP deploy secrets/vars
# (see docs/deployment.md) are currently set on this repo, so you can
# self-check bootstrap progress before pushing to main / publishing a
# release. Requires `gh` to be authenticated against this repo.
deploy-bootstrap-check:
	@echo "Checking GCP deploy bootstrap status for $$(gh repo view --json nameWithOwner -q .nameWithOwner)..."
	@echo
	@echo "Repo secrets:"
	@for s in GCP_PROJECT_ID GCP_WIF_PROVIDER GCP_SERVICE_ACCOUNT GCP_REGION \
	          INTEGRATION_DATABASE_URL_SECRET PRODUCTION_DATABASE_URL_SECRET \
	          INTEGRATION_JWT_SIGNING_KEY_SECRET PRODUCTION_JWT_SIGNING_KEY_SECRET; do \
		if gh secret list --json name -q '.[].name' | grep -qx "$$s"; then \
			echo "  [x] $$s"; \
		else \
			echo "  [ ] $$s (missing)"; \
		fi; \
	done
	@echo
	@echo "Repo variables (optional, e.g. CORS origins):"
	@gh variable list --json name -q '.[].name' 2>/dev/null | sed 's/^/  - /' || echo "  (none set)"
