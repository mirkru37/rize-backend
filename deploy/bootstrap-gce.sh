#!/usr/bin/env bash
#
# One-time (per environment) GCE VM bootstrap for rize-backend (RIZ-75).
#
# Creates the Compute Engine VM that a deploy-integration.yml /
# deploy-production.yml run will later ssh/scp into to run the full stack
# (api + TimescaleDB + one-shot migrate) via docker compose — see
# docs/deployment.md for the full architecture and
# deploy/docker-compose.deploy.yml for the compose file that ends up on the
# VM.
#
# This script is idempotent-ish: every gcloud call is preceded by an
# existence check, so re-running it after a partial failure (or to pick up
# a config tweak) does not error out on "already exists".
#
# Usage:
#   ENVIRONMENT=integration ./deploy/bootstrap-gce.sh
#   ENVIRONMENT=production  ./deploy/bootstrap-gce.sh
#
# Run once per environment, by someone with GCP project-owner/editor
# permissions and `gcloud` authenticated against the right project. This
# script only provisions the VM and its supporting IAM/firewall/service
# account — it does NOT set any GitHub secrets/vars; that's a separate,
# manual step printed at the end (see docs/deployment.md §Secrets and
# variables for the full per-environment list).
set -euo pipefail

# --- variables: edit these first (or export before invoking) ---
ENVIRONMENT="${ENVIRONMENT:?set ENVIRONMENT to integration or production}"
case "${ENVIRONMENT}" in
  integration|production) ;;
  *) echo "ENVIRONMENT must be 'integration' or 'production', got '${ENVIRONMENT}'" >&2; exit 1 ;;
esac

PROJECT_ID="${PROJECT_ID:?set PROJECT_ID to your GCP project id}"
ZONE="${ZONE:-us-central1-a}"
REGION="${REGION:-${ZONE%-*}}"
INSTANCE_NAME="${INSTANCE_NAME:-rize-backend-${ENVIRONMENT}}"
MACHINE_TYPE="${MACHINE_TYPE:-e2-small}"
API_PORT="${API_PORT:-8080}"
DEPLOYER_SA_EMAIL="${DEPLOYER_SA_EMAIL:?set DEPLOYER_SA_EMAIL to the GitHub Actions deployer service account (see docs/deployment.md bootstrap step for GCP_SERVICE_ACCOUNT)}"
RUNTIME_SA_NAME="${RUNTIME_SA_NAME:-rize-backend-vm-${ENVIRONMENT}}"
RUNTIME_SA_EMAIL="${RUNTIME_SA_NAME}@${PROJECT_ID}.iam.gserviceaccount.com"
AR_REPO="${AR_REPO:-rize-backend}"

echo "=== rize-backend GCE bootstrap: environment=${ENVIRONMENT} project=${PROJECT_ID} zone=${ZONE} instance=${INSTANCE_NAME} ==="

# 1. Enable required APIs (safe to re-run).
gcloud services enable \
  compute.googleapis.com \
  artifactregistry.googleapis.com \
  iamcredentials.googleapis.com \
  iap.googleapis.com \
  --project="${PROJECT_ID}"

# 2. Create a dedicated runtime service account for this VM, distinct from
#    the GitHub Actions deployer SA — this is the identity the VM itself
#    runs as, and it only needs pull access to Artifact Registry (least
#    privilege: it never needs to deploy anything, just read images).
if gcloud iam service-accounts describe "${RUNTIME_SA_EMAIL}" --project="${PROJECT_ID}" &>/dev/null; then
  echo "[skip] runtime service account ${RUNTIME_SA_EMAIL} already exists"
else
  gcloud iam service-accounts create "${RUNTIME_SA_NAME}" \
    --project="${PROJECT_ID}" \
    --display-name="rize-backend ${ENVIRONMENT} VM runtime identity"
fi

if gcloud projects get-iam-policy "${PROJECT_ID}" \
    --flatten="bindings[].members" \
    --filter="bindings.role:roles/artifactregistry.reader AND bindings.members:serviceAccount:${RUNTIME_SA_EMAIL}" \
    --format="value(bindings.role)" | grep -q artifactregistry.reader; then
  echo "[skip] ${RUNTIME_SA_EMAIL} already has roles/artifactregistry.reader"
else
  gcloud projects add-iam-policy-binding "${PROJECT_ID}" \
    --member="serviceAccount:${RUNTIME_SA_EMAIL}" \
    --role="roles/artifactregistry.reader" \
    --condition=None
fi

# 3. Firewall: allow the API port from anywhere (this is a public API by
#    design — see docs/deployment.md; request auth is the app's own JWT
#    layer, not network-level).
FW_API_RULE="allow-rize-backend-api-${ENVIRONMENT}"
if gcloud compute firewall-rules describe "${FW_API_RULE}" --project="${PROJECT_ID}" &>/dev/null; then
  echo "[skip] firewall rule ${FW_API_RULE} already exists"
else
  gcloud compute firewall-rules create "${FW_API_RULE}" \
    --project="${PROJECT_ID}" \
    --network=default \
    --direction=INGRESS \
    --action=ALLOW \
    --rules="tcp:${API_PORT}" \
    --source-ranges="0.0.0.0/0" \
    --target-tags="rize-backend-${ENVIRONMENT}" \
    --description="rize-backend ${ENVIRONMENT}: public access to the API port"
fi

# 4. Firewall: allow SSH only from Google's Identity-Aware Proxy range, not
#    the whole internet. The deploy workflow ssh/scp's into the VM via
#    `gcloud compute ssh/scp --tunnel-through-iap`, which relays through
#    this range rather than needing a stable GitHub Actions runner IP
#    allowlist (runner IPs are ephemeral and unpredictable).
FW_IAP_RULE="allow-iap-ssh-${ENVIRONMENT}"
if gcloud compute firewall-rules describe "${FW_IAP_RULE}" --project="${PROJECT_ID}" &>/dev/null; then
  echo "[skip] firewall rule ${FW_IAP_RULE} already exists"
else
  gcloud compute firewall-rules create "${FW_IAP_RULE}" \
    --project="${PROJECT_ID}" \
    --network=default \
    --direction=INGRESS \
    --action=ALLOW \
    --rules="tcp:22" \
    --source-ranges="35.235.240.0/20" \
    --target-tags="rize-backend-${ENVIRONMENT}" \
    --description="rize-backend ${ENVIRONMENT}: SSH only via IAP tunnel"
fi

# 5. Let the GitHub Actions deployer service account open IAP tunnels to
#    this project (needed for --tunnel-through-iap ssh/scp in the deploy
#    workflows).
if gcloud projects get-iam-policy "${PROJECT_ID}" \
    --flatten="bindings[].members" \
    --filter="bindings.role:roles/iap.tunnelResourceAccessor AND bindings.members:serviceAccount:${DEPLOYER_SA_EMAIL}" \
    --format="value(bindings.role)" | grep -q iap.tunnelResourceAccessor; then
  echo "[skip] ${DEPLOYER_SA_EMAIL} already has roles/iap.tunnelResourceAccessor"
else
  gcloud projects add-iam-policy-binding "${PROJECT_ID}" \
    --member="serviceAccount:${DEPLOYER_SA_EMAIL}" \
    --role="roles/iap.tunnelResourceAccessor" \
    --condition=None
fi

# 6. Let the GitHub Actions deployer service account log in over OS Login
#    as an ssh-capable admin (needed to run `docker compose` commands on
#    the VM). instanceAdmin.v1 covers describe/start/stop, osLogin covers
#    the actual ssh access grant.
for ROLE in roles/compute.instanceAdmin.v1 roles/iam.serviceAccountUser roles/compute.osAdminLogin; do
  if gcloud projects get-iam-policy "${PROJECT_ID}" \
      --flatten="bindings[].members" \
      --filter="bindings.role:${ROLE} AND bindings.members:serviceAccount:${DEPLOYER_SA_EMAIL}" \
      --format="value(bindings.role)" | grep -q "${ROLE}"; then
    echo "[skip] ${DEPLOYER_SA_EMAIL} already has ${ROLE}"
  else
    gcloud projects add-iam-policy-binding "${PROJECT_ID}" \
      --member="serviceAccount:${DEPLOYER_SA_EMAIL}" \
      --role="${ROLE}" \
      --condition=None
  fi
done

# 7. Create the Artifact Registry Docker repo if it doesn't already exist
#    (deploy-integration.yml / deploy-production.yml push api+migrate
#    images here; this is shared across environments, so this step is a
#    no-op if RIZ-36's Cloud Run bootstrap already created it).
if gcloud artifacts repositories describe "${AR_REPO}" --location="${REGION}" --project="${PROJECT_ID}" &>/dev/null; then
  echo "[skip] Artifact Registry repo ${AR_REPO} already exists in ${REGION}"
else
  gcloud artifacts repositories create "${AR_REPO}" \
    --repository-format=docker \
    --location="${REGION}" \
    --project="${PROJECT_ID}" \
    --description="rize-backend deploy images (api, migrate)"
fi

# 8. Create the VM itself: Debian 12 with Docker + the Compose plugin
#    installed via a startup script, tagged so the firewall rules above
#    apply, running as the dedicated runtime service account (scoped to
#    cloud-platform so it can pull from Artifact Registry via its own
#    identity as a fallback/inspection path — the deploy workflow itself
#    authenticates docker with a short-lived access token it generates,
#    see deploy-integration.yml / deploy-production.yml).
if gcloud compute instances describe "${INSTANCE_NAME}" --zone="${ZONE}" --project="${PROJECT_ID}" &>/dev/null; then
  echo "[skip] VM instance ${INSTANCE_NAME} already exists in ${ZONE}"
else
  gcloud compute instances create "${INSTANCE_NAME}" \
    --project="${PROJECT_ID}" \
    --zone="${ZONE}" \
    --machine-type="${MACHINE_TYPE}" \
    --image-family=debian-12 \
    --image-project=debian-cloud \
    --boot-disk-size=20GB \
    --tags="rize-backend-${ENVIRONMENT}" \
    --service-account="${RUNTIME_SA_EMAIL}" \
    --scopes="cloud-platform" \
    --metadata=enable-oslogin=TRUE \
    --metadata-from-file=startup-script=<(cat <<'STARTUP'
#!/bin/bash
set -e
if command -v docker >/dev/null 2>&1; then
  exit 0
fi
apt-get update
apt-get install -y ca-certificates curl gnupg
install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/debian/gpg -o /etc/apt/keyrings/docker.asc
chmod a+r /etc/apt/keyrings/docker.asc
echo \
  "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian $(. /etc/os-release && echo "$VERSION_CODENAME") stable" \
  > /etc/apt/sources.list.d/docker.list
apt-get update
apt-get install -y docker-ce docker-ce-cli containerd.io docker-compose-plugin
mkdir -p /home/rize-backend
STARTUP
)
  echo "VM created. The startup script installs Docker + the Compose plugin on first boot — allow a minute or two before the first deploy."
fi

cat <<EOF

=== Bootstrap complete for ${ENVIRONMENT} ===

Follow-up (manual) steps:

1. Wait ~1-2 minutes for the startup script to finish installing Docker on
   first boot. You can check with:
     gcloud compute ssh ${INSTANCE_NAME} --zone=${ZONE} --project=${PROJECT_ID} --tunnel-through-iap --command="docker --version"

2. Set these GitHub Environment (${ENVIRONMENT}) vars, in addition to the
   repo-level bootstrap secrets from RIZ-36 (GCP_PROJECT_ID, GCP_REGION,
   GCP_WIF_PROVIDER, GCP_SERVICE_ACCOUNT) — see docs/deployment.md for the
   full table:
     gh variable set GCE_INSTANCE --env ${ENVIRONMENT} --body "${INSTANCE_NAME}"
     gh variable set GCE_ZONE --env ${ENVIRONMENT} --body "${ZONE}"
     gh variable set CORS_ALLOWED_ORIGINS --env ${ENVIRONMENT} --body "https://<your-${ENVIRONMENT}-frontend>"

3. Set these GitHub Environment (${ENVIRONMENT}) secrets:
     gh secret set POSTGRES_PASSWORD --env ${ENVIRONMENT} --body "<a-strong-random-password>"
     gh secret set JWT_SIGNING_KEY --env ${ENVIRONMENT} --body "$(openssl genrsa 2048)"
   (Or set DATABASE_URL instead of POSTGRES_PASSWORD if pointing at a
   connection string that isn't the in-stack TimescaleDB container.)

4. Push to main (integration) or publish a release (production) to trigger
   the first real deploy.

EOF
