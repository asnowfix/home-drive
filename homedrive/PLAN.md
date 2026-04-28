# PLAN — `homedrive`: bidirectional Google Drive sync for Linux NAS

> Execution document for Claude Code, aligned with the conventions of
> `github.com/asnowfix/home-automation` (Go workspace, `cmd/`, `pkg/`,
> `hlog`, templated systemd, MQTT, goreleaser).

---

## 1. Goal & scope

Replace paid Dropbox with a **headless ARM64** Google Drive client
(Raspberry Pi NAS) providing:

- **Offline-first** storage (the external disk is the local source of truth).
- **Real-time push** of local modifications to Drive (on file close).
- **Periodic pull** of remote modifications (Drive Changes API every 30s).
- Conflict policy: *newer wins*, the loser kept as `.old.<N>`.
- Exclusion filters (editor caches, `.git`, temp files).
- Templated systemd service per user, env in `/etc/default/`.
- HTTP control endpoint, structured `slog` logs, MQTT for Home Assistant.
- **Minimal** rclone import (only `backend/drive` + `fs/sync`).
- `--dry-run` mode available from day 1.
- **Efficient directory rename handling** (single Drive call, not 50k).
- MQTT wrapper designed for future cross-device sync (peer discovery, lock
  coordination, conflict voting).

Out of scope for v0.1: multi-pair sync, GUI, Windows, macOS, Google
Docs/Sheets/Slides binary export (skipped + warned), cross-device peer sync
(designed for, not implemented).

---

## 2. Decisions locked in

| Topic | Decision |
|---|---|
| Project name | `homedrive` |
| inotify library | `fsnotify/fsnotify` (no spike, well-known patterns) |
| Pull strategy | Drive Changes API every 30s + hourly bisync safety net |
| Config format | YAML rich + `/etc/default/homedrive` minimal for systemd |
| HTTP control port | `6090` (loopback) |
| Dry-run | Supported from Phase 0 (`--dry-run` flag, no remote writes) |
| Google native files | Skip + log warning, out of scope v0.1 |
| MQTT publisher | `eclipse/paho.mqtt.golang` wrapped in `internal/mqtt/` |
| Audit log retention | JSONL + logrotate weekly, keep 12 |
| Quota full behavior | Stop pushes, keep pulls running, MQTT alert |
| sysctl `inotify.max_user_watches` | Set at package install (postinst), not via systemd |
| Home Assistant integration | Enabled, MQTT discovery |
| Directory rename handling | Cookie-paired event detection, single Drive `MoveFile` call |
| Dev environment | macOS host + OrbStack Ubuntu VM for Linux tests |

---

## 3. Target architecture

### 3.1 Repo layout

New Go module at the repo root, alongside `myhome/`, `myip/`, `myzone/`:

```
homedrive/
├── cmd/
│   └── homedrive/main.go      # binary (cobra: `homedrive run`, `homedrive ctl ...`)
├── internal/
│   ├── watcher/               # fsnotify recursive + per-path debouncer + dir-rename pairing
│   ├── syncer/                # push/pull logic + conflict resolution
│   ├── rcloneclient/          # minimal wrapper around rclone libraries
│   ├── store/                 # local journal (BoltDB) + .old.<N> index
│   ├── config/                # loader for /etc/default + YAML + flags
│   ├── http/                  # /status /pause /resume /resync /reload
│   └── mqtt/                  # paho wrapper, HA discovery, future peer sync
├── pkg/
│   └── homedrive/             # public types (reusable by myhome if needed)
├── linux/
│   ├── homedrive@.service     # templated systemd unit (per user)
│   ├── homedrive.default      # /etc/default/homedrive sample
│   ├── homedrive.logrotate    # /etc/logrotate.d/homedrive
│   ├── 99-homedrive-inotify.conf  # /etc/sysctl.d/ tuning
│   └── postinst.sh            # applies sysctl + creates /var/lib /var/log
├── docs/
│   ├── architecture.md
│   ├── home-assistant.md
│   ├── conflict-resolution.md
│   ├── directory-rename.md
│   ├── dev-environment.md
│   └── manual-validation.md
├── README.md
└── PLAN.md                    # this document, copied to module root
```

Add `./homedrive` to `go.work`. Add a `homedrive` build entry in
`.goreleaser.yml` targeting `linux/arm64` and `linux/amd64`.

### 3.2 Runtime topology

```
┌────────────────────────────────────────────────────────────┐
│  homedrive run (single process)                            │
│                                                            │
│  watcher (fsnotify) ──events──▶ debouncer (per-path)      │
│                              ▲   + dir-rename pairer       │
│                              │                             │
│                              ▼                             │
│                          job queue                         │
│                              │                             │
│  pull ticker (30s, Changes API) ───▶ syncer (workers)     │
│  bisync ticker (1h, safety net) ───▶                      │
│                              │                             │
│                              ├─▶ rclone lib (drive only)  │
│                              ├─▶ store (Bolt)             │
│                              ├─▶ mqtt.Publisher (paho)    │
│                              └─▶ slog                     │
│                                                            │
│  http server: /status /pause /resume /resync /reload      │
│              /healthz /metrics                             │
└────────────────────────────────────────────────────────────┘
```

---

## 4. Configuration

### 4.1 `/etc/default/homedrive` (consumed by systemd)

```sh
# Variables consumed by systemd (homedrive@.service).
# Rich config lives in HOMEDRIVE_CONFIG (YAML).
HOMEDRIVE_CONFIG=/etc/homedrive/config.yaml
HOMEDRIVE_LOG_LEVEL=info
HOMEDRIVE_LOG=stderr
```

Optional per-user override file `/etc/default/homedrive.<user>` (loaded with
`-` prefix in `EnvironmentFile=` so it's optional).

### 4.2 `/etc/homedrive/config.yaml` (rich config)

```yaml
local_root: /mnt/external/gdrive
remote: gdrive:
rclone_config: /home/fix/.config/rclone/rclone.conf

watcher:
  debounce: 2s
  dir_rename_pair_window: 500ms
  exclude:
    - "**/.git/**"
    - "**/.svn/**"
    - "**/.hg/**"
    - "**/.DS_Store"
    - "**/Thumbs.db"
    - "**/desktop.ini"
    - "**/*.swp"
    - "**/*.swo"
    - "**/*~"
    - "**/.#*"
    - "**/~$*"
    - "**/.~lock.*"
    - "**/node_modules/**"
    - "**/.venv/**"
    - "**/__pycache__/**"
    - "**/.idea/**"
    - "**/.vscode/**"
    - "**/*.tmp"
    - "**/*.partial"
    - "**/*.crdownload"

push:
  workers: 2
  retry:
    max_attempts: 5
    initial_backoff: 5s
    max_backoff: 5m

pull:
  changes_api_interval: 30s
  bisync_interval: 1h

conflict:
  policy: newer_wins         # newer_wins | local_wins | remote_wins | manual
  old_suffix_format: ".old.%d"

state:
  path: /var/lib/homedrive/state.db
  audit_log: /var/log/homedrive/audit.jsonl

http:
  listen: 127.0.0.1:6090
  metrics: true

mqtt:
  enabled: true
  broker: tcp://192.168.1.2:1883
  client_id_prefix: homedrive
  base_topic: homedrive
  ha_discovery_prefix: homeassistant
  publish_interval: 30s
  qos: 1

logging:
  level: info               # error | warn | info | debug
  format: json              # text | json

quota:
  warn_pct: 90              # MQTT warning above this
  stop_push_pct: 99         # stop pushes, keep pulls

dry_run: false              # if true, no remote writes; CLI flag overrides
```

Hot reload via `SIGHUP` or `POST /reload`.

---

## 5. Watcher: design notes (no spike)

`fsnotify/fsnotify` is the chosen library. The patterns below are well-known
and don't require a benchmarking spike — they are documented here as binding
implementation notes.

### 5.1 Recursive watching

fsnotify on Linux is not natively recursive. Standard pattern:

1. At startup: `filepath.WalkDir(local_root)`, call `Watcher.Add` on every
   directory.
2. On `Create` event for a directory entry: `Watcher.Add` on the new
   directory before any of its children can fire events.
3. On `Remove` event for a watched directory: kernel removes the watch
   automatically; no action needed.
4. Honor exclusion globs at watch-add time AND at event emission time
   (defense in depth — globs may match files inside watched directories).

### 5.2 Debouncing

Per-path timer (`time.AfterFunc`) reset on each event. Default window 2s.
A path with many writes in burst → single event emitted 2s after the last
write. Editor saves (vim swap, LibreOffice lock) handled by exclusion globs
plus the debouncer.

### 5.3 Inode/mtime guard

Before emitting an event downstream, the watcher consults the store. If
`mtime` and `size` match the last recorded sync within 1s tolerance, the
event is dropped — it is a self-induced echo from a recent pull.

---

## 6. Directory rename: efficient handling

This is the most performance-critical edge case. A naive implementation
turns `mv dir_50k other_dir` into 50k+ DELETE/CREATE events and 50k Drive
API calls. The design below collapses this to **one event, one Drive call,
one Bolt transaction**.

### 6.1 Detection

When a directory is renamed within a watched tree, inotify emits paired
events with a shared **cookie**:

- `IN_MOVED_FROM` on the source parent (cookie=N, name=`dir_50k`)
- `IN_MOVED_TO` on the destination parent (cookie=N, name=`dir_50k`)

fsnotify exposes these as `Rename` then `Create` events on the parents,
preserving the cookie in `event.Op`. The watcher's **rename pairer**
buffers `Rename` events for `dir_rename_pair_window` (default 500ms) and
matches them with subsequent `Create` events by cookie.

### 6.2 Action when paired

A successful pair within the window emits a single `DirRename{from, to}`
event to the syncer:

- The rename pairer **removes the old subtree watches** and **adds new ones**
  (paths changed; existing watches are still valid by inode but produce
  events with wrong path strings).
- All subsequent `Rename`/`Create` events from the children of `dir_50k`
  arriving in the window are **suppressed** — they are redundant.
- A `cookie_seen` set deduplicates against late-arriving paired events.

### 6.3 Action when unpaired

If `Rename` is not matched within the window (cross-mount move, source moved
out of the watched tree, dest moved in from outside): fall back to standard
delete-or-create handling per file. This is the slow path but correct.

### 6.4 Syncer behavior on `DirRename`

```
1. Drive: operations.MoveFile(remoteFromID, newParentID, newName)
   — Drive treats this as a parent-ID metadata change, O(1) regardless of
   subtree size, single API call.
2. Store: single Bolt transaction rewriting all keys with prefix `from/`
   to prefix `to/`. Implementation: open a cursor on the prefix, batch
   put/delete pairs in one TX.
3. Audit log: one JSONL line `{op: dir_rename, from, to, files_count}`.
4. MQTT: one event `dir_rename`, no per-file events.
```

### 6.5 Edge cases documented

- **Rename across different mount points**: inotify cannot pair (different
  inode space). Falls back to delete+create handling. Documented in
  `docs/directory-rename.md`.
- **Rename of unwatched dir into watched tree**: only `Create` arrives, no
  paired `Rename`. Treated as recursive create (full Walk + push).
- **Rapid double-rename within window**: pairer matches by cookie order;
  the second pair is processed independently after the first's `Create`
  is consumed.
- **Conflict during rename**: if the destination already exists remotely,
  apply standard conflict resolution per §9 on the directory itself
  (rare; documented as known limitation in v0.1).

### 6.6 Test scenarios (mandatory)

- `mv dir_with_50k_files dest` → exactly 1 Drive call, < 100ms watcher latency.
- `mv` then `mv` back within 1s → 2 paired events handled correctly.
- `mv` across mount points → falls back gracefully, no event lost.
- `mv` of a directory containing 1 excluded file → still 1 paired event,
  the excluded file is not synced anyway.

---

## 7. Pull strategy: hybrid Changes API + bisync

### 7.1 Drive Changes API (primary path, 30s latency)

- On startup: `changes.getStartPageToken` if no token persisted in Bolt.
- Loop: every 30s, call `changes.list(pageToken=...)`, persist new
  `nextPageToken` after successful processing of all changes.
- Each `Change` is dispatched to the syncer like a local event would be,
  but flagged as `origin=remote` to avoid loop-back.
- 410 GONE on token → reset via `getStartPageToken` + emit MQTT warning.

### 7.2 Bisync (safety net, 1h)

- Hourly full directory diff to catch any drift caused by bugs, crashes,
  or missed events.
- Acquires a global write lock (`sync.RWMutex`) blocking push/pull workers
  during execution.
- Records duration, files changed, conflicts found in audit log.

### 7.3 Loop prevention

When a remote change downloads file X, the watcher will fire `WRITE` for X.
Without care, this re-uploads X.

Solution: every successful sync writes to `store/`:
`{path, local_mtime, remote_mtime, remote_md5, remote_id, last_synced_at, last_origin}`.

The syncer ignores any incoming watcher event whose `mtime` matches the
last-recorded `local_mtime` for that path (within 1s tolerance).

---

## 8. MQTT wrapper

### 8.1 Goals

- Lightweight publish-only initially (HA Discovery + state + events).
- Designed so future enrichment (cross-device sync, peer discovery, lock
  coordination) is a non-breaking interface extension.
- Hide `paho.mqtt.golang` behind a stable internal API so swapping libraries
  later is local to one package.

### 8.2 Initial API (v0.1)

```go
// internal/mqtt/mqtt.go
package mqtt

type Config struct {
    Broker            string        // tcp://host:1883
    ClientIDPrefix    string        // homedrive
    BaseTopic         string        // homedrive
    HADiscoveryPrefix string        // homeassistant
    QoS               byte          // 0|1|2
    KeepAlive         time.Duration
    ReconnectMax      time.Duration
    Username, Password string       // optional
}

type Publisher interface {
    Publish(topic string, qos byte, retain bool, payload any) error
    PublishJSON(topic string, payload any) error
    Topic(parts ...string) string  // builds <base>/<host>/<user>/<parts...>
    Close(ctx context.Context) error
}

type Client struct { /* unexported */ }

func New(cfg Config, host, user string, log *slog.Logger) (*Client, error)
```

Key behaviors:

- LWT set on `Connect()`: `<base>/<host>/<user>/online` payload `offline`,
  retain=true.
- After successful connect: publish `online` (retain=true).
- Auto-reconnect with exponential backoff via paho's `OnConnectionLost`.
- `PublishJSON` marshals with `encoding/json`, sets QoS from config.
- `Topic` is a small helper that builds path-style topics consistently.
- All publishes are non-blocking; errors logged + counted in metrics.

### 8.3 Designed-in extension points (NOT implemented in v0.1)

The package is shaped so future features add interfaces without modifying
existing call sites:

```go
// Future: cross-device peer discovery & sync
type Subscriber interface {
    Subscribe(topic string, qos byte, handler MessageHandler) error
    Unsubscribe(topic string) error
}

type PeerCoordinator interface {
    AnnouncePresence(ctx context.Context) error          // retained presence on peers/<host>
    Peers(ctx context.Context) ([]Peer, error)
    AcquireLock(ctx context.Context, key string) (Lock, error)
    ProposeConflictResolution(ctx context.Context, ...) (Decision, error)
}

// Client will implement these interfaces when the features land.
// Existing Publisher consumers are unaffected.
```

Reserved future topic namespace (documented now to prevent accidental
collisions):

| Topic | Purpose | Phase |
|---|---|---|
| `homedrive/peers/<host>` | retained presence beacon | future |
| `homedrive/locks/<key>` | distributed mutex | future |
| `homedrive/sync/proposals/<id>` | conflict resolution voting | future |
| `homedrive/sync/decisions/<id>` | resolution outcome | future |

### 8.4 Why not reuse `mymqtt`

`mymqtt` from the repo provides bidirectional MQTT plumbing (lazy connect,
subscriptions, RPC) tailored to Shelly device interactions. `homedrive`
v0.1 is publish-only and benefits from a thinner dependency surface.
A future migration to `mymqtt` is possible if cross-device sync needs its
RPC features — `homedrive`'s `Publisher` interface stays stable.

---

## 9. MQTT structure for Home Assistant

### 9.1 State entities (HA Discovery)

Base topic: `homedrive/<hostname>/<user>/`. Auto-discovery via
`homeassistant/<component>/homedrive_<host>_<user>_<entity>/config`,
published `retain=true` at startup and on `/reload`.

| Entity | HA component | State topic | Notes |
|---|---|---|---|
| Status | sensor | `.../status` | `running`, `paused`, `error`, `quota_blocked` |
| Last push (timestamp) | sensor | `.../last_push` | ISO8601, `device_class: timestamp` |
| Last pull (timestamp) | sensor | `.../last_pull` | ISO8601 |
| Pending uploads | sensor | `.../queue/pending_up` | int |
| Pending downloads | sensor | `.../queue/pending_down` | int |
| Conflicts (24h) | sensor | `.../conflicts_24h` | int, rolling window |
| Bytes uploaded (24h) | sensor | `.../bytes_up_24h` | bytes |
| Bytes downloaded (24h) | sensor | `.../bytes_down_24h` | bytes |
| Drive quota used | sensor | `.../quota_used_pct` | %, `device_class: data_size` |
| Online | binary_sensor | `.../online` | LWT, `device_class: connectivity` |

All entities share a `device` block linking them in HA UI:
```json
"device": {
  "identifiers": ["homedrive_<host>_<user>"],
  "name": "homedrive (<user>@<host>)",
  "sw_version": "<semver>",
  "model": "homedrive",
  "manufacturer": "asnowfix/home-automation"
}
```

### 9.2 Events (for HA automations)

Topic: `homedrive/<host>/<user>/events/<type>` (QoS 1, retain false).

Types: `push.success`, `push.failure`, `pull.success`, `pull.failure`,
`conflict.detected`, `conflict.resolved`, `dir_rename`, `quota.warning`,
`quota.exhausted`, `oauth.refresh_failed`.

Sample payload:
```json
{
  "ts": "2026-04-28T14:32:11Z",
  "type": "conflict.detected",
  "path": "Documents/notes.md",
  "local_mtime": "2026-04-28T14:32:00Z",
  "remote_mtime": "2026-04-28T14:31:45Z",
  "resolution": "newer_wins:local",
  "kept_old_as": "Documents/notes.md.old.3"
}
```

### 9.3 LWT

`homedrive/<host>/<user>/online` with `retain=true`, payload `online`/`offline`.
Set on `Connect()` with the LWT message `offline`. Publish `online` after
successful connect.

---

## 10. Minimal rclone import

Goal: avoid the ~50 rclone backends (binary 5 → 40 MB).

```go
import (
    _ "github.com/rclone/rclone/backend/drive"   // single backend registered
    "github.com/rclone/rclone/fs"
    "github.com/rclone/rclone/fs/config/configfile"
    "github.com/rclone/rclone/fs/operations"
    "github.com/rclone/rclone/fs/sync"
)
```

Verify after build:
- `go tool nm homedrive | grep -c rclone/backend/` → must list only `drive`.
- `du -h homedrive` → target < 25 MB stripped.
- Use `-trimpath -ldflags="-s -w"` in Makefile and CI.

API to wrap inside `internal/rcloneclient/`:

| Wrapper method | rclone call |
|---|---|
| `CopyFile(ctx, srcLocal, dstRemoteDir)` | `operations.CopyFile` |
| `DeleteFile(ctx, remotePath)` | `operations.DeleteFile` |
| `MoveFile(ctx, srcRemote, dstRemote)` | `operations.MoveFile` |
| `Stat(ctx, remotePath)` | `fs.NewObject` |
| `ListChanges(ctx, pageToken)` | direct Drive API via cast to `*drive.Fs` |
| `Quota(ctx)` | `fs.About` |

The client loads `rclone.conf` at startup, exposes OAuth health (refresh
token expiry → MQTT alert + `/healthz` 503).

When `dry_run=true`, the wrapper logs intended writes and returns success
without touching the remote. Dry-run mode also disables store writes so
subsequent runs replay the same plan unchanged.

---

## 11. Conflict resolution

### 11.1 Detection

A conflict exists when, on push or pull, the remote `mtime` or `md5` differs
from what the local journal expected.

Implementation: BoltDB journal indexed by path, storing for each synced
file `{local_mtime, remote_mtime, remote_md5, remote_id, last_synced_at, last_origin}`.

### 11.2 `newer_wins` algorithm

```
1. mtime(local) > mtime(remote):
     → upload local; the remote version is renamed remotely as "<path>.old.<N>"
2. mtime(remote) > mtime(local):
     → download remote; the local version is renamed locally as "<path>.old.<N>"
3. mtime equal but checksums differ:
     → log warning; default winner is local (configurable);
        the loser is preserved as ".old.<N>" on its own side
```

`<N>` is computed by listing existing `.old.*` siblings (in the journal,
not by listing the FS) and incrementing the max. The `.old.<N>` is created
**on the same side as the loser**, never both.

Every conflict emits MQTT `conflict.detected` then `conflict.resolved`.

### 11.3 Audit log

Each operation (push, pull, conflict, delete, dir_rename, dry-run) appends a
JSONL line to `/var/log/homedrive/audit.jsonl`. Rotation handled by logrotate
(see §15.3).

### 11.4 Manual mode (future)

If `policy: manual`, conflicts are not auto-resolved; the file is locked in
the journal as `conflict_pending` and exposed via `/status` and a dedicated
MQTT event. CLI command `homedrive ctl conflict resolve <path>
[--keep-local|--keep-remote]` to be added in a later phase.

---

## 12. HTTP control endpoint

Listens on `127.0.0.1:6090` by default (loopback, no auth required). If
exposed to the network, a `Bearer` token must be set in config.

| Route | Method | Description |
|---|---|---|
| `/status` | GET | JSON: state, queues, last syncs, quota, version |
| `/pause` | POST | pause watcher + workers |
| `/resume` | POST | resume |
| `/resync` | POST | force immediate bisync |
| `/reload` | POST | reload config (equivalent to `SIGHUP`) |
| `/healthz` | GET | 200/503 based on OAuth + MQTT + disk |
| `/metrics` | GET | Prometheus exposition (reuse `myhome` stack) |

CLI sub-commands (`homedrive ctl status`, `homedrive ctl pause`, etc.) call
this endpoint over HTTP.

Port 6090 is free per the documented port allocation in the repo README.

---

## 13. Logging

- `slog` standard library, JSON output by default.
- Reuse `hlog` from the repo for per-package named loggers if compatible
  with `slog`; otherwise call `slog` directly with a package attribute.
- Levels: `error`, `warn`, `info`, `debug`. Auto-`debug` when launched from
  VSCode (env `HOMEDRIVE_LOG=stderr` set in launch.json).
- Mandatory structured fields: `path`, `op`, `bytes`, `duration_ms`,
  `attempt`, `remote_id`, `origin` (`local|remote`).
- **Never** log file content.

---

## 14. Execution phases

Each phase = one PR, reviewed before the next. Issues created via
`gh issue create` with appropriate labels (`enhancement`, `core-architecture`,
`integrations`, `monitoring`).

### Phase 0 — Bootstrap (0.5d) [DONE]
- [x] Create `homedrive/` with the layout above.
- [x] Update `go.work`.
- [x] `go.mod` with `fsnotify`, `cobra`, YAML loader (match `myhome`'s loader),
  `paho.mqtt.golang`, `bbolt`, `doublestar`.
- [x] Skeleton `cmd/homedrive/main.go` with subcommands `run`, `ctl status`,
  `ctl pause`, `ctl resume`, `ctl resync`.
- [x] Global `--dry-run` flag wired through context and respected by the rclone
  wrapper.
- [x] Makefile target `make build-homedrive`, plus `test-linux` (OrbStack).
- [x] Initial README + this `PLAN.md`.
- Issue: `[homedrive] Bootstrap module`.

### Phase 1 — Watcher with rename pairer (1.5d) [DONE]
- [x] `internal/watcher/`: initial Walk, dynamic AddWatch on directory `Create`.
- [x] Per-path debouncer (configurable window).
- [x] **Directory rename pairer** (cookie-based, configurable window).
- [x] `doublestar` exclusion filters from config.
- [x] Inode/mtime guard — consult store before emitting (1s tolerance).
- [x] Outbound channel: `Event{Path, Op, At}` and `DirRename{From, To, At}`.
- [x] Unit tests:
  - [x] create, write, write x10 fast → debounced to 1 event.
  - [x] mv dir small (10 files) → 1 paired event, child events suppressed.
  - [x] mv dir large (5k files in tests) → 1 paired event, < 100ms latency.
  - [x] mv across mount points → fallback to delete+create.
  - [x] rapid double rename within window → both pairs handled.
  - [x] rename of dir containing only excluded files → 1 paired event.
- Issue: `[homedrive] Watcher fsnotify + rename pairer`.

### Phase 2 — Minimal rclone wrapper (1d)
- `internal/rcloneclient/`: selective backend import.
- Methods: `CopyFile`, `DeleteFile`, `MoveFile`, `Stat`, `ListChanges`, `Quota`.
- Honor `--dry-run`: log + return success without remote writes.
- Binary size assertion in CI (< 25 MB).
- Mock-friendly interface (`RemoteFS`).
- Issue: `[homedrive] Wrapper rclone (minimal import)`.

### Phase 3 — Store + conflict resolution (1d)
- `internal/store/` with BoltDB, documented schema.
- `newer_wins` algorithm with `.old.<N>`.
- **Bulk path-prefix rewrite** for directory renames (single TX).
- JSONL audit appender.
- Unit tests covering all 3 cases of §11.2 + edge cases.
- Issue: `[homedrive] Store + conflict resolution`.

### Phase 4 — MQTT wrapper (0.5d)
- `internal/mqtt/`: paho wrapper per §8.
- LWT, auto-reconnect, JSON publish, topic builder.
- Unit tests with embedded broker (`mochi-mqtt/server`).
- Future-extension interfaces documented (commented out).
- Issue: `[homedrive] MQTT wrapper`.

### Phase 5 — Push syncer (1d)
- Worker pool consuming the watcher queue.
- Handles `Event` and `DirRename` events distinctly.
- Exponential backoff retry.
- Push/bisync coordination via `sync.RWMutex`.
- End-to-end tests with mocked `RemoteFS`, including `mv dir` scenario
  asserting exactly 1 `MoveFile` call.
- Issue: `[homedrive] Push worker pool`.

### Phase 6 — Pull via Drive Changes API (1d)
- Polling `changes.list` with `pageToken` persisted in store.
- Download + conflict resolution.
- Tests with mock supplying simulated `Change` events.
- 410 GONE handling (token reset).
- Issue: `[homedrive] Pull via Drive Changes API`.

### Phase 7 — Bisync safety net (0.5d)
- Configurable ticker (default 1h).
- Global lock blocking push/pull during execution.
- Issue: `[homedrive] Periodic bisync safety net`.

### Phase 8 — HTTP control + metrics (0.5d)
- Standard `net/http` server.
- Routes `/status /pause /resume /resync /reload /healthz /metrics`.
- Prometheus exporter aligned with `myhome` pattern.
- `httptest`-based unit tests.
- CLI sub-commands wired to call the endpoint.
- Issue: `[homedrive] HTTP control endpoint`.

### Phase 9 — HA Discovery + state publisher (0.5d)
- HA discovery configs published with retain at startup + on `/reload`.
- Periodic state publisher (uses `internal/mqtt/`).
- Event publisher (incl. `dir_rename`).
- `docs/home-assistant.md` with example automations.
- Issue: `[homedrive] HA Discovery + state/event publishing`.

### Phase 10 — Quota handling (0.5d)
- Periodic `Quota()` poll (every 5min).
- At `warn_pct`: MQTT warning event.
- At `stop_push_pct`: pause push workers, keep pull running, MQTT
  `quota.exhausted` event, status reflects `quota_blocked`.
- Resume pushes when quota drops below `stop_push_pct - 5%` (hysteresis).
- Issue: `[homedrive] Quota-aware push throttling`.

### Phase 11 — Packaging systemd + sysctl + logrotate (0.5d)
- `linux/homedrive@.service` templated.
- `linux/homedrive.default` sample.
- `linux/homedrive.logrotate` (weekly, keep 12).
- `linux/99-homedrive-inotify.conf` (sysctl).
- `linux/postinst.sh` applying sysctl + creating dirs.
- Make targets `install-systemd` and `install-package`.
- Manual test on Pi.
- Issue: `[homedrive] systemd packaging + sysctl + logrotate`.

### Phase 12 — CI / GitHub Actions (0.5d)
- Dedicated workflow `.github/workflows/homedrive.yml`.
- Build matrix `linux/amd64` + `linux/arm64` (QEMU).
- `go test`, `go vet`, `staticcheck`, `golangci-lint`.
- Coverage gate > 70%.
- Binary size check in CI.
- Update `.goreleaser.yml`.
- Update `dependabot.yml` with grouping for rclone updates.
- Issue: `[homedrive] CI GitHub Actions`.

### Phase 13 — Docs + 0.1.0 release (0.5d)
- Final README, `docs/architecture.md`, `docs/conflict-resolution.md`,
  `docs/directory-rename.md`, `docs/dev-environment.md`,
  `docs/manual-validation.md`.
- Update root `RELEASE_NOTES.md`.
- Tag `homedrive/v0.1.0`.

**Estimated total: ~10 person-days, atomic 0.5–1.5 day PRs.**

---

## 15. Packaging

### 15.1 `linux/homedrive@.service`

```ini
[Unit]
Description=homedrive sync agent for %i
Documentation=https://github.com/asnowfix/home-automation/blob/main/homedrive/README.md
After=network-online.target
Wants=network-online.target

[Service]
Type=notify
User=%i
Group=%i
EnvironmentFile=/etc/default/homedrive
EnvironmentFile=-/etc/default/homedrive.%i
ExecStart=/usr/bin/homedrive run --config ${HOMEDRIVE_CONFIG}
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure
RestartSec=10
WatchdogSec=60

# hardening
ProtectSystem=strict
ProtectHome=read-only
ReadWritePaths=/var/lib/homedrive /var/log/homedrive
ReadOnlyPaths=/etc/homedrive
PrivateTmp=true
NoNewPrivileges=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true

LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
```

### 15.2 `linux/99-homedrive-inotify.conf`

```
fs.inotify.max_user_watches=524288
fs.inotify.max_user_instances=512
```

### 15.3 `linux/homedrive.logrotate`

```
/var/log/homedrive/audit.jsonl {
    weekly
    rotate 12
    compress
    delaycompress
    missingok
    notifempty
    create 0640 homedrive homedrive
    sharedscripts
    postrotate
        systemctl reload 'homedrive@*.service' > /dev/null 2>&1 || true
    endscript
}
```

### 15.4 `linux/postinst.sh`

```sh
#!/bin/sh
set -e
install -d -m 0755 -o root -g root /etc/homedrive
install -d -m 0750 -o root -g root /var/lib/homedrive
install -d -m 0750 -o root -g root /var/log/homedrive
install -m 0644 99-homedrive-inotify.conf /etc/sysctl.d/
install -m 0644 homedrive.logrotate /etc/logrotate.d/homedrive
sysctl --system
```

---

## 16. Testing strategy

### 16.1 Pyramid

```
        ┌──────────────┐
        │  e2e (1-2)   │  manual on test Pi (checklist documented)
        ├──────────────┤
        │  integration │  syncer + watcher + mock RemoteFS
        ├──────────────┤
        │   unit       │  per package, table-driven
        └──────────────┘
```

### 16.2 Drive backend mock

Define an interface in `internal/rcloneclient/`:

```go
type RemoteFS interface {
    CopyFile(ctx context.Context, src, dstDir string) (RemoteObject, error)
    DeleteFile(ctx context.Context, path string) error
    MoveFile(ctx context.Context, src, dst string) error
    Stat(ctx context.Context, path string) (RemoteObject, error)
    ListChanges(ctx context.Context, pageToken string) (Changes, error)
    Quota(ctx context.Context) (Quota, error)
}
```

Implementations:
- `RcloneFS` — production, wraps `rclone/operations`.
- `MemFS` — in-memory thread-safe with injectable clock.
- `FlakyFS` — decorator injecting errors / latency / timeouts.

### 16.3 Priority test scenarios

- **Watcher**: 50k files created in burst → no missed events; debouncer
  coalesces to 1 event per path.
- **Directory rename**: `mv dir_5k other_dir` → exactly 1 `DirRename` event,
  exactly 1 `MoveFile` Drive call, exactly 1 Bolt TX. Latency < 100ms.
- **Syncer**: 1000 files modified concurrently → all synced, retries OK.
- **Conflicts**: every case in §11.2.
- **Pull Changes API**: pageToken persisted across daemon restarts.
- **Bisync coordination**: push during bisync → blocked, resumed after.
- **Offline/online**: `FlakyFS` simulating 5min network outage → clean
  resumption, queue drained, audit log consistent.
- **OAuth refresh**: expired token → metric published, retry after refresh.
- **Crash recovery**: `kill -9` mid-upload → restart consistent.
- **Quota full**: simulate quota exhaustion → push pauses, pull continues,
  MQTT alert emitted, recovery on hysteresis.
- **Dry-run**: every operation logged as "would do X", store unchanged,
  remote unchanged.
- **MQTT wrapper**: LWT delivered when broker is killed mid-session
  (verified with embedded `mochi-mqtt/server`).

### 16.4 No tests against real Drive API

Confirmed. Integration tests use `MemFS` / `FlakyFS`. Manual e2e validation
on the production Pi is documented in `docs/manual-validation.md`.

---

## 17. CI / GitHub Actions

### 17.1 `.github/workflows/homedrive.yml`

```yaml
name: homedrive

on:
  push:
    paths: [ "homedrive/**", "go.work", "go.work.sum" ]
  pull_request:
    paths: [ "homedrive/**" ]

jobs:
  test:
    runs-on: ubuntu-latest
    strategy:
      matrix:
        goarch: [amd64, arm64]
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: 'stable' }
      - uses: docker/setup-qemu-action@v3
        if: matrix.goarch == 'arm64'
      - name: Build
        env: { GOARCH: "${{ matrix.goarch }}" }
        run: go build -trimpath -ldflags="-s -w" -o homedrive ./homedrive/cmd/homedrive
      - name: Verify binary size
        run: test $(stat -c%s homedrive) -lt 26214400
      - name: Verify rclone backends
        run: |
          backends=$(go tool nm homedrive | grep -c 'rclone/backend/' || true)
          test "$backends" -eq 1 || (echo "expected exactly 1 rclone backend, got $backends" && exit 1)
      - name: Test
        if: matrix.goarch == 'amd64'
        run: go test -race -coverprofile=coverage.out ./homedrive/...
      - name: Lint
        if: matrix.goarch == 'amd64'
        uses: golangci/golangci-lint-action@v6
        with: { working-directory: homedrive }
      - name: Coverage gate
        if: matrix.goarch == 'amd64'
        run: |
          pct=$(go tool cover -func=coverage.out | tail -1 | awk '{print $3}' | tr -d %)
          awk -v p=$pct 'BEGIN { exit (p < 70) }'
```

### 17.2 Dependabot

Add a group in `.github/dependabot.yml` for `gomod` to bundle rclone-related
updates so they can be reviewed monthly as a single PR.

### 17.3 goreleaser

```yaml
builds:
  - id: homedrive
    main: ./homedrive/cmd/homedrive
    binary: homedrive
    goos: [linux]
    goarch: [amd64, arm64]
    flags: [-trimpath]
    ldflags: [-s, -w, -X main.version={{.Version}}]
```

Add nfpm packaging directives so `postinst.sh`, sysctl, and logrotate files
land in the right place automatically.

---

## 18. Claude Code skills to create in `.claude/skills/`

Each skill = a subdirectory with `SKILL.md` (frontmatter + procedure).

### 18.1 `homedrive-conventions`
**Trigger**: "homedrive module", "add feature to homedrive", "write code in homedrive".
**Content**:
- Mandatory layout (`cmd/`, `internal/`, `pkg/`, `linux/`, `docs/`).
- Logging conventions (structured `slog`, never `fmt.Println`).
- Error conventions (`%w` wrap, exported sentinel errors).
- No `panic` outside `main`.
- Table-driven tests, naming `TestXxx_Case`.
- Files < 500 lines, functions < 80 lines.
- rclone imports: only what is listed in §10.

### 18.2 `homedrive-rclone-import`
**Trigger**: "import rclone", "add rclone dependency", "call rclone from Go".
**Content**:
- Allow-list of rclone packages.
- Post-build binary size verification procedure.
- Mandatory wrapper pattern (never call rclone directly from the syncer).
- How to load `rclone.conf`.
- How to obtain a typed `*drive.Fs` for `Changes API` access.
- `--dry-run` honored at the wrapper layer.

### 18.3 `homedrive-mqtt-wrapper`
**Trigger**: "homedrive MQTT", "publish from homedrive", "extend MQTT in homedrive".
**Content**:
- Use only `internal/mqtt/` package (never call paho directly elsewhere).
- `Publisher` interface contract (Publish, PublishJSON, Topic, Close).
- LWT setup pattern (set on connect, publish online after).
- Reserved future namespaces — do not use `peers/`, `locks/`, `sync/` in v0.1.
- Tests must use embedded `mochi-mqtt/server`, never a real broker.

### 18.4 `homedrive-watcher-rename`
**Trigger**: "homedrive watcher", "directory rename", "fsnotify rename pairing".
**Content**:
- Exact pairer algorithm from §6.
- Cookie-based pairing, default 500ms window.
- Suppression of child events post-pairing.
- Re-watching subtree on rename.
- Mandatory test scenarios from §6.6.
- Performance assertion: O(1) Drive call regardless of subtree size.

### 18.5 `homedrive-test-mocks`
**Trigger**: "test homedrive", "mock rclone", "test syncer".
**Content**:
- `RemoteFS` interface (never real rclone calls in tests).
- `MemFS` and `FlakyFS` patterns.
- How to inject a testable clock.
- `_test.go` format, table-driven.
- No tests depending on the real Google API.

### 18.6 `homedrive-systemd`
**Trigger**: "homedrive systemd", "homedrive service", "/etc/default/homedrive".
**Content**:
- Full `homedrive@.service` template.
- Mandatory hardening directives.
- Per-user override pattern.
- sysctl applied at install (`postinst.sh`), not via the unit file.
- logrotate weekly with `keep 12`.

### 18.7 `homedrive-conflict-resolution`
**Trigger**: "homedrive conflict", "newer wins", ".old suffix".
**Content**:
- Exact algorithm from §11.2.
- `.old.<N>` format and `<N>` computation.
- Which MQTT events to emit.
- Which log level.
- Tests required for any modification.

### 18.8 `homedrive-issue` (meta)
**Trigger**: explicit user request ("create an issue for X").
**Content**: `gh issue create` command with default labels (`homedrive` + a
functional label per the README's project-specific label set).

> Also create `.claude/agents/homedrive-implementer.md` referencing these
> skills and providing a system prompt oriented toward "phase-by-phase
> implementation, atomic PRs, no skipping tests".

---

## 19. Development environment (macOS host → Linux target)

`homedrive` is Linux-specific (inotify, systemd, FUSE potentially) but
development happens on a macOS workstation. fsnotify on macOS uses **FSEvents**
(coalesces aggressively, different semantics) while on Linux it uses
**inotify** (per-event, supports rename cookies). Watcher tests **must** run
on Linux to validate real behavior.

### 19.1 Recommended stack

1. **Editor**: VS Code on macOS, as today.
2. **Linux runtime for tests**: OrbStack Ubuntu 24.04 VM (free for personal
   use, near-zero overhead on Apple Silicon).
3. **CI**: GitHub Actions for cross-platform validation (already in plan §17).
4. **Pi**: final manual e2e per `docs/manual-validation.md`.

### 19.2 OrbStack setup

```bash
brew install orbstack
orb create ubuntu-24.04 dev
# Repo is auto-mounted at the same path inside the VM.
orb run -m dev -- bash -c 'cd ~/code/home-automation && go test ./homedrive/...'
```

### 19.3 Cross-compilation from macOS

```bash
GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" \
  -o dist/homedrive-arm64 ./homedrive/cmd/homedrive

# Deploy to Pi for manual testing
scp dist/homedrive-arm64 fix@nas.local:/tmp/
ssh fix@nas.local 'sudo install /tmp/homedrive-arm64 /usr/local/bin/homedrive'
```

### 19.4 Makefile targets

```make
build-mac:
	go build -o dist/homedrive ./homedrive/cmd/homedrive

build-arm64:
	GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" \
	  -o dist/homedrive-arm64 ./homedrive/cmd/homedrive

build-amd64:
	GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" \
	  -o dist/homedrive-amd64 ./homedrive/cmd/homedrive

test-linux:
	orb run -m dev -- go test -race ./homedrive/...

test-pi:
	ssh fix@nas.local 'cd /tmp/homedrive-test && go test ./...'

deploy-pi: build-arm64
	scp dist/homedrive-arm64 fix@nas.local:/tmp/homedrive
	ssh fix@nas.local 'sudo install /tmp/homedrive /usr/local/bin/'
	ssh fix@nas.local 'sudo systemctl restart homedrive@fix.service'
```

### 19.5 Build tag protection for Linux-only code

For files that use Linux-specific syscalls (inotify constants, etc.):

```go
//go:build linux

package watcher
```

For tests that must skip on macOS:

```go
func TestRenamePairer(t *testing.T) {
    if runtime.GOOS != "linux" {
        t.Skip("rename pairer requires Linux inotify cookies")
    }
    // ...
}
```

### 19.6 VS Code configuration

`.vscode/settings.json` recommendation:

```json
{
  "go.testEnvVars": { "HOMEDRIVE_LOG": "stderr" },
  "go.testFlags": ["-v", "-race"],
  "go.toolsEnvVars": { "GOOS": "linux", "GOARCH": "arm64" },
  "gopls": { "build.env": { "GOOS": "linux" } }
}
```

This makes gopls type-check the Linux build (catches macOS-only code paths
at edit time).

Document all of the above in `docs/dev-environment.md`.

---

## 20. Risks & mitigations

| Risk | Mitigation |
|---|---|
| OAuth refresh token expired | MQTT metric + error log + healthz 503 |
| inotify saturation (`max_user_watches`) | Set at install via sysctl; log if `EMFILE` |
| Push/pull loop (re-upload after pull) | Track every op in store; ignore watcher event whose mtime matches last sync |
| Massive folder rename | Cookie-paired detection, single Drive call (§6) |
| Crash mid-upload | Bolt journal + rclone idempotency for resumption |
| Drive quota full | Stop pushes only, keep pulls running, MQTT alert |
| System clock drift | Use mtimes returned by each side; never use local clock as reference |
| Google native files (Docs/Sheets/Slides) | Skip + warn (out of scope v0.1) |
| Symlinks | Document behavior: follow files, skip directory symlinks |
| Binary size regression | CI gate < 25 MB and exactly 1 rclone backend |
| macOS dev → Linux runtime divergence | OrbStack VM for tests, GOOS=linux gopls, build tags |
| Future cross-device sync collision | Reserved MQTT namespaces documented in §8.3 |

---

## 21. Immediate next steps

1. Create the 8 `.claude/skills/homedrive-*` skills (§18).
2. Document `docs/dev-environment.md` and set up OrbStack locally.
3. Run Phase 0 (bootstrap module) — PR #1.
4. Run Phase 1 (watcher + rename pairer) — PR #2.
5. Iterate phase by phase, one PR at a time.

> Once approved, copy this file to `homedrive/PLAN.md` and keep it up to date
> at each phase (tick progress, adjust estimates).
