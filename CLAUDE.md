# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project status

**Pre-alpha.** No Go source code exists yet. This repo contains the execution plan (`PLAN.md`), skill definitions (`.claude/skills/`), and Linux packaging files (`linux/`). All Go code will be written phase-by-phase per `PLAN.md` §14.

Use the `homedrive-implementer` agent for phase-by-phase implementation work.

## What this project is

`homedrive` is a bidirectional Google Drive sync agent for headless ARM64 Linux (Raspberry Pi NAS), written in Go. Repo: `github.com/asnowfix/home-drive`. Conventions are borrowed from the parent [`home-automation`](https://github.com/asnowfix/home-automation) Go workspace (templated systemd, MQTT patterns, `cmd/`/`pkg/` layout).

Key design points:
- **Local disk is source of truth** (offline-first).
- Push: fsnotify watches → debouncer → job queue → rclone Drive backend.
- Pull: Drive Changes API every 30s + hourly bisync safety net.
- Conflict policy: **newer wins**, loser kept as `.old.<N>`.
- Single binary, minimal rclone import (only `backend/drive` — binary must be < 25 MB).
- MQTT publisher for Home Assistant integration (publish-only in v0.1).
- HTTP control endpoint on `127.0.0.1:6090`.

For architecture details (runtime topology, module layout, directory rename pairing, loop prevention, conflict algorithm), see `PLAN.md` §3–§11.

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

## Go workspace integration

After Phase 0, add `./homedrive` to `go.work` and a `homedrive` build entry to `.goreleaser.yml` targeting `linux/arm64` and `linux/amd64`. See `PLAN.md` §3.1 and §17.3.

## Key design decisions

**Directory rename pairing**: Cookie-paired inotify events collapse `mv dir_50k other_dir` to 1 Drive `MoveFile` call + 1 Bolt TX. See `PLAN.md` §6.

**Loop prevention**: Store records `{path, local_mtime, ...}` after every sync; watcher events matching the last mtime (±1s) are dropped. See `PLAN.md` §7.3.

**Conflict resolution** (`newer_wins`): `.old.<N>` suffix computed from the journal, not filesystem listing. Every conflict emits MQTT events. See `PLAN.md` §11.

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

Target is Linux ARM64; development is on macOS. fsnotify uses FSEvents on macOS (different semantics from inotify), so **watcher tests must run on Linux**. See `PLAN.md` §19 and `docs/dev-environment.md` for OrbStack setup, cross-compilation, build tags, and VS Code config.

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
