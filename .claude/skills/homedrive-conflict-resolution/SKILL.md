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

## Computing `<N>`: the `oldsuffix` package

`<N>` is the smallest positive integer such that `<path>.old.<N>` does
not already exist according to the journal. Use the journal, **not** the
filesystem listing — this avoids races and works identically for the
remote side.

All of that logic — parsing the suffix, deciding whether to collapse onto
an existing base, and finding the next free `N` — lives in
`internal/oldsuffix` (`Matcher.Format`, `Matcher.TrimOne`, `Matcher.Base`,
`oldsuffix.NextOldN`). It used to be reimplemented three times
(`store.Journal.NextOldN`, `store.ConflictResolver.nextOldN`,
`syncer.Bisync.nextOldN`), and none of them checked whether the path they
were handed already carried a suffix — that produced unbounded chains
like `file.old.1.old.1.old.1...` (issue #65). Every call site now
delegates to this one package:

```go
base, n := oldsuffix.NextOldN(matcher, path, journal.Exists)
oldPath := matcher.Format(base, n)
```

**Collapsing, not nesting.** If `path` already carries one or more
`.old.<N>` suffixes *and* its fully-stripped base is a real, tracked
file, `NextOldN` collapses onto that base instead of nesting a new suffix
on top: a repeat conflict on `f.md.old.1` yields `f.md.old.2`, never
`f.md.old.1.old.1`. If the base is *not* a known file, `path` is treated
literally — a file that merely happens to be named like a conflict
artifact (e.g. a user's own `budget.old.2`) keeps its own numbering space
and is never silently renumbered onto an unrelated `budget`.

Never hand-roll the `"%s.old.%d"` format string or a `nextOldN`-style
loop at a new call site — always go through `oldsuffix.New`/`Matcher`/
`NextOldN`, or the format becomes hardcoded again and
`conflict.old_suffix_format` silently stops working (this exact bug
shipped once already).

## Format

`old_suffix_format` from config (default `".old.%d"`):
```
notes.md          → notes.md.old.1
photo.jpg         → photo.jpg.old.3
.bashrc           → .bashrc.old.1
```

## Retention, GC, and chain repair

`conflict.retention` bounds how many `.old.<N>` siblings a base file
accumulates: `max_per_file` (default 3) and `max_age` (default off —
age-based eviction can delete a user's only surviving copy of a genuine
edit purely because time passed, so it's opt-in). Enforced two ways:

- **Inline**, right after a new loser's journal entry is written
  (`store.PruneOldSiblings`, called from both the pull path and bisync).
- **Periodic sweep**, piggybacked on the bisync tick
  (`store.SweepOldFiles`, `conflict.retention.sweep_interval`, default
  `24h`) — this is what reclaims siblings on a file that never conflicts
  again after the inline pass ran.

Deletion order is normative: the file/remote object is removed **before**
its journal entry, never the reverse. That's what makes "smallest free
N" reuse crash-safe — a crash between the two steps only leaves an
orphan journal entry (reserving an N a little longer than necessary),
never a file on disk with no journal record that a later conflict could
silently overwrite.

`syncer.RepairChains` (`internal/syncer/repair.go`) is the one-time fix
for chains that had already nested *before* the collapsing fix above
shipped. It runs once automatically (`conflict.repair_chains: true`, the
default) on the first bisync pass after upgrade, and on demand via
`POST /conflict/repair` / `homedrive ctl conflict repair [--dry-run]`.
Driven by a fresh local walk + remote listing, not the journal (some
pre-existing chain links may never have gotten a journal entry). See
PLAN.md §11.5 for the full design.

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
| Repeat conflict on a suffixed path | conflict on `f.md.old.1` | collapses to `f.md.old.2`, never `f.md.old.1.old.1` |
| Suffix-like but unrelated file | conflict involving `budget.old.2` with no `budget` base | numbering not collapsed onto `budget` |
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
- Don't nest `.old.<N>` suffixes on top of an already-suffixed path.
  Always collapse onto the tracked base via `oldsuffix.NextOldN` — nesting
  is exactly the bug (issue #65) that produced unbounded
  `file.old.1.old.1.old.1...` chains in production.
- Don't hardcode `"%s.old.%d"` (or any other literal suffix format) at a
  new call site. Go through `internal/oldsuffix`'s `Matcher`, or a
  configured `conflict.old_suffix_format` silently stops taking effect.
