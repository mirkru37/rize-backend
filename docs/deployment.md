# Deployment (RIZ-75)

`rize-backend` deploys the **full stack** (api + TimescaleDB + one-shot migrate, via `docker compose`) to a single **GCP Compute Engine VM per environment**: every merge to `main` deploys to an **integration** VM, and publishing a GitHub Release deploys to a **production** VM. This document describes the environments, the promotion flow, the one-time VM bootstrap procedure, every GitHub Environment var/secret the workflows consume, how migrations run as part of a deploy, and why the stack runs on a VM rather than Cloud Run.

CI (`.github/workflows/ci.yml`) is unaffected by any of this — it still runs lint/test/vuln/docker-build on every PR and push to `main`, unrelated to deployment.

> **History**: this replaces the Cloud Run-based deployment described by RIZ-36 (see git history for the prior version of this doc). The change is driven by a hosting gap RIZ-36 left open and explicitly flagged (see its "Free-tier notes" in the old version): TimescaleDB's continuous-aggregate migrations (`internal/store/migrations`) require the TimescaleDB *extension*, which none of Cloud SQL, Neon, or Supabase support out of the box. Running TimescaleDB **in** the deploy compose stack (the same `timescale/timescaledb:latest-pg16` image `docker-compose.yml` already uses for local dev) resolves that gap directly — the extension is always present, no managed-Postgres compromise needed.

## Environments

| | Integration | Production |
|---|---|---|
| Trigger | Push to `main` (`.github/workflows/deploy-integration.yml`) | GitHub Release published (`.github/workflows/deploy-production.yml`) |
| GitHub Environment | `integration` | `production` |
| GCE VM | `vars.GCE_INSTANCE` in the `integration` GitHub Environment (e.g. `rize-backend-integration`) | `vars.GCE_INSTANCE` in the `production` GitHub Environment (e.g. `rize-backend-production`) |
| `ENVIRONMENT` value | `staging` (workflow-set, not configurable) | `production` (workflow-set, not configurable) |
| Image source | Built fresh from the pushed commit | The *same* image already built and pushed by `deploy-integration.yml` for the release's target commit — never rebuilt, deployed by immutable digest |

> **Assumption on `ENVIRONMENT`** (carried over from RIZ-36, unchanged): `internal/config/config.go`'s `validEnvironments` set is exactly `{development, staging, production}` — there is no literal `"integration"` value accepted by `config.Load()`. The integration deploy therefore runs the api container with `ENVIRONMENT=staging`, the closest semantic fit (a non-development, non-production deployed environment), which also satisfies the same-file rule that any non-`development` environment must not resolve to a wildcard CORS origin.

## Architecture: one VM, one compose stack, per environment

Each environment is a single `e2-small` (or larger) Compute Engine VM running Docker + the Compose plugin. A deploy copies two files to the VM's home directory (`~/rize-backend/`):

- `docker-compose.yml` — this repo's [`deploy/docker-compose.deploy.yml`](../deploy/docker-compose.deploy.yml), copied as-is. Three services: `timescaledb` (TimescaleDB, data on the named volume `timescaledb-data` so it survives redeploys), `migrate` (one-shot `docker compose run --rm`, never part of `up`), and `api` (the application, `restart: unless-stopped`, with a `/healthz` healthcheck).
- `.env` — rendered fresh on every deploy from that environment's GitHub Environment vars/secrets (see table below); never committed, never logged.

A deploy then runs, over `gcloud compute ssh --tunnel-through-iap`:

```bash
docker compose --env-file .env pull                 # pull api + migrate + timescaledb images
docker compose --env-file .env run --rm migrate      # one-shot migration, blocks until done, exits
docker compose --env-file .env up -d api             # (re)start the api container only
```

`timescaledb` itself is started implicitly as a dependency of both `migrate` and `api` (`depends_on: condition: service_healthy`); it is not restarted on every deploy if it's already running and healthy — `docker compose up` only (re)creates a service whose config/image actually changed.

This mirrors local development's `docker-compose.yml` (same TimescaleDB image, same migration-then-api ordering) but with pre-built images from Artifact Registry instead of a local build, and real per-environment secrets instead of hardcoded local credentials.

## Promotion flow

```
PR merged to main
      │
      ▼
deploy-integration.yml
  1. guard: GCP_PROJECT_ID set?  no -> skip cleanly, workflow succeeds
  2. build-and-push: build api + migrate images, tag with <git-sha> and "integration", push to Artifact Registry
  3. deploy (environment: integration):
       - verify the integration VM exists (fails with an actionable error
         pointing at deploy/bootstrap-gce.sh if not)
       - render .env from the integration GitHub Environment's vars/secrets
       - scp deploy/docker-compose.deploy.yml + .env to the VM
       - ssh: docker login to Artifact Registry, then
         `docker compose pull && docker compose run --rm migrate && docker compose up -d api`
       - post-deploy health check: curl /healthz on the VM, fail the
         workflow on non-200
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
     (both api and migrate images), resolving each image's immutable
     content digest — fails loudly if either is missing
  3. deploy (environment: production): same VM-deploy steps as
     integration, but the rendered .env pins API_IMAGE/MIGRATE_IMAGE by
     the resolved immutable digest (@sha256:...), never by mutable tag,
     and never rebuilds
```

Production never builds its own image. If you publish a release for a commit that never landed on `main` (or landed but `deploy-integration.yml` hasn't finished/succeeded for it yet), `deploy-production.yml`'s `resolve-image` job fails with an explicit error telling you to land the commit on `main`, wait for the integration deploy to succeed, and then re-publish the release.

In both workflows, `docker compose run --rm migrate` completes (and the workflow step succeeds) **before** `docker compose up -d api` runs, so a failed migration blocks the traffic-affecting deploy step. The post-deploy health check runs last and fails the workflow if the api container doesn't answer `/healthz` with 200 after the deploy.

## GitHub Environments: vars and secrets

Deployment configuration comes from two places:

1. **Repo-level secrets** (`gh secret set ... --repo <repo>`, no `--env`) — the GCP bootstrap identity, shared across both environments, set once by whoever ran the one-time GCP project bootstrap (see RIZ-36's original bootstrap procedure; unchanged by this ticket):

   | Name | Kind | Purpose |
   |---|---|---|
   | `GCP_PROJECT_ID` | secret | The GCP project ID. Also acts as the deploy on/off switch: both workflows' `guard` job checks this is set. |
   | `GCP_REGION` | secret | The Artifact Registry / Compute Engine region, e.g. `us-central1`. |
   | `GCP_WIF_PROVIDER` | secret | Workload Identity Federation provider resource name, used by `google-github-actions/auth@v2`. |
   | `GCP_SERVICE_ACCOUNT` | secret | Email of the deployer service account GitHub Actions impersonates via WIF (no JSON keys). |

2. **Per-environment vars/secrets**, via [GitHub Environments](https://docs.github.com/en/actions/deployment/targeting-different-environments/using-environments-for-deployment) named `integration` and `production` — every runtime **application** setting comes from here (no GCP Secret Manager involved for app config; the whole point of this GCE model is that the VM is the only place secrets need to live besides GitHub itself):

   | Name | Kind | Purpose |
   |---|---|---|
   | `GCE_INSTANCE` | var | Name of this environment's VM (e.g. `rize-backend-integration`), created by `deploy/bootstrap-gce.sh`. |
   | `GCE_ZONE` | var | Zone the VM lives in (e.g. `us-central1-a`). |
   | `CORS_ALLOWED_ORIGINS` | var | Comma-separated list of allowed CORS origins for this environment. Per `documentation/security.md` §API hardening, this must never be `*` outside `development`. |
   | `POSTGRES_PASSWORD` | secret | Password for the in-stack TimescaleDB container's `rize` user. Used directly by the `timescaledb` service and to build `DATABASE_URL` for `api`/`migrate` if `DATABASE_URL` (below) isn't set. **Must not contain `:`, `@`, `/`, `?`, `#`, or whitespace** — the deploy workflow interpolates it directly into the `postgres://rize:$POSTGRES_PASSWORD@timescaledb:5432/rize?sslmode=disable` connection string below with no percent-encoding, so any of those characters would corrupt the URL. Stick to alphanumerics plus a small set of unreserved punctuation (e.g. `-_.`) when generating this value; there's no need for the full password character space here. |
   | `DATABASE_URL` | secret | **Optional** full Postgres connection string, e.g. to point at a database other than the in-stack `timescaledb` container. If set, this is used verbatim and `POSTGRES_PASSWORD` is only used to provision the (otherwise still-running, but then unused) in-stack TimescaleDB container. If unset, the rendered `.env`'s `DATABASE_URL` is built as `postgres://rize:$POSTGRES_PASSWORD@timescaledb:5432/rize?sslmode=disable` — see the `POSTGRES_PASSWORD` row above for the resulting character-set constraint on that secret. |
   | `JWT_SIGNING_KEY` | secret | PEM-encoded RSA signing key (see `internal/config`'s doc comment; RS256, 2048-bit minimum) used by this environment's api container only. |

   Set with `gh variable set NAME --env integration --body ...` / `gh secret set NAME --env integration --body ...` (swap `--env production` for the production environment). **Use two different `JWT_SIGNING_KEY` values for integration and production** — never reuse the same key material across environments; a shared signing key would let anyone who obtains an integration-issued token (a lower-trust environment) authenticate against production.

   The `deploy` job in each workflow declares `environment: integration` / `environment: production` so these are the only secrets/vars visible to it (the `build-and-push` and `resolve-image` jobs use only the repo-level secrets above).

## Why GCE instead of Cloud Run (superseding RIZ-36)

The previous (RIZ-36) design deployed the api binary alone to Cloud Run, with the database as an external dependency (Cloud SQL, or a free-tier managed Postgres like Neon/Supabase) and migrations as a Cloud Run Job. That left TimescaleDB-extension support as an explicitly unresolved gap: none of the free-tier-friendly managed Postgres options support the extension this project's continuous-aggregate migrations require. Running the **entire stack** — including TimescaleDB itself — as a `docker compose` unit on one small VM per environment sidesteps that gap entirely: the same `timescale/timescaledb:latest-pg16` image used locally is what runs in integration/production, so the extension is always available by construction. The tradeoff is that this project now owns VM patching/uptime/backups for the database instead of a managed provider — for a project at this stage and this rate of change, that tradeoff was made deliberately in favor of dropping the extension-support gap rather than working around it.

## One-time VM bootstrap procedure

Run **once per environment** (`ENVIRONMENT=integration` and again with `ENVIRONMENT=production`), by someone with GCP project-owner/editor permissions and `gcloud` authenticated against the right project. [`deploy/bootstrap-gce.sh`](../deploy/bootstrap-gce.sh) is safe to re-run — every step checks for existing state before creating anything.

```bash
PROJECT_ID="your-gcp-project-id" \
DEPLOYER_SA_EMAIL="rize-backend-deployer@your-gcp-project-id.iam.gserviceaccount.com" \
ZONE="us-central1-a" \
ENVIRONMENT=integration \
  ./deploy/bootstrap-gce.sh

PROJECT_ID="your-gcp-project-id" \
DEPLOYER_SA_EMAIL="rize-backend-deployer@your-gcp-project-id.iam.gserviceaccount.com" \
ZONE="us-central1-a" \
ENVIRONMENT=production \
  ./deploy/bootstrap-gce.sh
```

What it does, per environment:

1. Enables the Compute Engine, Artifact Registry, IAM Credentials, and IAP APIs.
2. Creates a dedicated runtime service account for the VM (`rize-backend-vm-<env>`), distinct from the GitHub Actions deployer SA, with only `roles/artifactregistry.reader` (least privilege — the VM only ever needs to pull images; the deploy workflow itself authenticates docker with its own short-lived access token, so this is a defense-in-depth grant, not the only path).
3. Creates two firewall rules, both scoped to a `rize-backend-<env>` network tag: one opening the API port (default `8080`) to `0.0.0.0/0` (the API is public by design — request auth is the app's own JWT layer, not network-level, matching the RIZ-36 `--allow-unauthenticated` posture), and one opening SSH (port 22) **only** to Google's Identity-Aware Proxy range (`35.235.240.0/20`) — the deploy workflow reaches the VM exclusively via `gcloud compute ssh/scp --tunnel-through-iap`, never a direct public SSH port, so GitHub Actions' ephemeral runner IPs never need to be allowlisted.
4. Grants the GitHub Actions deployer service account `roles/iap.tunnelResourceAccessor` (to open the IAP tunnel), `roles/compute.instanceAdmin.v1` + `roles/iam.serviceAccountUser` (to describe the VM) and `roles/compute.osAdminLogin` (to actually SSH in as an admin over OS Login).
5. Creates the Artifact Registry Docker repo (`rize-backend`) if it doesn't already exist — shared across environments, so this is a no-op on the second run.
6. Creates the VM itself: Debian 12, `e2-small` by default, OS Login enabled, tagged for the firewall rules above, running as the dedicated runtime service account, with a startup script that installs Docker + the Compose plugin on first boot (idempotent — it exits immediately if Docker is already present, so re-running the bootstrap script or rebooting the VM doesn't reinstall).

After it finishes, it prints the exact `gh variable set` / `gh secret set` commands for that environment's `GCE_INSTANCE`, `GCE_ZONE`, `CORS_ALLOWED_ORIGINS`, `POSTGRES_PASSWORD`, and `JWT_SIGNING_KEY` — fill those in with real values, then push to `main` (integration) or publish a release (production) to trigger the first real deploy.

## How migrations run as part of a deploy

`Dockerfile.migrate` (unchanged by this ticket) builds a separate image from the application image (`Dockerfile`) because the application image only ships the compiled API binary in its runtime stage — it does not embed `golang-migrate` or the migrations directory. It is built and pushed with the same git-SHA tag as the application image so the two always travel together, and referenced by the `migrate` service in `deploy/docker-compose.deploy.yml`.

Ordering per deploy, all within a single `gcloud compute ssh` command:

1. `docker compose --env-file .env pull` — pulls the `timescaledb`, `migrate`, and `api` images for this deploy.
2. `docker compose --env-file .env run --rm migrate` — starts `timescaledb` (if not already healthy), runs the one-shot migration to completion, removes the migration container. A non-zero exit fails this step (and the workflow) **before** touching the running `api` container.
3. Only after that succeeds: `docker compose --env-file .env up -d api` — (re)creates the `api` container with the new image if it changed.

This guarantees the schema is migrated before any new application code can receive traffic, for both integration and production.

### Migration/deploy ordering invariant

Because the migration step commits its changes and completes **before** `api` is (re)started (step 3 above), there is always a window — however brief — during which the **already-migrated** schema is being served by the **still-running previous** `api` container (it isn't stopped until `docker compose up -d api` replaces it). This matters most for production.

**Invariant: every migration must be backward-compatible with the previous application revision.** Concretely, this means migrations must follow an expand/contract pattern:

- **Expand**: additive, backward-compatible changes only — new tables, new nullable columns, new indexes, new columns with a default. The previous revision must continue to run unmodified against the post-migration schema.
- **Contract**: destructive changes (dropping a column, dropping a table, renaming a column, tightening a constraint that old code relied on being loose) must be a *separate, later* migration, shipped only after a revision that no longer reads/writes the old shape has already been deployed and is stable.

A migration that the still-running previous `api` container cannot tolerate (e.g. dropping a column it still selects, or a `NOT NULL` addition it doesn't populate) will break that container for the duration of the deploy window, independent of whether the migration step itself succeeds.

## Production image resolution: digest pinning

`deploy-production.yml`'s `resolve-image` job does not deploy by the git-SHA *tag* it resolves. Tags in Artifact Registry are mutable — a tag can be repointed at a different image after the fact (accidentally or via a compromised credential), which would let a later, uninspected image slip into a "promote this exact, already-validated build" deploy without it being obvious in the workflow run. To close that gap, `resolve-image` additionally resolves each image's immutable content digest via `gcloud artifacts docker images describe ... --format='value(image_summary.digest)'` for both the api and migrate images, fails the job with a clear error if either digest can't be resolved, and the production `deploy` job's rendered `.env` pins both `API_IMAGE` and `MIGRATE_IMAGE` by `<image>@sha256:<digest>` rather than by `<image>:<sha-tag>`. Integration continues to deploy by tag (`:${{ github.sha }}`), since it's building and pushing the image itself in the same run.

## TimescaleDB extension: resolved

**This is the consideration RIZ-36 flagged as unresolved, now resolved by this ticket.** RIZ-36's version of this document noted that this project's migrations (see `internal/store/migrations`) create TimescaleDB continuous aggregates (`daily_app_totals`, `daily_category_totals`, `hourly_category_totals`), which require the TimescaleDB extension — and that Neon, Supabase, and plain Cloud SQL Postgres don't support it out of the box. Running TimescaleDB itself inside the deploy compose stack (the `timescaledb` service in `deploy/docker-compose.deploy.yml`, same image as local dev) means the extension is always present by construction; there is no external managed-Postgres compromise to make. No migration changes were needed to resolve this.

## Free-tier / cost notes

- Two `e2-small` VMs (integration + production) are the primary ongoing cost — Compute Engine has no perpetual free tier equivalent to Cloud Run's; budget for roughly one to two small always-on instances' worth of compute per month. `e2-micro` is available in some regions under the "always free" tier terms; swap `MACHINE_TYPE=e2-micro` into the bootstrap script if that fits the target region's always-free eligibility and the workload fits within it (TimescaleDB alongside the api process is unlikely to be comfortable on `e2-micro` for anything beyond light integration testing).
- Both firewall rules, the Artifact Registry repo, and IAM bindings created by `deploy/bootstrap-gce.sh` have no meaningful cost themselves.
- TimescaleDB's data volume (`timescaledb-data`) lives on the VM's persistent disk. Back it up out-of-band (e.g. periodic `pg_dump` to a bucket) if data durability beyond "the VM disk stays intact" matters for the target environment — this ticket does not add automated backups.
