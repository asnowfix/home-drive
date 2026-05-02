---
name: homedrive-watcher-rename
description: Algorithm and invariants for the directory rename pairer in homedrive's fsnotify watcher. Apply whenever modifying the watcher, the rename detection logic, or running performance tests on directory moves.
---

# Watcher: directory rename pairing

The most performance-critical edge case. A naive implementation turns
`mv dir_50k other_dir` into 50k+ events and 50k Drive API calls. The
algorithm below collapses it to **1 event, 1 Drive call, 1 Bolt TX**.

## Cookie pairing

When inotify renames within a watched tree, it emits paired events with
a shared cookie:

- `IN_MOVED_FROM` on the source parent (cookie=N, name=`dir_50k`)
- `IN_MOVED_TO` on the destination parent (cookie=N, name=`dir_50k`)

fsnotify exposes these as `Rename` then `Create` events on the parents,
preserving the cookie in `event.Op`.

## Algorithm

1. **Buffer** every `Rename` event for `dir_rename_pair_window` (default
   500ms, configurable in `watcher.dir_rename_pair_window`).
2. **Match** with subsequent `Create` events by cookie.
3. On a successful pair within the window:
   - Emit a single `DirRename{From, To, At}` event downstream.
   - **Remove** the old subtree watches (paths changed; existing watches
     are still valid by inode but produce events with wrong path strings).
   - **Add** new watches under the new path.
   - **Suppress** subsequent `Rename`/`Create` events from children of
     the renamed dir within the window — they are redundant.
   - Add the cookie to `cookie_seen` to deduplicate against late
     duplicates.
4. On window expiry without a match:
   - Fall back to standard delete/create handling per file (slow path,
     but correct).

## Edge cases (must be tested)

- **Cross-mount rename**: cookies don't match across inode spaces. Falls
  back to delete+create. Documented in `docs/directory-rename.md`.
- **Unwatched dir moved into watched tree**: only `Create` arrives, no
  pair. Treat as recursive create (full Walk + push).
- **Rapid double-rename within window**: the pairer matches by cookie
  order; second pair processed independently after first's `Create` is
  consumed.
- **Conflict during rename**: if destination already exists remotely,
  apply the standard §11 conflict resolution at the directory level.
  Documented as a known v0.1 limitation.
- **`IN_MOVE_SELF` fires after pairing**: when the source directory has
  its own inotify watch, the kernel fires `IN_MOVE_SELF` on that watch
  descriptor in addition to `IN_MOVED_FROM` on the parent. This produces
  a second `Rename(srcDir)` event that can arrive **after** the pair
  completes — at which point `srcDir` has been untracked by
  `removeSubtreeWatches`. `isSuppressed` must match the directory path
  itself, not only paths beneath it. `strings.HasPrefix(path, "srcDir/")`
  silently misses `path == "srcDir"`. Fix: also check
  `path == strings.TrimSuffix(prefix, string(filepath.Separator))`.

## Syncer behavior on `DirRename`

```
1. Drive: operations.MoveFile(remoteFromID, newParentID, newName)
   — Drive treats this as a parent-ID metadata change, O(1) regardless
   of subtree size, single API call.
2. Store: single Bolt transaction rewriting all keys with prefix `from/`
   to prefix `to/`. Open a cursor on the prefix, batch put/delete pairs
   in one TX.
3. Audit log: one JSONL line `{op: dir_rename, from, to, files_count}`.
4. MQTT: one event `dir_rename`, no per-file events.
```

## Mandatory test scenarios

These must pass in `internal/watcher/` tests:

| Scenario | Assertion |
|---|---|
| `mv dir_5k other_dir` | exactly 1 `DirRename` event |
| `mv dir_5k other_dir` | < 100ms watcher latency |
| `mv` then `mv` back within 1s | 2 paired events handled |
| `mv` across mount points | falls back to per-file, no event lost |
| `mv` of dir with 1 excluded file | 1 paired event, excluded file not synced |

In syncer tests:

| Scenario | Assertion |
|---|---|
| `DirRename{from, to}` event | exactly 1 `MoveFile` Drive call |
| `DirRename{from, to}` event | exactly 1 Bolt TX |
| 50k entries in store under `from/` | renamed correctly under `to/` |

## Performance assertion

`MoveFile` on Drive is O(1) — a parent-ID metadata change. Asserting
"single Drive call" in tests guarantees performance regardless of
subtree size. The Bolt TX is also O(N) in entries but a single TX, not
N transactions.

## Implementation hints

- Pair buffer keyed by **path** (not cookie): `map[string]renameEntry`
  with a `time.Timer` per entry for window expiry. The path key is the
  old directory path as reported by fsnotify.
- Use a mutex around the buffer; events arrive on the fsnotify goroutine.
- The suppression set for child events expires when the pair completes
  (or shortly after, e.g. 100ms grace).
- `isSuppressed(path)` must cover **both** the directory itself and its
  children: check `path == dirPath || strings.HasPrefix(path, dirPath+"/")`.
  Checking only the `dirPath + "/"` prefix misses the `IN_MOVE_SELF` event
  on the directory's own wd.
- When re-watching, do it in the order: remove old → add new. Brief
  window where events on the renamed subtree are dropped is acceptable
  given the suppression set.

## What NOT to do

- Don't try to detect rename by comparing inode metadata after the
  events arrive. Rely on cookies — they exist for this exact purpose.
- Don't process child events first and then "undo" them. The suppression
  must be proactive within the pair window.
- Don't expand `DirRename` into per-file events at the syncer. The whole
  point is the single Drive call.
