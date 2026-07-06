# Deployment (RIZ-36)

`rize-backend` deploys to Google Cloud Run on Google Cloud's free tier: every merge to `main` deploys to an **integration** environment, and publishing a GitHub Release deploys to **production**. This document describes the environments, the promotion flow, the one-time bootstrap procedure, every secret/var the workflows consume, how migrations run as part of a deploy, and free-tier considerations for the database.

CI (`.github/workflows/ci.yml`) is unaffected by any of this — it still runs lint/test/vuln/docker-build on every PR and push to `main`, unrelated to deployment.

## Environments

| | Integration | Production |
|---|---|---|
| Trigger | Push to `main` (`.github/workflows/deploy-integration.yml`) | GitHub Release published (`.github/workflows/deploy-production.yml`) |
| Cloud Run service | `rize-backend-integration` | `rize-backend-production` |
| Cloud Run migration Job | `rize-backend-migrate-integration` | `rize-backend-migrate-production` |
| Database | Value of `INTEGRATION_DATABASE_URL_SECRET` | Value of `PRODUCTION_DATABASE_URL_SECRET` |
| `ENVIRONMENT` value | `staging` | `production` |
| Image source | Built fresh from the pushed commit | The *same* image already built and pushed by `deploy-integration.yml` for the release's target commit — never rebuilt |

> **Assumption on `ENVIRONMENT`**: `internal/config/config.go`'s `validEnvironments` set is exactly `{development, staging, production}` — there is no literal `"integration"` value accepted by `config.Load()`. The integration Cloud Run service therefore runs with `ENVIRONMENT=staging`, which is the closest semantic fit (a non-development, non-production deployed environment) and satisfies the same-file rule that any non-`development` environment must not resolve to a wildcard CORS origin.

## Promotion flow

```
PR merged to main
      │
      ▼
deploy-integration.yml
  1. guard: GCP_PROJECT_ID set?  no -> skip cleanly, workflow succeeds
  2. build-and-push: build api + migrate images, tag with <git-sha> and "integration", push to Artifact Registry
  3. migrate: run rize-backend-migrate-integration Cloud Run Job (image tag = git sha), wait for success
  4. deploy: gcloud run deploy rize-backend-integration with the api image tag = git sha
      │
      │  (time passes; integration is validated)
      ▼
GitHub Release published (tag pointing at a commit already on main)
      │
      ▼
deploy-production.yml
  1. guard: GCP_PROJECT_ID set?  no -> skip cleanly, workflow succeeds
  2. resolve-image: resolve the release tag to its commit SHA; verify an
     integration build already exists in Artifact Registry for that SHA
     (both api and migrate images) — fails loudly if not
  3. migrate: run rize-backend-migrate-production Cloud Run Job against the
     SAME migrate image tag = that SHA, wait for success
  4. deploy: gcloud run deploy rize-backend-production with the SAME api
     image tag = that SHA (no rebuild)
```

Production never builds its own image. If you publish a release for a commit that never landed on `main` (or landed but `deploy-integration.yml` hasn't finished/succeeded for it yet), `deploy-production.yml`'s `resolve-image` job fails with an explicit error telling you to land the commit on `main`, wait for the integration deploy to succeed, and then re-publish the release.

In both workflows, the migration Job is deployed and executed with `--wait` **before** the Cloud Run service deploy step runs, so a failed migration blocks the traffic-affecting deploy.

## Secrets and variables

All of the following are **GitHub Actions repository secrets** (`gh secret set ...`), except the two CORS variables, which are plain **repository variables** (`gh variable set ...`) since they are non-sensitive:

| Name | Kind | Where the value comes from |
|---|---|---|
| `GCP_PROJECT_ID` | secret | The GCP project ID created/chosen during bootstrap. Also acts as the deploy on/off switch: both workflows guard on this being set. |
| `GCP_WIF_PROVIDER` | secret | Full resource name of the Workload Identity Federation provider, e.g. `projects/<number>/locations/global/workloadIdentityPools/github-pool/providers/github-provider` (from bootstrap step 6). |
| `GCP_SERVICE_ACCOUNT` | secret | Email of the deployer service account created in bootstrap step 3, e.g. `rize-backend-deployer@<project>.iam.gserviceaccount.com`. |
| `GCP_REGION` | secret | The Cloud Run / Artifact Registry region chosen during bootstrap, e.g. `us-central1`. |
| `INTEGRATION_DATABASE_URL_SECRET` | secret | Secret Manager reference (not the connection string itself) in the form `projects/<project>/secrets/<name>:latest`, pointing at the integration DB connection string secret created in bootstrap. |
| `PRODUCTION_DATABASE_URL_SECRET` | secret | Same, pointing at the production DB connection string secret. |
| `JWT_SIGNING_KEY_SECRET` | secret | Secret Manager reference (same format) pointing at the PEM-encoded RSA signing key secret, shared by both environments unless you choose to split it. |
| `INTEGRATION_CORS_ALLOWED_ORIGINS` | variable | Comma-separated list of allowed CORS origins for the integration service (e.g. your staging frontend URL). Per `documentation/security.md` §API hardening, this must never be `*` outside `development`. |
| `PRODUCTION_CORS_ALLOWED_ORIGINS` | variable | Comma-separated list of allowed CORS origins for production. |

**On the Secret Manager reference format**: the two `*_SECRET`-suffixed values and `JWT_SIGNING_KEY_SECRET` must be in the form `projects/<project>/secrets/<name>:<version>` (colon before the version, e.g. `:latest`) — this is exactly what `gcloud run deploy --set-secrets=ENV_VAR=<value>` and `gcloud run jobs deploy --set-secrets=ENV_VAR=<value>` expect. This is subtly different from the Secret Manager API's own resource-name format for a specific secret version (`projects/<project>/secrets/<name>/versions/<version>`, with `/versions/` as a path segment) — do not use the `/versions/` form here, it will not resolve. The bootstrap commands below produce values in the correct colon form.

## One-time bootstrap procedure

Run once, by someone with GCP project-owner/editor permissions and `gh` authenticated against this repo. This is safe to re-run (most `gcloud`/`gh` commands here are idempotent or will just report "already exists").

```bash
# --- variables: edit these first ---
PROJECT_ID="your-gcp-project-id"
REGION="us-central1"
GH_REPO="mirkru37/rize-backend"
AR_REPO="rize-backend"
SA_NAME="rize-backend-deployer"
SA_EMAIL="${SA_NAME}@${PROJECT_ID}.iam.gserviceaccount.com"
WIF_POOL="github-pool"
WIF_PROVIDER="github-provider"
PROJECT_NUMBER="$(gcloud projects describe "${PROJECT_ID}" --format='value(projectNumber)')"

# 1. Enable required APIs
gcloud services enable \
  run.googleapis.com \
  artifactregistry.googleapis.com \
  iamcredentials.googleapis.com \
  secretmanager.googleapis.com \
  --project="${PROJECT_ID}"

# 2. Create the Artifact Registry Docker repo (holds both the api and migrate images)
gcloud artifacts repositories create "${AR_REPO}" \
  --repository-format=docker \
  --location="${REGION}" \
  --project="${PROJECT_ID}" \
  --description="rize-backend deploy images (api, migrate)"

# 3. Create the deployer service account
gcloud iam service-accounts create "${SA_NAME}" \
  --project="${PROJECT_ID}" \
  --display-name="rize-backend GitHub Actions deployer"

# 4. Grant the deployer SA the IAM roles it needs
for ROLE in roles/run.admin roles/artifactregistry.writer roles/iam.serviceAccountUser roles/secretmanager.secretAccessor; do
  gcloud projects add-iam-policy-binding "${PROJECT_ID}" \
    --member="serviceAccount:${SA_EMAIL}" \
    --role="${ROLE}"
done

# 5. Create the Workload Identity Pool
gcloud iam workload-identity-pools create "${WIF_POOL}" \
  --project="${PROJECT_ID}" \
  --location="global" \
  --display-name="GitHub Actions pool"

# 6. Create the GitHub OIDC provider, restricted to this exact repo
gcloud iam workload-identity-pools providers create-oidc "${WIF_PROVIDER}" \
  --project="${PROJECT_ID}" \
  --location="global" \
  --workload-identity-pool="${WIF_POOL}" \
  --display-name="GitHub OIDC provider" \
  --attribute-mapping="google.subject=assertion.sub,attribute.repository=assertion.repository" \
  --attribute-condition="assertion.repository=='${GH_REPO}'" \
  --issuer-uri="https://token.actions.githubusercontent.com"

# 7. Allow GitHub Actions workflows in this repo to impersonate the deployer SA (no JSON keys)
gcloud iam service-accounts add-iam-policy-binding "${SA_EMAIL}" \
  --project="${PROJECT_ID}" \
  --role="roles/iam.workloadIdentityUser" \
  --member="principalSet://iam.googleapis.com/projects/${PROJECT_NUMBER}/locations/global/workloadIdentityPools/${WIF_POOL}/attribute.repository/${GH_REPO}"

# 8. Create both Cloud Run services with an initial placeholder image
#    (assumption: using Google's public "hello" sample so the services exist
#    before the first real deploy; the first real deploy-integration /
#    deploy-production run replaces this with the actual application image)
gcloud run deploy rize-backend-integration \
  --image=us-docker.pkg.dev/cloudrun/container/hello \
  --region="${REGION}" --project="${PROJECT_ID}" --quiet

gcloud run deploy rize-backend-production \
  --image=us-docker.pkg.dev/cloudrun/container/hello \
  --region="${REGION}" --project="${PROJECT_ID}" --quiet

# 9. Create the Secret Manager secrets (empty containers; add real values next)
gcloud secrets create integration-database-url --project="${PROJECT_ID}" --replication-policy=automatic
gcloud secrets create production-database-url --project="${PROJECT_ID}" --replication-policy=automatic
gcloud secrets create jwt-signing-key --project="${PROJECT_ID}" --replication-policy=automatic

# 9a. Populate the secrets with real values (replace the placeholders below)
printf '%s' "postgres://user:pass@host:5432/rize_integration?sslmode=require" | \
  gcloud secrets versions add integration-database-url --project="${PROJECT_ID}" --data-file=-
printf '%s' "postgres://user:pass@host:5432/rize_production?sslmode=require" | \
  gcloud secrets versions add production-database-url --project="${PROJECT_ID}" --data-file=-
gcloud secrets versions add jwt-signing-key --project="${PROJECT_ID}" --data-file=/path/to/jwt-signing-key.pem

# 10. Set the GitHub Actions repo secrets
gh secret set GCP_PROJECT_ID --repo "${GH_REPO}" --body "${PROJECT_ID}"
gh secret set GCP_WIF_PROVIDER --repo "${GH_REPO}" \
  --body "projects/${PROJECT_NUMBER}/locations/global/workloadIdentityPools/${WIF_POOL}/providers/${WIF_PROVIDER}"
gh secret set GCP_SERVICE_ACCOUNT --repo "${GH_REPO}" --body "${SA_EMAIL}"
gh secret set GCP_REGION --repo "${GH_REPO}" --body "${REGION}"
gh secret set INTEGRATION_DATABASE_URL_SECRET --repo "${GH_REPO}" \
  --body "projects/${PROJECT_ID}/secrets/integration-database-url:latest"
gh secret set PRODUCTION_DATABASE_URL_SECRET --repo "${GH_REPO}" \
  --body "projects/${PROJECT_ID}/secrets/production-database-url:latest"
gh secret set JWT_SIGNING_KEY_SECRET --repo "${GH_REPO}" \
  --body "projects/${PROJECT_ID}/secrets/jwt-signing-key:latest"

# 11. Set the CORS origin repo variables
gh variable set INTEGRATION_CORS_ALLOWED_ORIGINS --repo "${GH_REPO}" --body "https://integration.example.com"
gh variable set PRODUCTION_CORS_ALLOWED_ORIGINS --repo "${GH_REPO}" --body "https://app.example.com"
```

After this, run `make deploy-bootstrap-check` to confirm all required secrets are set, then push to `main` to trigger the first real integration deploy.

## How migrations run as part of a deploy

Both workflows build (integration only) and reuse a second image, `Dockerfile.migrate`, alongside the main application image (built from `Dockerfile`). This mirrors `docker-compose.yml`'s `migrate` service — a one-shot `golang-migrate` run against `internal/store/migrations` — but as a Cloud Run **Job** instead of a Compose service, since Cloud Run services don't support one-shot run-to-completion containers.

`Dockerfile.migrate` is a separate image from the application image because the application image (`Dockerfile`) only ships the compiled API binary in its runtime stage — it does not embed `golang-migrate` or the migrations directory. `Dockerfile.migrate` repackages the official `migrate/migrate` binary onto a minimal Alpine base (the official image is built `FROM scratch`, which has no shell and so can't expand a `DATABASE_URL` environment variable at runtime) with the migrations directory baked in, and a shell entrypoint that reads `DATABASE_URL` from the environment. It is built and pushed with the same git-SHA tag as the application image so the two always travel together.

Ordering per deploy:

1. `gcloud run jobs deploy rize-backend-migrate-<env>` (create-or-update the Job definition, pointing at the migrate image tag for this deploy, with `DATABASE_URL` injected via `--set-secrets`).
2. `gcloud run jobs execute rize-backend-migrate-<env> --wait` — blocks until the migration run finishes; a non-zero exit fails the workflow step and the pipeline stops **before** touching the Cloud Run service.
3. Only after that succeeds: `gcloud run deploy rize-backend-<env>` with the application image tag for this deploy.

This guarantees the schema is migrated before any new application revision can receive traffic, for both integration and production.

## Free-tier notes

- **Cloud Run** has a genuine always-free monthly quota (as of writing: roughly 2 million requests, 360,000 GB-seconds of memory, 180,000 vCPU-seconds, and 1 GiB of egress from North America, per month, per billing account) — two small, low-traffic services (integration + production) comfortably fit within this for a project at this stage.
- **Cloud SQL has no free tier.** Every Cloud SQL instance, even the smallest (`db-f1-micro`), is billed continuously. Two options, pick one when you bootstrap:
  - **Recommended for this project's budget: a free-tier managed Postgres provider** — [Neon](https://neon.tech) or [Supabase](https://supabase.com) both offer a genuinely free Postgres tier suitable for integration/production at this scale. This keeps the whole stack on Google's free tier plus a free external Postgres, at $0/month.
  - **Alternative: Cloud SQL, smallest tier** (`db-f1-micro`, shared-core, ~10 GB storage) — rough cost is on the order of $10–15/month per instance as of writing (two instances, integration + production, roughly doubles that), plus storage/egress. Choose this if you want everything inside GCP IAM/networking and are fine with the monthly cost.
- **Known consideration, not addressed by this PR: TimescaleDB continuous aggregates.** This project's migrations (see `internal/store/migrations`, and `documentation/architecture-backend.md` §Aggregation Strategy) create TimescaleDB continuous aggregates (`daily_app_totals`, `daily_category_totals`, `hourly_category_totals`), which require the TimescaleDB extension to be installed and enabled on the target Postgres instance. Neon, Supabase, and plain Cloud SQL Postgres (i.e. Cloud SQL *without* explicitly provisioning it as Timescale-capable) do **not** support the TimescaleDB extension out of the box, so the cagg-creation migration steps will fail against any of those targets as-is. This PR does **not** change any migrations to work around this — it is a known, documented consideration for whoever picks the concrete DB target during bootstrap. Practical paths forward at that point: use a Timescale-capable managed offering (e.g. Timescale Cloud), or make the cagg-related migration steps conditional/optional for non-Timescale targets. That decision and any migration change it requires is out of scope for RIZ-36.
