# Directory rename handling

This document describes how homedrive efficiently handles directory
renames, avoiding the naive approach of re-syncing every file in the
renamed subtree.

## The problem

When a user renames a directory:

```bash
mv ~/gdrive/Photos_2024 ~/gdrive/Photos_Archive
```

A naive sync agent sees this as:

- 50,000 `DELETE` events (one per file under the old name)
- 50,000 `CREATE` events (one per file under the new name)

This translates to 100,000 Drive API calls, potentially taking hours and
consuming significant API quota. The actual operation should be a single
metadata update on the Drive folder object.

## How inotify reports renames

When a directory is renamed within a watched tree, the Linux inotify
subsystem emits a pair of events sharing a numeric **cookie**:

1. `IN_MOVED_FROM` on the source parent directory (cookie=N,
   name=`Photos_2024`)
2. `IN_MOVED_TO` on the destination parent directory (cookie=N,
   name=`Photos_Archive`)

The fsnotify library exposes these as `Rename` and `Create` events,
preserving the cookie value.

## The rename pairer algorithm

homedrive's watcher includes a **rename pairer** that detects and
collapses these paired events:

```
1. Receive a Rename event for a directory.
   - Buffer it in a map keyed by cookie.
   - Start a timer for dir_rename_pair_window (default 500ms).

2. Receive a Create event for a directory with the same cookie.
   - Match it with the buffered Rename.
   - Cancel the timer.
   - Emit a single DirRename{From, To, At} event.

3. If the timer fires without a matching Create:
   - The Rename is unpaired (see "Unpaired events" below).
   - Fall back to standard delete handling.
```

After pairing:

- The old subtree watches are removed (paths are stale).
- New watches are added for the renamed subtree.
- All child `Rename`/`Create` events arriving within the pairing window
  from the renamed subtree are **suppressed** -- they are redundant
  echoes from the kernel reporting individual file moves.
- A `cookie_seen` set prevents duplicate processing of late-arriving
  paired events.

## Syncer behavior on DirRename

When the syncer receives a `DirRename{From, To}` event:

1. **Drive API**: a single `operations.MoveFile` call updates the folder's
   parent ID and name. This is a metadata-only operation; Drive does not
   move individual files. Execution time is O(1) regardless of subtree
   size.

2. **Store (BoltDB)**: a single transaction rewrites all journal keys
   with the prefix `From/` to `To/`. Implementation: open a cursor on
   the prefix, batch put/delete pairs in one transaction.

3. **Audit log**: one JSONL line:
   ```json
   {"op": "dir_rename", "from": "Photos_2024", "to": "Photos_Archive", "files_count": 50000}
   ```

4. **MQTT**: one `dir_rename` event. No per-file events are published.

## Performance characteristics

| Metric | Naive approach | homedrive |
|---|---|---|
| Watcher events processed | 100,000 | 1 (paired) |
| Drive API calls | 100,000 | 1 |
| BoltDB transactions | 100,000 | 1 |
| MQTT events | 100,000 | 1 |
| Latency (50k files) | Hours | < 100ms watcher + 1 API call |
| API quota consumed | 100,000 units | 1 unit |

The performance improvement is proportional to subtree size.

## Edge cases

### Cross-mount rename

```bash
mv /mnt/disk1/Photos /mnt/disk2/Photos
```

inotify cannot pair events across different mount points (different
inode namespaces). The watcher receives only a `Rename` event on the
source mount with no matching `Create` on the destination.

Behavior: the pairing window expires, and the rename is treated as a
delete on the source side. If the destination mount is also watched,
the `Create` is treated as a recursive new directory (full walk + push).
This is the slow path but is correct.

### Rename into the watched tree from outside

```bash
mv /tmp/new_photos ~/gdrive/Photos
```

Only a `Create` event arrives (no `Rename` from the source, which is
outside the watched tree). The watcher treats this as a new directory:
it walks the subtree, adds watches, and pushes each file individually.

### Rename out of the watched tree

```bash
mv ~/gdrive/Photos /tmp/archive
```

Only a `Rename` event arrives. After the pairing window expires without
a match, the watcher treats this as a directory deletion. The syncer
removes the corresponding files from Drive.

### Rapid double rename

```bash
mv dir_a dir_b
mv dir_b dir_c    # within the 500ms window
```

Each rename generates its own cookie pair. The pairer matches by cookie
value, not by name, so both pairs are processed independently:

1. First pair: `DirRename{dir_a, dir_b}`
2. Second pair: `DirRename{dir_b, dir_c}`

The syncer processes them in order. The net result on Drive is a single
rename from `dir_a` to `dir_c` (two API calls, but correct).

### Rename of a directory containing only excluded files

```bash
mv ~/gdrive/build_cache ~/gdrive/old_cache
# build_cache contains only *.tmp files (excluded by glob)
```

The rename pairer operates at the directory level, independent of the
directory's contents. The `DirRename` event is emitted and the Drive
folder is renamed. The excluded files inside are not synced in either
case, but the folder structure is maintained.

### Conflict during rename

If the destination name already exists as a different folder on Drive
(e.g., another device created `Photos_Archive` while this device was
offline), the standard conflict resolution policy applies to the
directory metadata. In v0.1, this is handled as a best-effort rename
with a warning logged. Complex directory merge conflicts are out of
scope.

## Configuration

The pairing window is configurable in `config.yaml`:

```yaml
watcher:
  dir_rename_pair_window: 500ms
```

The default of 500ms is generous for local inotify events, which
typically arrive within microseconds. A longer window may be needed on
very slow filesystems (e.g., network-mounted CIFS).

## Testing

The following test scenarios are mandatory for any changes to the rename
pairer or its syncer integration:

1. `mv dir_with_50k_files dest` -- exactly 1 Drive call, watcher
   latency < 100ms.
2. `mv` then `mv` back within 1s -- 2 paired events handled correctly.
3. `mv` across mount points -- falls back gracefully, no events lost.
4. `mv` of a directory containing only excluded files -- still 1 paired
   event.

Tests requiring real inotify cookies must skip on non-Linux:

```go
if runtime.GOOS != "linux" {
    t.Skip("rename pairer requires Linux inotify cookies")
}
```

On macOS (development host), FSEvents does not provide rename cookies.
Full validation requires Linux, either via OrbStack or CI.
