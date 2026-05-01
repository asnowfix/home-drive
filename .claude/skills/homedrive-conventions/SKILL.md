---
name: homedrive-conventions
description: Mandatory conventions for any code in the homedrive Go module — layout, logging, errors, tests, file/function size, and rclone import allow-list. Apply whenever writing or modifying Go code in homedrive/.
---

# homedrive code conventions

These are **binding** for any code in `homedrive/`. Deviation requires
explicit user approval and a note in PLAN.md.

## Layout

```
homedrive/
├── cmd/homedrive/main.go     # binary entrypoint, cobra subcommands
├── internal/                 # all implementation
│   ├── watcher/              # fsnotify + debouncer + rename pairer
│   ├── syncer/               # push/pull + conflict resolution
│   ├── rcloneclient/         # minimal rclone wrapper
│   ├── store/                # BoltDB journal + audit log
│   ├── config/               # YAML + /etc/default + flags loader
│   ├── http/                 # control endpoint
│   └── mqtt/                 # paho wrapper, HA discovery
├── pkg/homedrive/            # public types if needed (rare)
├── linux/                    # systemd, sysctl, logrotate, postinst
└── docs/                     # ADRs and reference docs
```

- Never put production code in `cmd/`. Put glue and wiring there only.
- Never put logic in `pkg/`. It is for stable public types only.

## Logging

- Use `log/slog` from the standard library, JSON output by default.
- Reuse `hlog` from the parent `home-automation` repo for per-package
  named loggers if compatible with `slog`.
- **Never** `fmt.Println`, `fmt.Printf`, or `log.Print*`. Period.
- Mandatory structured fields: `path`, `op`, `bytes`, `duration_ms`,
  `attempt`, `remote_id`, `origin` (`local|remote`).
- **Never** log file contents. Only metadata.

## Errors

- Wrap with `%w`: `fmt.Errorf("foo bar: %w", err)`.
- Exported sentinel errors get the `Err` prefix: `var ErrConflict = errors.New(...)`.
- Use `errors.Is` / `errors.As` at boundaries, never string matching.
- No `panic` outside `main` and outside `init` functions of test helpers.

## Tests

- Table-driven, naming `TestXxx_Case` (e.g. `TestNewerWins_LocalNewer`).
- One `_test.go` per source file.
- Tests requiring Linux-only behavior must skip on macOS:
  ```go
  if runtime.GOOS != "linux" {
      t.Skip("requires Linux inotify cookies")
  }
  ```
- Use the mock `RemoteFS` from `internal/rcloneclient/`. Never call rclone
  directly in tests.
- Use an injectable clock (e.g. `benbjohnson/clock`) for timing-sensitive
  tests.

## Size limits

- Files < 500 lines. If exceeded, split by responsibility before adding
  more.
- Functions < 80 lines. If exceeded, extract helpers.
- Cyclomatic complexity: aim for ≤ 10 per function; refactor at 15.

## rclone imports

Only the following imports are allowed:

```go
import (
    _ "github.com/rclone/rclone/backend/drive"  // single backend
    "github.com/rclone/rclone/fs"
    "github.com/rclone/rclone/fs/config/configfile"
    "github.com/rclone/rclone/fs/operations"
    "github.com/rclone/rclone/fs/sync"
)
```

Adding any other rclone package requires user approval AND verification
that the binary stays < 25 MB stripped. See `homedrive-rclone-import`.

## Build tags

Linux-specific code:
```go
//go:build linux

package watcher
```

## Style

- Use `gofmt` and `goimports` (enforced by CI).
- Use `golangci-lint` config from the parent repo if present, otherwise
  the defaults plus: `errcheck`, `staticcheck`, `gosec`, `revive`.

## CI validation (mandatory)

Before declaring any phase done, the GitHub Actions workflow must pass:
- `gh run watch --exit-status` on the PR's latest run.
- The "Verify rclone backends" check is skipped when rclone is not in
  `go.mod` (pre-Phase-2). Once rclone is added, exactly 1 backend is
  required.

## Concurrency

- Channels for events and signals; mutexes for shared state.
- `context.Context` first parameter on every blocking function.
- No goroutine leaks: every `go func() { ... }()` must have a documented
  shutdown path tied to a context or a `done` channel.
