# Conflict resolution

This document describes how homedrive detects and resolves file conflicts
between the local disk and Google Drive.

## When conflicts occur

A conflict exists when a file has been modified on both sides since the
last successful sync. Specifically, on push or pull, the remote `mtime`
or `md5` differs from what the local journal (BoltDB store) expected.

Example scenario: the daemon is offline for 10 minutes. During that
time, the user edits `Documents/notes.md` locally, and a collaborator
edits the same file on Drive. When the daemon reconnects, both the
watcher (push) and the Changes API (pull) report changes to the same
path.

## Detection

The store maintains a journal record for each synced file:

```
{
  path:           "Documents/notes.md"
  local_mtime:    2026-04-28T14:30:00Z
  remote_mtime:   2026-04-28T14:30:00Z
  remote_md5:     "abc123..."
  remote_id:      "1A2B3C..."
  last_synced_at: 2026-04-28T14:30:05Z
  last_origin:    "local"
}
```

Before syncing a file, the syncer checks:

1. **Push path**: the file's current `remote_mtime` and `remote_md5`
   match the stored values. If they differ, someone else modified the
   file remotely since the last sync -- conflict.

2. **Pull path**: the file's current `local_mtime` matches the stored
   value. If it differs, the user modified the file locally since the
   last sync -- conflict.

If both sides changed independently, the conflict policy determines
which version wins.

## Resolution policies

### `newer_wins` (default)

The version with the more recent `mtime` wins. The loser is preserved
as a `.old.<N>` backup.

**Case 1: local is newer** (`mtime(local) > mtime(remote)`)

- The local version is uploaded to Drive (overwrites remote).
- The remote version is renamed on Drive as `<path>.old.<N>`.
- The store is updated with the new sync state.

**Case 2: remote is newer** (`mtime(remote) > mtime(local)`)

- The remote version is downloaded to local disk (overwrites local).
- The local version is renamed locally as `<path>.old.<N>`.
- The store is updated with the new sync state.

**Case 3: equal mtime, different checksums**

This is rare (clocks aligned but content differs). Default behavior:

- Local wins (configurable).
- A warning is logged at `warn` level with both checksums.
- The loser (remote by default) is preserved as `.old.<N>` on its own
  side.

### `local_wins`

The local version always wins. The remote version is renamed as
`.old.<N>` on Drive. Useful when the local disk is strictly
authoritative and collaborators should not override local edits.

### `remote_wins`

The remote version always wins. The local version is renamed as
`.old.<N>` locally. Useful for read-mostly setups where Drive is the
primary editing location.

### `manual` (future, not implemented in v0.1)

Conflicts are not auto-resolved. The file is marked as
`conflict_pending` in the journal and exposed via:

- `GET /status` (lists pending conflicts)
- MQTT `conflict.pending` event

Resolution requires a CLI command:

```bash
homedrive ctl conflict resolve Documents/notes.md --keep-local
homedrive ctl conflict resolve Documents/notes.md --keep-remote
```

Until resolved, the file is not synced in either direction.

## `.old.<N>` naming convention

The loser of a conflict is preserved with a numeric suffix:

```
Documents/notes.md           # winner
Documents/notes.md.old.1     # first conflict loser
Documents/notes.md.old.2     # second conflict loser
Documents/notes.md.old.3     # third conflict loser (and so on)
```

The `.old.<N>` number (`<N>`) is computed from the store's old-suffix
index, not by listing the filesystem. This avoids race conditions when
multiple conflicts for the same file occur in rapid succession.

The `.old.<N>` file is created **on the same side as the loser**:

- If local wins, the `.old.<N>` is created on Drive.
- If remote wins, the `.old.<N>` is created on the local disk.

The `.old.<N>` files are **not** synced back to the other side. They
are archive copies for manual review.

## MQTT events

Every conflict emits two MQTT events in sequence:

### `conflict.detected`

Published when a conflict is identified, before resolution.

```json
{
  "ts": "2026-04-28T14:32:11Z",
  "type": "conflict.detected",
  "path": "Documents/notes.md",
  "local_mtime": "2026-04-28T14:32:00Z",
  "remote_mtime": "2026-04-28T14:31:45Z",
  "policy": "newer_wins"
}
```

### `conflict.resolved`

Published after the conflict is resolved.

```json
{
  "ts": "2026-04-28T14:32:12Z",
  "type": "conflict.resolved",
  "path": "Documents/notes.md",
  "local_mtime": "2026-04-28T14:32:00Z",
  "remote_mtime": "2026-04-28T14:31:45Z",
  "resolution": "newer_wins:local",
  "kept_old_as": "Documents/notes.md.old.3"
}
```

The `resolution` field indicates which side won:

- `newer_wins:local` -- local was newer, local uploaded
- `newer_wins:remote` -- remote was newer, remote downloaded
- `newer_wins:local:equal_mtime` -- mtimes equal, local chosen as default
- `local_wins:local` -- policy forced local win
- `remote_wins:remote` -- policy forced remote win

## Audit log

Every conflict is recorded in the JSONL audit log at
`/var/log/homedrive/audit.jsonl`:

```json
{"ts":"2026-04-28T14:32:12Z","op":"conflict","path":"Documents/notes.md","policy":"newer_wins","winner":"local","old_path":"Documents/notes.md.old.3","local_mtime":"2026-04-28T14:32:00Z","remote_mtime":"2026-04-28T14:31:45Z"}
```

## Logging

Conflict detection and resolution are logged at `info` level with
structured fields:

```
level=INFO msg="conflict detected" path=Documents/notes.md policy=newer_wins local_mtime=2026-04-28T14:32:00Z remote_mtime=2026-04-28T14:31:45Z
level=INFO msg="conflict resolved" path=Documents/notes.md winner=local kept_old_as=Documents/notes.md.old.3
```

The equal-mtime case (case 3) additionally logs at `warn` level:

```
level=WARN msg="conflict with equal mtime, checksums differ" path=Documents/notes.md local_md5=abc123 remote_md5=def456 default_winner=local
```

## Edge cases

### Conflict on a directory

Directory conflicts (e.g., renaming a directory to the same name on
both sides) are rare. In v0.1, directory-level conflicts are resolved
by the same newer-wins policy applied to the directory metadata. This
is a known limitation; complex directory tree merge conflicts are not
handled.

### Conflict during offline period

If the daemon is offline and multiple conflicting edits accumulate, the
first sync after reconnection processes them in order. Each conflict is
resolved independently according to the configured policy.

### `.old.<N>` accumulation

Old conflict backups are not automatically cleaned up. Users should
periodically review and delete `.old.<N>` files. A future version may
add configurable retention (e.g., keep only the last 5 per file).
