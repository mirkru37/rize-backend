.PHONY: build test lint vuln run ci deploy-bootstrap-check

build:
	go build ./...

test:
	go test -race ./...

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
