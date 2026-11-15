# Application Privacy Policy

**Application:** homedrive, a Google Drive sync agent
**Effective date:** 2026-11-15
**Source code and issues:** https://github.com/asnowfix/home-drive

## Summary

homedrive runs on your own computer or server. The maintainer does not run
a homedrive service, and homedrive does not send anything to the maintainer.
Your files go to your own Google Drive. homedrive has no telemetry, analytics
or crash reporting.

## What homedrive accesses

When you authorize homedrive with Google, it uses the Google Drive API with
the scope `https://www.googleapis.com/auth/drive`. With that scope it can read,
create, update and delete files in your Google Drive, and read Drive change
lists, so that changes made elsewhere are pulled to your machine. It reads the
files in the local folder you configure (`local_root`).

## Where data goes

homedrive sends data only to the following places:

- **Google Drive.** File contents and file metadata go directly from your
  machine to the Google Drive API, using the OAuth token you store locally.
- **An MQTT broker you configure, if you enable MQTT.** Messages include sync
  status, queue depth, quota usage and conflict events. Some events include
  file paths. MQTT is off unless you turn it on, and the broker address is one
  you set.
- **The local control endpoint.** homedrive's HTTP control endpoint listens on
  `127.0.0.1:6090` by default, which is reachable only from your machine. If you
  change the listen address to a non-loopback address, homedrive refuses to start
  unless you also set an auth token.

homedrive does not send data to the maintainer or to any other party.

## What is stored on your machine

- **rclone.conf** (the path is set in your config). It holds the OAuth token for
  your Drive remote and, if you set them, the OAuth client ID and secret. Protect
  this file with file permissions.
- **State database** (`state.path`). It records file paths, modification times
  and sync results. homedrive uses it to avoid re-syncing its own writes and to
  number conflict copies.
- **Audit log** (`state.audit_log`) and the service log. These contain operational
  messages and may include file paths.

You control all of these files. Deleting them removes homedrive's local state.
Deleting files in Google Drive is governed by Google.

## Deletion behaviour

When a local file is deleted, homedrive deletes the matching file in Google Drive
through rclone. rclone's Drive backend sends files to the Drive trash by default
(`use_trash` defaults to true), unless your rclone.conf sets `use_trash = false`.

## Google user data and Limited Use

homedrive's use of information received from Google APIs will adhere to Google
API Services User Data Policy, including the Limited Use requirements.

Google user data obtained through homedrive is used only to sync your files.
It is not sold, not used for advertising, and not transferred to third parties.
The maintainer does not receive it.

## Your choices

- Stop homedrive and remove its configuration and state files at any time.
- Revoke homedrive's access in your Google Account's third-party access settings.

## Contact

Questions about this policy: open an issue at
https://github.com/asnowfix/home-drive/issues.

## Changes

If this policy changes, the new version is committed to the repository and the
effective date above is updated.
