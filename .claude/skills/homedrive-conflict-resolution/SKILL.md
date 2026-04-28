---
name: homedrive-conflict-resolution
description: The newer-wins conflict resolution algorithm for homedrive — detection, resolution, .old.<N> naming, MQTT events, and mandatory tests. Apply whenever working on the syncer, store, or any code path that compares local and remote file state.
---

# Conflict resolution: newer wins, loser kept as `.old.<N>`

## Detection

A conflict exists when, on push or pull, the remote `mtime` or `md5`
differs from what the local journal expected.

The journal (BoltDB) stores per path:
```go
type JournalEntry struct {
    Path           string
    LocalMtime     time.Time
    RemoteMtime    time.Time
    RemoteMD5      string
    RemoteID       string  // Drive file ID
    LastSyncedAt   time.Time
    LastOrigin     string  // "local" | "remote"
}
```

A push that finds `Stat(remote)` ≠ journal expectation = conflict.
A pull that finds local mtime/size ≠ journal expectation = conflict.

## Algorithm: `newer_wins`

```
1. mtime(local) > mtime(remote):
     → upload local
     → rename remote: <path> → <path>.old.<N>  (REMOTE side)
2. mtime(remote) > mtime(local):
     → download remote
     → rename local:  <path> → <path>.old.<N>  (LOCAL side)
3. mtime equal but checksums differ:
     → log warning
     → default: local wins (configurable to remote)
     → loser kept as .old.<N> on its own side
```

Key rules:
- `.old.<N>` is created on the **same side as the loser**, never both.
- The winner is preserved at the original path.
- Both sides eventually re-sync to converge.

## Computing `<N>`

`<N>` is the smallest positive integer such that `<path>.old.<N>` does
not already exist according to the journal:

```go
func nextOldN(j *Journal, path string) int {
    n := 1
    for {
        if !j.Exists(fmt.Sprintf("%s.old.%d", path, n)) {
            return n
        }
        n++
    }
}
```

Use the journal, **not** the filesystem listing. This avoids races and
works identically for the remote side.

## Format

`old_suffix_format` from config (default `".old.%d"`):
```
notes.md          → notes.md.old.1
photo.jpg         → photo.jpg.old.3
.bashrc           → .bashrc.old.1
```

## Manual mode (out of scope v0.1)

If `policy: manual`:
- Conflicts are not auto-resolved.
- The file is locked in the journal as `conflict_pending`.
- Exposed via `GET /status` and a dedicated MQTT event.
- CLI command `homedrive ctl conflict resolve <path> [--keep-local|--keep-remote]`
  to land in a later phase.

## MQTT events

For every conflict, emit two events:

1. `conflict.detected`:
```json
{
  "ts": "2026-04-28T14:32:11Z",
  "type": "conflict.detected",
  "path": "Documents/notes.md",
  "local_mtime": "2026-04-28T14:32:00Z",
  "remote_mtime": "2026-04-28T14:31:45Z"
}
```

2. `conflict.resolved`:
```json
{
  "ts": "2026-04-28T14:32:12Z",
  "type": "conflict.resolved",
  "path": "Documents/notes.md",
  "resolution": "newer_wins:local",
  "kept_old_as": "Documents/notes.md.old.3"
}
```

## Audit log

Each conflict appends a JSONL line to `/var/log/homedrive/audit.jsonl`:
```json
{"ts":"...","op":"conflict","path":"...","resolution":"...","old_path":"..."}
```

## Log level

- `INFO`: every conflict, with structured fields.
- `WARN`: equal-mtime case (rare, suggests clock issue).
- `ERROR`: rename of loser failed (data integrity risk).

## Mandatory tests

Every modification to conflict resolution must keep these passing:

| Case | Setup | Assertion |
|---|---|---|
| Local newer | local.mtime > remote.mtime | upload, remote → `.old.1` |
| Remote newer | remote.mtime > local.mtime | download, local → `.old.1` |
| Equal mtime, diff md5, default | configurable | local wins, remote → `.old.1` |
| Equal mtime, diff md5, remote_wins | policy override | remote wins, local → `.old.1` |
| `<N>` collision | `.old.1` already exists | new file becomes `.old.2` |
| Loser rename fails | mock returns error | conflict left in journal as pending |
| Missing journal entry | first sync ever | not a conflict, just a sync |
| Both sides deleted | local + remote gone | journal entry removed, no conflict |

Tests live in `internal/syncer/conflict_test.go` and use `MemFS`.

## Loop prevention

After resolving and syncing, write the **new** journal entry. The
watcher's mtime guard then ignores the upcoming local event for the
loser's `.old.<N>` rename, preventing a re-upload loop.

## What NOT to do

- Don't delete the loser. Always preserve as `.old.<N>` so users can
  recover.
- Don't compare via filesystem listing. Always use the journal.
- Don't compute `<N>` on both sides independently — they must agree.
- Don't emit per-file events for conflicts inside a `dir_rename`
  operation. Document and resolve at the directory level (rare; v0.1
  limitation).
