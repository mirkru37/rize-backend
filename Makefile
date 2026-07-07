.PHONY: build test coverage lint vuln run ci deploy-bootstrap-check

# RIZ-69: single source of truth for the minimum acceptable total statement
# coverage, mirrored by .github/workflows/ci.yml's COVERAGE_THRESHOLD env
# var. Override locally with `make coverage COVERAGE_THRESHOLD=95`.
COVERAGE_THRESHOLD ?= 90

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
