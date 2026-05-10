#!/usr/bin/env bash
# Creates a preemptible GCE e2-micro VM that runs rclone to copy Dropbox → Google Drive,
# then self-destructs. rclone copy is idempotent: if the VM is preempted, just re-run.
#
# Usage:
#   GCP_PROJECT=my-project bash migrate/run.sh
#
# Optional overrides:
#   GCP_ZONE=us-central1-a        (default)
#   MACHINE_TYPE=e2-micro         (default; e2-small if you want more RAM)
#   DROPBOX_SRC=photos            (default: root — copies everything)
#   GDRIVE_DST=Dropbox-import     (default: root)

set -euo pipefail

: "${GCP_PROJECT:?Set GCP_PROJECT}"
GCP_ZONE="${GCP_ZONE:-us-central1-a}"
MACHINE_TYPE="${MACHINE_TYPE:-e2-micro}"
SECRET_NAME="dropbox-gdrive-rclone-conf"
SA_EMAIL="dropbox-gdrive-migrator@${GCP_PROJECT}.iam.gserviceaccount.com"
INSTANCE="dropbox-gdrive-$(date +%Y%m%d-%H%M%S)"
DROPBOX_SRC="${DROPBOX_SRC:-}"
GDRIVE_DST="${GDRIVE_DST:-}"

# ── Write startup script to a temp file (avoids quoting hell in --metadata) ──

TMPSCRIPT=$(mktemp)
trap 'rm -f "$TMPSCRIPT"' EXIT

cat > "$TMPSCRIPT" << 'STARTUP'
#!/bin/bash
set -euo pipefail
exec > >(logger -t rclone-migration -s) 2>&1

# Pull params from instance metadata
META="http://metadata.google.internal/computeMetadata/v1/instance"
H=(-H "Metadata-Flavor: Google")
PROJECT=$(curl -sf "$META/attributes/mig-project" "${H[@]}")
SECRET=$(curl  -sf "$META/attributes/mig-secret"  "${H[@]}")
SRC=$(curl     -sf "$META/attributes/mig-src"     "${H[@]}")
DST=$(curl     -sf "$META/attributes/mig-dst"     "${H[@]}")
ZONE=$(curl    -sf "$META/zone"                   "${H[@]}" | awk -F/ '{print $NF}')
NAME=$(curl    -sf "$META/name"                   "${H[@]}")

echo "[$(date -u +%FT%TZ)] Installing rclone..."
# Use official install script for latest version; fall back to apt
curl -fsSL https://rclone.org/install.sh | bash 2>/dev/null \
  || { apt-get update -qq && apt-get install -y -qq rclone; }

echo "[$(date -u +%FT%TZ)] Fetching rclone config from Secret Manager..."
mkdir -p /root/.config/rclone
gcloud secrets versions access latest \
  --secret="$SECRET" \
  --project="$PROJECT" \
  > /root/.config/rclone/rclone.conf

echo "[$(date -u +%FT%TZ)] Starting: $SRC → $DST"
rclone copy "$SRC" "$DST" \
  --transfers=4 \
  --checkers=8 \
  --drive-chunk-size=256M \
  --dropbox-chunk-size=128M \
  --log-level=INFO \
  --stats=60s \
  --stats-log-level=INFO

echo "[$(date -u +%FT%TZ)] Migration complete. Deleting VM..."
gcloud compute instances delete "$NAME" \
  --zone="$ZONE" \
  --project="$PROJECT" \
  --quiet
STARTUP

# ── Create the VM ─────────────────────────────────────────────────────────────

echo "Creating VM: $INSTANCE"
echo "  Zone:    $GCP_ZONE"
echo "  Type:    $MACHINE_TYPE (SPOT)"
echo "  Source:  dropbox:${DROPBOX_SRC:-<root>}"
echo "  Dest:    gdrive:${GDRIVE_DST:-<root>}"
echo ""

gcloud compute instances create "$INSTANCE" \
  --project="$GCP_PROJECT" \
  --zone="$GCP_ZONE" \
  --machine-type="$MACHINE_TYPE" \
  --provisioning-model=SPOT \
  --instance-termination-action=DELETE \
  --image-family=debian-12 \
  --image-project=debian-cloud \
  --boot-disk-size=10GB \
  --boot-disk-type=pd-standard \
  --service-account="$SA_EMAIL" \
  --scopes=cloud-platform \
  --metadata-from-file=startup-script="$TMPSCRIPT" \
  --metadata="mig-project=${GCP_PROJECT},mig-secret=${SECRET_NAME},mig-src=dropbox:${DROPBOX_SRC},mig-dst=gdrive:${GDRIVE_DST}"

# ── Tail instructions ─────────────────────────────────────────────────────────

echo "VM created. Watch progress (logs appear ~1 min after boot):"
echo ""
echo "  # Cloud Logging (structured)"
echo "  gcloud logging read \\"
echo "    'resource.type=gce_instance AND jsonPayload.MESSAGE=~\"rclone-migration\"' \\"
echo "    --project=$GCP_PROJECT --order=asc --freshness=2h \\"
echo "    --format='table(timestamp,jsonPayload.MESSAGE)'"
echo ""
echo "  # Serial port (raw, good for startup errors)"
echo "  gcloud compute instances get-serial-port-output $INSTANCE \\"
echo "    --zone=$GCP_ZONE --project=$GCP_PROJECT"
echo ""
echo "VM self-deletes when done. If preempted, just re-run run.sh — rclone copy is idempotent."
