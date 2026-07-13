# Release notes

## homedrive v0.1.1

Maintenance release. No functional changes to the sync engine, CLI, or MQTT
wire protocol since v0.1.0.

### Fixed

- MQTT test suite: eliminated an intermittent CI hang where raw paho test
  clients' `AutoReconnect` could race the embedded mochi-mqtt broker's
  shutdown, deadlocking on its `Clients` RWMutex (#38).

### Documentation

- Refreshed the root `README.md` to reflect v0.1.0 released status.
- Removed stale, duplicate root-level `PLAN.md` and `docs/architecture.md` /
  `docs/dev-environment.md` scaffolding, superseded by `homedrive/PLAN.md`
  and `homedrive/docs/` (#39).

### Upgrade path

No action required. Drop-in replacement for v0.1.0; no config, schema, or
wire-protocol changes.

## homedrive v0.1.0

First tagged release of homedrive, a bidirectional Google Drive sync agent
for headless ARM64 Linux (Raspberry Pi NAS).

All 13 implementation phases from PLAN.md §14 are complete.

### What is included

**Core sync engine**

- Real-time push via fsnotify recursive watcher with 2 s per-path debounce.
- Periodic pull via the Drive Changes API (default every 30 s), with
  `pageToken` persisted across restarts in BoltDB.
- Hourly bisync safety net: full directory diff under a global `sync.RWMutex`
  that blocks push/pull workers during execution.
- Loop prevention: mtime/size guard in the BoltDB journal prevents
  re-uploading files that were just downloaded.

**Directory rename handling**

- inotify cookie-based pairing collapses `mv large_dir new_name` to a single
  Drive API metadata call and a single BoltDB prefix-rewrite transaction,
  regardless of subtree size. Typical 50k-file rename: O(1) Drive calls
  instead of O(50k).
- Both inotify (Linux) and kqueue (macOS, development only) event orderings
  are handled by the rename pairer.

**Conflict resolution**

- Default policy: `newer_wins` — the version with the more recent mtime wins;
  the loser is preserved as `<path>.old.<N>`.
- Alternative policies: `local_wins`, `remote_wins`.
- `<N>` is tracked in the BoltDB journal (not by filesystem listing) to
  prevent races on rapid successive conflicts.
- Every conflict emits MQTT `conflict.detected` and `conflict.resolved` events
  and appends a JSONL line to the audit log.

**Remote filesystem abstraction**

- `RemoteFS` interface implemented by `RcloneFS` (production), `MemFS`
  (tests), `FlakyFS` (error/latency injection), and `DryRunFS` (no-op
  logging when `--dry-run` is set).
- Only the `backend/drive` rclone package is imported; stripped binary
  stays under 25 MB.

**MQTT integration**

- Paho wrapper with LWT (`offline` on unexpected disconnect), auto-reconnect,
  and JSON publishing.
- Home Assistant MQTT Discovery configs published at startup and on
  `POST /reload` (retained).
- Periodic state sensors: status, last push/pull timestamps, queue depths,
  quota usage, 24h conflict count, 24h bytes transferred.
- Event stream: `push.success`, `push.failure`, `pull.success`,
  `pull.failure`, `conflict.detected`, `conflict.resolved`, `dir_rename`,
  `quota.warning`, `quota.exhausted`.

**HTTP control endpoint**

- Listens on `127.0.0.1:6090` (loopback only).
- Routes: `GET /status`, `POST /pause`, `POST /resume`, `POST /resync`,
  `POST /reload`, `GET /healthz`, `GET /metrics`.
- Prometheus metrics exposition on `/metrics`.
- CLI subcommands `homedrive ctl status|pause|resume|resync` call these
  endpoints.

**Quota monitoring**

- Polls `Quota()` every 5 minutes.
- MQTT warning event at `warn_pct` (default 90 %).
- Push workers paused at `stop_push_pct` (default 99 %); pull continues.
- Hysteresis: push resumes only after usage drops below 94 % to prevent
  flapping.

**Systemd packaging**

- Templated per-user unit `homedrive@.service` with systemd hardening
  directives (`ProtectSystem=strict`, `NoNewPrivileges`, `PrivateTmp`, etc.).
- `postinst.sh` applies sysctl (`fs.inotify.max_user_watches=524288`) and
  creates `/var/lib/homedrive` and `/var/log/homedrive`.
- `logrotate` configuration: weekly rotation, keep 12 copies.
- `.goreleaser.yml` produces `.deb` packages for `linux/amd64` and
  `linux/arm64` via nfpm.

**CI / GitHub Actions**

- Workflow: build, test (with race detector), lint (`golangci-lint`),
  coverage gate (> 70 %), binary size gate (< 25 MB).
- Build matrix: `linux/amd64` and `linux/arm64` (QEMU).
- Dependabot configured with grouped rclone updates.

### Binary invariants

| Invariant | Value |
|---|---|
| Stripped binary size | < 25 MB |
| rclone backends registered | 1 (`backend/drive`) |
| Go version | 1.22+ |
| Target platforms | `linux/arm64`, `linux/amd64` |

### Known limitations (v0.1)

- Multi-pair sync (more than one `local_root`/`remote` pair) is not
  supported. Run multiple daemon instances as separate systemd services.
- Google Docs, Sheets, and Slides native formats are skipped with a warning
  (binary export is out of scope).
- The `manual` conflict policy is documented but not implemented; conflicts
  are always auto-resolved.
- Cross-device peer sync (distributed lock, conflict voting) is designed for
  but not implemented. Reserved MQTT namespaces are documented in
  `internal/mqtt/mqtt.go`.
- macOS is not a supported target. The binary builds on macOS for
  development type-checking but FSEvents semantics differ from inotify.

### Upgrade path

v0.1.0 is the first release. No migration is needed.

### Documentation

- [homedrive/README.md](homedrive/README.md) — installation, configuration, CLI usage
- [homedrive/docs/architecture.md](homedrive/docs/architecture.md) — runtime topology and data flows
- [homedrive/docs/conflict-resolution.md](homedrive/docs/conflict-resolution.md) — conflict algorithm
- [homedrive/docs/directory-rename.md](homedrive/docs/directory-rename.md) — rename pairer details
- [homedrive/docs/dev-environment.md](homedrive/docs/dev-environment.md) — macOS dev setup with OrbStack
- [homedrive/docs/home-assistant.md](homedrive/docs/home-assistant.md) — HA entities and automations
- [homedrive/docs/manual-validation.md](homedrive/docs/manual-validation.md) — Pi end-to-end checklist
