# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project status

**v0.1.0 released.** All 14 phases in `homedrive/PLAN.md` §14 are complete and tagged as `homedrive/v0.1.0`. See `RELEASE_NOTES.md` for what shipped and `homedrive/README.md` for install/usage docs. `homedrive/PLAN.md` remains the architecture reference and phase history; future work (e.g. a v0.2 feature) should still follow the phase workflow below.

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

For architecture details (runtime topology, module layout, directory rename pairing, loop prevention, conflict algorithm), see `homedrive/PLAN.md` §3–§11.

## Build commands

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

# Exactly 1 rclone backend explicitly imported (crypt is a transitive
# dependency of drive itself, so a raw binary symbol count is not a
# reliable check -- verify at the source-import level instead)
grep -rn '"github.com/rclone/rclone/backend/' homedrive/ --include='*.go' \
  | grep -v '_test.go' | grep -v 'rclone/backend/drive'  # must print nothing
```

## Go workspace integration

`./homedrive` is registered in `go.work`, with a `homedrive` build entry in `.goreleaser.yml` targeting `linux/arm64` and `linux/amd64`. See `homedrive/PLAN.md` §3.1 and §17.3.

## Key design decisions

**Directory rename pairing**: Cookie-paired inotify events collapse `mv dir_50k other_dir` to 1 Drive `MoveFile` call + 1 Bolt TX. See `homedrive/PLAN.md` §6.

**Loop prevention**: Store records `{path, local_mtime, ...}` after every sync; watcher events matching the last mtime (±1s) are dropped. See `homedrive/PLAN.md` §7.3.

**Conflict resolution** (`newer_wins`): `.old.<N>` suffix computed from the journal, not filesystem listing. Every conflict emits MQTT events. See `homedrive/PLAN.md` §11.

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

This project uses **[OrbStack](https://orbstack.dev)** (not Docker Desktop) for the local Linux VM. OrbStack runs a fast, lightweight Ubuntu 24.04 VM (`dev` machine) with real inotify support. Always run watcher tests via `orb run -m dev -- ...` or `make test-linux`.

Build pipeline order — run in this order, never skip, commit only after all pass:
1. `make test` — macOS native: catches compile errors and all platform-agnostic tests including rename tests (rename pairer handles both inotify and kqueue event orderings)
2. `orb run -m dev -- go test -race ./homedrive/...` — Linux VM: real inotify, IN_MOVE_SELF, race detector
3. Commit and push — CI validates on amd64 and arm64

See `homedrive/PLAN.md` §19 and `homedrive/docs/dev-environment.md` for cross-compilation, build tags, and VS Code config.

## Skills and agent

Specialized skills in `.claude/skills/` cover: conventions, rclone imports, MQTT wrapper, watcher rename algorithm, test mocks, systemd packaging, conflict resolution, and issue creation. Read the relevant skill before implementing any feature in its area.

Use `homedrive-implementer` agent (`.claude/agents/homedrive-implementer.md`) for phase-by-phase roadmap work. Each phase = one atomic PR. Branch: `phase-N-<short-title>`.

## Phase workflow

1. Create a GitHub issue for the phase (`homedrive-issue` skill).
2. Branch `phase-N-<short-title>`.
3. Read the relevant skills.
4. Implement with tests; run `orb run -m dev -- go test -race ./homedrive/...`.
5. Verify binary size and rclone backend count.
6. Tick the phase in `homedrive/PLAN.md` §14.
7. Open PR, link issue, paste test output.

PRs that combine phases, skip required test scenarios from `homedrive/PLAN.md` §16.3, exceed 25 MB binary, add rclone backends beyond `drive`, use unstructured logging, or add MQTT subscriptions in v0.1 are rejected.

## Release workflow (tagging)

**Any tag matching `homedrive/v*` pushed to origin is a real release**, not a
dry run: it triggers `.github/workflows/homedrive-release.yml`, which runs
GoReleaser and publishes a GitHub Release with `linux/amd64`/`linux/arm64`
`.deb`/`.tar.gz` artifacts. Don't push one casually, and never as a side
effect of testing something else.

Two tag shapes, both live under the same `homedrive/v*` glob:

- **Formal release**: `homedrive/vX.Y.Z` — a milestone or phase-completion
  release. Add a `RELEASE_NOTES.md` entry (see the `v0.1.0`/`v0.1.1` entries
  for the format) in the same PR or a follow-up before tagging.
- **Interim fix release**: `homedrive/vX.Y.Z-issueNN`, where `NN` is the
  GitHub issue number the change closes. Used for smaller fixes that need a
  real deployable build (e.g. to test on the NAS) without a full version
  bump — most PRs do **not** get one of these; only tag when a build
  actually needs to ship. No `RELEASE_NOTES.md` entry expected for this
  shape. Check `git tag --sort=-creatordate | grep homedrive` for the
  current highest tag before picking the next one.

Sequence: merge the PR first (squash, matching this repo's convention —
`gh pr merge --squash`), **then** tag the resulting commit on `main` — not
the feature branch tip:

```bash
git fetch origin main --tags
git tag -a homedrive/vX.Y.Z-issueNN <merge-commit-sha> -m "<summary>"
git push origin homedrive/vX.Y.Z-issueNN
```

The release will show as a GitHub-side "draft" with an `untagged-<hash>`
URL — that's the shadow-tag pipeline's normal steady state (see the
workflow file's comments), not a broken release. Don't try to "fix" it.

If you're deploying the resulting build to the NAS to verify before tagging,
use `make deploy-pi` (or the same steps by hand): always cross-compile
locally and transfer the binary, never build on the NAS itself, so the
manual path mirrors what CI actually ships.
