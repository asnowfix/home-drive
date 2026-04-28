# Architecture

This document describes the runtime architecture of homedrive, its
module layout, and the data flows for push and pull sync operations.

## Runtime topology

```
+------------------------------------------------------------+
|  homedrive run (single process)                            |
|                                                            |
|  watcher (fsnotify) --events--> debouncer (per-path)       |
|                              ^   + dir-rename pairer       |
|                              |                             |
|                              v                             |
|                          job queue                         |
|                              |                             |
|  pull ticker (30s, Changes API) ----> syncer (workers)     |
|  bisync ticker (1h, safety net) ---->                      |
|                              |                             |
|                              +---> rclone lib (drive only) |
|                              +---> store (Bolt)            |
|                              +---> mqtt.Publisher (paho)   |
|                              +---> slog                    |
|                                                            |
|  http server: /status /pause /resume /resync /reload       |
|              /healthz /metrics                             |
+------------------------------------------------------------+
```

homedrive runs as a single long-lived process managed by systemd. All
subsystems are started in `homedrive run` and communicate via Go
channels and shared interfaces. There is no IPC or subprocess spawning.

## Module layout

```
homedrive/
  cmd/
    homedrive/main.go       Entry point, cobra commands, signal handling
  internal/
    watcher/                 fsnotify recursive watch + debouncer + rename pairer
    syncer/                  Push/pull orchestration, conflict resolution
    rcloneclient/            Minimal rclone wrapper (drive backend only)
    store/                   BoltDB journal + .old.<N> index + audit log
    config/                  YAML + /etc/default loader, hot reload
    http/                    Control endpoint (status, pause, resume, etc.)
    mqtt/                    Paho wrapper, HA Discovery, topic builder
  pkg/
    homedrive/               Public types (reusable by other modules if needed)
  linux/
    homedrive@.service       Templated systemd unit (per user)
    homedrive.default        /etc/default/homedrive sample
    homedrive.logrotate      Logrotate configuration
    99-homedrive-inotify.conf  Sysctl tuning for inotify
    postinst.sh              Post-install script
  docs/                      This documentation directory
  PLAN.md                    Full execution plan and phase tracking
  README.md                  Project overview
```

All non-trivial logic lives in `internal/`. The `pkg/homedrive/` package
exports only shared type definitions that other modules in the workspace
(such as `myhome`) may import.

## Push flow: local change to Drive

```
1. File modified on disk
   |
2. fsnotify emits Write/Create/Remove/Rename event
   |
3. Watcher checks exclusion globs (defense in depth)
   |  Excluded? --> drop
   |
4. Per-path debouncer resets a 2s timer
   |  More events within 2s? --> timer resets
   |
5. Timer fires: watcher emits Event{Path, Op, At}
   |  (or DirRename{From, To, At} for paired directory renames)
   |
6. Syncer receives event from the job queue
   |
7. Loop prevention check: compare event mtime with store
   |  Matches last sync mtime (+/-1s)? --> drop (echo from pull)
   |
8. Conflict check: compare local state with remote state from store
   |  Conflict detected? --> apply conflict policy (see conflict-resolution.md)
   |
9. rclone wrapper executes the operation:
   |  - CopyFile for create/modify
   |  - DeleteFile for remove
   |  - MoveFile for directory rename (single API call)
   |
10. Store updated with new sync record
    |
11. MQTT event published (push.success or push.failure)
    |
12. Audit log entry appended (JSONL)
```

### Retry behavior

If step 9 fails (network error, transient API error), the syncer retries
with exponential backoff: 5s initial, doubling up to 5m, for a maximum
of 5 attempts. Failed operations remain in the queue and are retried
on the next cycle.

### Directory rename optimization

When the watcher detects a paired directory rename (via inotify cookies),
only a single `DirRename` event reaches the syncer. The syncer calls
`MoveFile` once (a metadata-only Drive API call) and rewrites all store
keys with the old path prefix in a single BoltDB transaction. See
[directory-rename.md](directory-rename.md) for details.

## Pull flow: Drive change to local disk

```
1. Pull ticker fires (every 30s)
   |
2. rclone wrapper calls Drive Changes API with persisted pageToken
   |
3. Each Change is processed:
   |  - New/modified file: download to local_root
   |  - Deleted file: remove from local disk
   |  - Moved/renamed: move on local disk
   |
4. Loop prevention: store updated with {path, local_mtime, remote_mtime, ...}
   |  (subsequent watcher events matching this mtime are dropped)
   |
5. Conflict check (if local file was also modified since last sync)
   |  Conflict? --> apply conflict policy
   |
6. New pageToken persisted in BoltDB
   |
7. MQTT event published (pull.success or pull.failure)
   |
8. Audit log entry appended
```

### 410 GONE handling

If the Drive Changes API returns HTTP 410 (token expired), the pull
subsystem resets via `getStartPageToken`, emits an MQTT warning, and
resumes polling. No data is lost; the next bisync pass catches any
missed changes.

## Bisync safety net

The bisync pass runs every hour (configurable) as a full directory
comparison between local and remote state. It serves as a safety net
for:

- Bugs in the event-driven push/pull path
- Missed inotify events (kernel buffer overflow on very high churn)
- Daemon restarts where the pageToken was not persisted

During bisync, a global `sync.RWMutex` write lock is acquired, blocking
all push and pull workers until the comparison completes. This prevents
race conditions between the bisync diff and in-flight operations.

Bisync records its duration, files changed, and conflicts found in the
audit log.

## Store (BoltDB journal)

The store is the single source of truth for sync state. Each synced file
has a record:

```
{
  path:           string  // relative to local_root
  local_mtime:    time    // last known local modification time
  remote_mtime:   time    // last known remote modification time
  remote_md5:     string  // remote checksum
  remote_id:      string  // Drive file ID
  last_synced_at: time    // when this record was last updated
  last_origin:    string  // "local" or "remote"
}
```

The store also maintains:

- **pageToken**: the Drive Changes API continuation token
- **old_suffix_index**: tracks `.old.<N>` numbering per path prefix
- **audit log**: JSONL file at `/var/log/homedrive/audit.jsonl`,
  rotated weekly by logrotate

## Configuration hot reload

Configuration can be reloaded without restarting the daemon:

- `SIGHUP` signal to the process
- `POST /reload` on the HTTP control endpoint

Hot reload updates: exclusion globs, debounce window, push worker
count, pull intervals, MQTT settings, log level, and quota thresholds.
It does not change `local_root` or `remote` (requires a restart).

## Quota handling

The rclone wrapper polls Drive quota every 5 minutes:

- At `warn_pct` (default 90%): MQTT `quota.warning` event published
- At `stop_push_pct` (default 99%): push workers are paused, pull
  continues, MQTT `quota.exhausted` event, status becomes
  `quota_blocked`
- Resume: pushes resume when quota drops below `stop_push_pct - 5%`
  (hysteresis to prevent flapping)

## HTTP control endpoint

Listens on `127.0.0.1:6090` (loopback only, no authentication required).

| Route | Method | Purpose |
|---|---|---|
| `/status` | GET | JSON: state, queues, last syncs, quota, version |
| `/pause` | POST | Pause watcher and workers |
| `/resume` | POST | Resume operations |
| `/resync` | POST | Force immediate bisync |
| `/reload` | POST | Reload configuration (same as SIGHUP) |
| `/healthz` | GET | 200 if healthy, 503 if OAuth/MQTT/disk issue |
| `/metrics` | GET | Prometheus exposition format |

CLI subcommands (`homedrive ctl status`, etc.) are thin HTTP clients
that call these endpoints.
