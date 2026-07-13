#!/usr/bin/env bash
# Run once on your local machine.
# Walks through every auth step end-to-end:
#   1. Dropbox OAuth (browser)
#   2. Google Drive — creates your own OAuth client in GCP so Google stops
#      showing the "unverified app" warning, then does the Drive OAuth (browser)
#   3. Stores the resulting rclone config in Secret Manager
#   4. Creates the GCE service account the migration VM will run as
#
# Prerequisites:
#   brew install rclone google-cloud-sdk
#   gcloud auth login && gcloud auth application-default login
#
# Usage:
#   GCP_PROJECT=my-project bash migrate/setup.sh

set -euo pipefail

: "${GCP_PROJECT:?Set GCP_PROJECT}"
GCP_REGION="${GCP_REGION:-us-central1}"
SECRET_NAME="dropbox-gdrive-rclone-conf"
SA_NAME="dropbox-gdrive-migrator"
SA_EMAIL="${SA_NAME}@${GCP_PROJECT}.iam.gserviceaccount.com"

# ── 1. Prerequisites ──────────────────────────────────────────────────────────

for cmd in rclone gcloud python3; do
  command -v "$cmd" >/dev/null || { echo "Missing: $cmd"; exit 1; }
done

gcloud config set project "$GCP_PROJECT" --quiet

# ── 2. Enable GCP APIs ────────────────────────────────────────────────────────

echo "Enabling GCP APIs..."
gcloud services enable \
  secretmanager.googleapis.com \
  compute.googleapis.com \
  drive.googleapis.com \
  --project="$GCP_PROJECT" --quiet

# ── 3. Dropbox OAuth ──────────────────────────────────────────────────────────

echo ""
echo "=== [1/2] Dropbox ==="

if rclone listremotes | grep -q "^dropbox:"; then
  echo "  Already configured — skipping."
else
  echo "  Your browser will open for Dropbox sign-in and consent."
  echo "  rclone uses its own registered Dropbox app, so there is no"
  echo "  'unverified app' warning on this side."
  echo ""
  read -rp "  Press Enter to open the browser..."

  # rclone authorize outputs the token JSON to stdout after the user consents.
  # We capture it and feed it straight into config create.
  TMPDB=$(mktemp)
  trap 'rm -f "$TMPDB"' EXIT
  rclone authorize "dropbox" > "$TMPDB" 2>&1

  DROPBOX_TOKEN=$(python3 - "$TMPDB" << 'PY'
import sys, re, json
content = open(sys.argv[1]).read()
for m in re.finditer(r'\{[^{}]*"access_token"[^{}]*\}', content):
    try:
        json.loads(m.group())
        print(m.group())
        break
    except json.JSONDecodeError:
        pass
PY
)

  [[ -n "$DROPBOX_TOKEN" ]] || { echo "No token received from Dropbox. Re-run the script."; exit 1; }

  rclone config create dropbox dropbox token="$DROPBOX_TOKEN" --non-interactive >/dev/null
  echo "  dropbox: OK"
fi

# ── 4. Google Drive — create your own OAuth 2.0 client ────────────────────────

echo ""
echo "=== [2/2] Google Drive ==="
echo ""
echo "We create a Desktop OAuth client inside your own GCP project so the"
echo "consent screen shows YOUR app name instead of rclone's — no warning."
echo ""

if rclone listremotes | grep -q "^gdrive:"; then
  echo "  Already configured — skipping."
else

  # ── 4a. OAuth consent screen ────────────────────────────────────────────────
  echo "Step 1/3  Configure the OAuth consent screen"
  echo "  Opening: GCP Console → APIs & Services → OAuth consent screen"
  echo ""
  open "https://console.cloud.google.com/apis/credentials/consent?project=${GCP_PROJECT}" \
    2>/dev/null || true
  echo "  If this is a new project, do the following in the browser:"
  echo "    • User Type  → External → Create"
  echo "    • App name   → rclone-migration  (or anything)"
  echo "    • Support email → your email address"
  echo "    • Scroll to the bottom → Save and Continue"
  echo "    • Skip Scopes → Save and Continue"
  echo "    • Skip Test users → Save and Continue"
  echo "    • Back to Dashboard"
  echo ""
  echo "  If a consent screen already exists, no changes needed."
  echo ""
  read -rp "  Press Enter once the consent screen is ready..."

  # ── 4b. Create OAuth client credentials ─────────────────────────────────────
  echo ""
  echo "Step 2/3  Create a Desktop app OAuth client"
  echo "  Opening: GCP Console → APIs & Services → Credentials → Create credentials"
  echo ""
  open "https://console.cloud.google.com/apis/credentials/oauthclient?project=${GCP_PROJECT}" \
    2>/dev/null || true
  echo "  In the browser:"
  echo "    • Application type → Desktop app"
  echo "    • Name             → rclone-migration"
  echo "    • Click Create"
  echo "    • Click Download JSON  (saves a file like client_secret_xxx.json)"
  echo ""
  read -rp "  Paste the path to the downloaded JSON (or drag the file here): " CREDS_FILE
  # Strip shell quoting, leading/trailing spaces, and ANSI escape sequences
  CREDS_FILE=$(echo "$CREDS_FILE" | sed $'s/\x1b\\[[0-9;]*m//g' | tr -d "'" | xargs)

  [[ -f "$CREDS_FILE" ]] || { echo "File not found: $CREDS_FILE"; exit 1; }

  CLIENT_ID=$(python3 -c "
import json, sys
d = json.load(open(sys.argv[1]))
print(d['installed']['client_id'])
" "$CREDS_FILE")

  CLIENT_SECRET=$(python3 -c "
import json, sys
d = json.load(open(sys.argv[1]))
print(d['installed']['client_secret'])
" "$CREDS_FILE")

  echo "  Parsed client_id: ${CLIENT_ID:0:24}..."

  # ── 4c. Authorize Drive access ───────────────────────────────────────────────
  echo ""
  echo "Step 3/3  Authorize Google Drive access"
  echo "  Your browser will open for Google sign-in and Drive consent."
  echo "  The consent screen will now show 'rclone-migration' (your app)."
  echo ""
  read -rp "  Press Enter to open the browser..."

  TMPGD=$(mktemp)
  # rclone authorize prints "Paste the following..." then the token JSON on stdout
  rclone authorize "drive" \
    --client-id="$CLIENT_ID" \
    --client-secret="$CLIENT_SECRET" \
    > "$TMPGD" 2>&1

  GDRIVE_TOKEN=$(python3 - "$TMPGD" << 'PY'
import sys, re, json
content = open(sys.argv[1]).read()
for m in re.finditer(r'\{[^{}]*"access_token"[^{}]*\}', content):
    try:
        json.loads(m.group())
        print(m.group())
        break
    except json.JSONDecodeError:
        pass
PY
)
  rm -f "$TMPGD"

  [[ -n "$GDRIVE_TOKEN" ]] || { echo "No token received from Google Drive. Re-run the script."; exit 1; }

  rclone config create gdrive drive \
    client_id="$CLIENT_ID" \
    client_secret="$CLIENT_SECRET" \
    scope=drive \
    token="$GDRIVE_TOKEN" \
    --non-interactive >/dev/null

  echo "  gdrive: OK"
fi

# ── 5. Verify both remotes can list files ─────────────────────────────────────

echo ""
echo "Verifying remotes..."
rclone lsd dropbox: --max-depth 1 >/dev/null 2>&1 \
  && echo "  dropbox: reachable" \
  || { echo "  dropbox: FAILED — re-run the script"; exit 1; }
rclone lsd gdrive: --max-depth 1 >/dev/null 2>&1 \
  && echo "  gdrive:  reachable" \
  || { echo "  gdrive:  FAILED — re-run the script"; exit 1; }

# ── 6. Upload rclone config to Secret Manager ─────────────────────────────────

RCLONE_CONF="$(rclone config file 2>/dev/null | tail -1)"
[[ -f "$RCLONE_CONF" ]] || { echo "rclone config file not found at: $RCLONE_CONF"; exit 1; }

echo ""
echo "Uploading rclone config to Secret Manager..."
gcloud secrets describe "$SECRET_NAME" --project="$GCP_PROJECT" >/dev/null 2>&1 \
  || gcloud secrets create "$SECRET_NAME" \
       --project="$GCP_PROJECT" \
       --replication-policy=automatic

gcloud secrets versions add "$SECRET_NAME" \
  --project="$GCP_PROJECT" \
  --data-file="$RCLONE_CONF"

echo "  Stored: projects/$GCP_PROJECT/secrets/$SECRET_NAME"

# ── 7. Create VM service account ──────────────────────────────────────────────

echo "Creating service account..."
gcloud iam service-accounts describe "$SA_EMAIL" --project="$GCP_PROJECT" >/dev/null 2>&1 \
  || gcloud iam service-accounts create "$SA_NAME" \
       --display-name="Dropbox→Drive migrator" \
       --project="$GCP_PROJECT"

echo "Granting IAM roles..."
for role in roles/secretmanager.secretAccessor roles/compute.instanceAdmin.v1; do
  gcloud projects add-iam-policy-binding "$GCP_PROJECT" \
    --member="serviceAccount:$SA_EMAIL" \
    --role="$role" \
    --condition=None --quiet >/dev/null
done

echo ""
echo "All done. Start the migration:"
echo "  GCP_PROJECT=$GCP_PROJECT bash migrate/run.sh"
