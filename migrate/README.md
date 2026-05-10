# Dropbox → Google Drive migration

One-shot migration of a personal Dropbox account to Google Drive using
[rclone](https://rclone.org) running on a preemptible GCE VM.
No data transits your local machine — everything flows Dropbox → VM → Drive.

## Prerequisites

```bash
brew install rclone google-cloud-sdk
gcloud auth login
gcloud auth application-default login
```

You need Owner or Editor on a GCP project with billing enabled.

## Step 1 — Setup (run once, on your Mac)

```bash
GCP_PROJECT=my-project bash migrate/setup.sh
```

The script walks through every auth step interactively:

| Step | What happens |
|---|---|
| Dropbox OAuth | Browser opens → sign in → Allow. Token saved automatically. |
| GCP consent screen | Browser opens the GCP Console. Fill in app name + support email, save. Skip scopes and test users. |
| Drive OAuth client | Browser opens credential creation. Pick **Desktop app**, name it anything, click **Create**, **Download JSON**. Drag the file into the terminal prompt. |
| Drive OAuth | Browser opens → sign in → Allow. The consent screen now shows *your* app name — no "unverified app" warning. |
| Verification | Script calls `rclone lsd` against both remotes to confirm they work. |
| Secret Manager | The rclone config (containing both OAuth tokens) is stored as `dropbox-gdrive-rclone-conf`. |
| Service account | `dropbox-gdrive-migrator@PROJECT.iam.gserviceaccount.com` is created with the two roles the VM needs. |

If a remote is already configured in `~/.config/rclone/rclone.conf`, that step is skipped.

## Step 2 — Run the migration

```bash
GCP_PROJECT=my-project bash migrate/run.sh
```

This creates a `SPOT e2-micro` VM in `us-central1-a` that:
1. Installs the latest rclone release.
2. Pulls `~/.config/rclone/rclone.conf` from Secret Manager.
3. Runs `rclone copy dropbox: gdrive:`.
4. Self-deletes when done.

For 2 GB expect 15–30 minutes. Cost: < $0.01.

### Optional environment variables

| Variable | Default | Example |
|---|---|---|
| `GCP_ZONE` | `us-central1-a` | `europe-west1-b` |
| `MACHINE_TYPE` | `e2-micro` | `e2-small` (more RAM if needed) |
| `DROPBOX_SRC` | *(root)* | `photos` — sync only one subfolder |
| `GDRIVE_DST` | *(root)* | `Dropbox-import` — land files in a subfolder |

```bash
GCP_PROJECT=my-project GDRIVE_DST=Dropbox-import bash migrate/run.sh
```

### Watch progress

```bash
# Structured logs (appear ~1 min after VM boot)
gcloud logging read \
  'resource.type=gce_instance AND jsonPayload.MESSAGE=~"rclone-migration"' \
  --project=my-project --order=asc --freshness=2h \
  --format='table(timestamp,jsonPayload.MESSAGE)'

# Raw serial port output (good for startup errors)
gcloud compute instances get-serial-port-output INSTANCE_NAME \
  --zone=us-central1-a --project=my-project
```

The instance name is printed by `run.sh` when the VM is created
(`dropbox-gdrive-YYYYMMDD-HHMMSS`).

## Re-running after preemption

`rclone copy` only transfers files that are missing or differ on the
destination, so re-running `run.sh` is safe at any point.

## Cleanup

The VM self-deletes on success. To remove the remaining GCP resources:

```bash
# Remove the secret
gcloud secrets delete dropbox-gdrive-rclone-conf --project=my-project

# Remove the service account
gcloud iam service-accounts delete \
  dropbox-gdrive-migrator@my-project.iam.gserviceaccount.com \
  --project=my-project
```
