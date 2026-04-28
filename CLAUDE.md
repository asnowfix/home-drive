# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project status

**Pre-alpha.** No Go source code exists yet. This repo contains the execution plan (`PLAN.md`), skill definitions (`.claude/skills/`), and Linux packaging files (`linux/`). All Go code will be written phase-by-phase per `PLAN.md` §14.

Use the `homedrive-implementer` agent for phase-by-phase implementation work.

## What this project is

`homedrive` is a bidirectional Google Drive sync agent for headless ARM64 Linux (Raspberry Pi NAS), written in Go. Key design points:
- **Local disk is source of truth** (offline-first).
- Push: fsnotify watches → debouncer → job queue → rclone Drive backend.
- Pull: Drive Changes API every 30s + hourly bisync safety net.
- Conflict policy: **newer wins**, loser kept as `.old.<N>`.
- Single binary, minimal rclone import (only `backend/drive` — binary must be < 25 MB).
- MQTT publisher for Home Assistant integration (publish-only in v0.1).
- HTTP control endpoint on `127.0.0.1:6090`.

## Build commands (after Phase 0 lands)

```bash
make build-mac        # local Mac build for type-checking
make build-arm64      # cross-compile for the Pi (GOOS=linux GOARCH=arm64)
make build-amd64      # cross-compile for x86_64 Linux
make test-linux       # run tests inside OrbStack Ubuntu 24.04 VM (real inotify)
make test-pi          # run tests via SSH on the production Pi
make deploy-pi        # build-arm64 + scp + systemctl restart on nas.local
```

Run a single package's tests on Linux:
```bash
orb run -m dev -- go test -race ./homedrive/internal/watcher/...
```

CI invariants to verify before any PR:
```bash
# Binary size < 25 MB
GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" \
  -o homedrive-bin ./homedrive/cmd/homedrive
du -h homedrive-bin

# Exactly 1 rclone backend
go tool nm homedrive-bin | grep -c rclone/backend  # must be 1
```

## Module layout (target, after Phase 0)

```
homedrive/
├── cmd/homedrive/main.go      # cobra: `homedrive run`, `homedrive ctl ...`
├── internal/
│   ├── watcher/               # fsnotify recursive + per-path debouncer + dir-rename pairer
│   ├── syncer/                # push/pull logic + conflict resolution
│   ├── rcloneclient/          # minimal rclone wrapper (RemoteFS interface + MemFS/FlakyFS mocks)
│   ├── store/                 # BoltDB journal + .old.<N> index + JSONL audit log
│   ├── config/                # YAML + /etc/default/homedrive + cobra flags
│   ├── http/                  # /status /pause /resume /resync /reload /healthz /metrics
│   └── mqtt/                  # paho wrapper, HA Discovery, future peer sync
├── pkg/homedrive/             # stable public types only (rare)
└── linux/                     # systemd unit, /etc/default sample, logrotate, sysctl, postinst.sh
```

- `cmd/` is wiring only — no business logic.
- `pkg/` is for stable public types only.

## Runtime topology

```
watcher (fsnotify) → debouncer (per-path, 2s) + dir-rename pairer (500ms cookie window)
                                    ↓
                               job queue
                                    ↓
pull ticker (30s, Changes API) → syncer workers → rclone (Drive only)
bisync ticker (1h, safety net) →                → store (Bolt)
                                                → mqtt.Publisher (paho)
                                                → slog (JSON)

HTTP server: 127.0.0.1:6090
```

## Key design decisions

**Directory rename pairing**: When `mv dir_50k other_dir` happens, inotify emits paired `MOVED_FROM`/`MOVED_TO` events with a shared cookie. The watcher's rename pairer matches them within a 500ms window and emits a single `DirRename{from, to}` event → exactly 1 Drive `MoveFile` call and 1 Bolt TX (not 50k API calls). See `PLAN.md` §6.

**Loop prevention**: After every successful sync, the store records `{path, local_mtime, remote_mtime, ...}`. The syncer ignores watcher events whose mtime matches the last-recorded local_mtime (±1s), preventing re-upload of just-downloaded files.

**Conflict resolution** (`newer_wins`): `mtime(local) > mtime(remote)` → upload local, rename remote to `.old.<N>`. Vice versa for remote newer. `<N>` is computed from the journal (not filesystem listing). Every conflict emits MQTT `conflict.detected` then `conflict.resolved`.

**rclone imports** — only these are allowed:
```go
_ "github.com/rclone/rclone/backend/drive"
"github.com/rclone/rclone/fs"
"github.com/rclone/rclone/fs/config/configfile"
"github.com/rclone/rclone/fs/operations"
"github.com/rclone/rclone/fs/sync"
```

## Code conventions (binding)

- Logging: `log/slog` JSON only. Never `fmt.Println` or `log.Print*`. Never log file contents.
- Errors: wrap with `%w`; exported sentinels use `Err` prefix; use `errors.Is`/`errors.As`.
- No `panic` outside `main` and test helper `init` functions.
- Files < 500 lines; functions < 80 lines.
- Tests: table-driven, named `TestXxx_Case`. One `_test.go` per source file.
- Tests that require real inotify cookies must skip on non-Linux: `t.Skip("requires Linux inotify cookies")`.
- Never call rclone directly in tests — use `MemFS` or `FlakyFS` mocks via the `RemoteFS` interface.
- MQTT tests use embedded `mochi-mqtt/server`; never a real broker.
- Coverage gate: > 70%.

## Development environment

Target is Linux ARM64; development is on macOS. fsnotify uses FSEvents on macOS (different semantics from inotify), so **watcher tests must run on Linux**.

Set up OrbStack:
```bash
brew install orbstack
orb create ubuntu-24.04 dev
# Repo auto-mounts inside the VM at the same path.
```

Recommended `.vscode/settings.json`:
```json
{
  "go.testEnvVars": { "HOMEDRIVE_LOG": "stderr" },
  "go.testFlags": ["-v", "-race"],
  "go.toolsEnvVars": { "GOOS": "linux", "GOARCH": "arm64" },
  "gopls": { "build.env": { "GOOS": "linux" } }
}
```

## Skills and agent

Specialized skills in `.claude/skills/` cover: conventions, rclone imports, MQTT wrapper, watcher rename algorithm, test mocks, systemd packaging, conflict resolution, and issue creation. Read the relevant skill before implementing any feature in its area.

Use `homedrive-implementer` agent (`.claude/agents/homedrive-implementer.md`) for phase-by-phase roadmap work. Each phase = one atomic PR. Branch: `phase-N-<short-title>`.

## Phase workflow

1. Create a GitHub issue for the phase (`homedrive-issue` skill).
2. Branch `phase-N-<short-title>`.
3. Read the relevant skills.
4. Implement with tests; run `orb run -m dev -- go test -race ./homedrive/...`.
5. Verify binary size and rclone backend count.
6. Tick the phase in `PLAN.md` §14.
7. Open PR, link issue, paste test output.

PRs that combine phases, skip required test scenarios from `PLAN.md` §16.3, exceed 25 MB binary, add rclone backends beyond `drive`, use unstructured logging, or add MQTT subscriptions in v0.1 are rejected.
