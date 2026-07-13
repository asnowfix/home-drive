# Development environment

homedrive targets Linux ARM64 (Raspberry Pi NAS) but development happens
on macOS. This document covers the setup for building, testing, and
debugging homedrive across platforms.

## Platform differences

| Feature | macOS (dev host) | Linux (target) |
|---|---|---|
| fsnotify backend | FSEvents (kqueue) | inotify |
| Rename cookies | Not available | Available (inotify) |
| Event coalescing | Aggressive (FSEvents) | Per-event (inotify) |
| systemd | Not available | Available |
| `max_user_watches` | N/A | Tunable via sysctl |

The key implication: **watcher tests that depend on inotify rename
cookies or per-event semantics must run on Linux.** macOS is suitable
for compilation, type-checking, and running non-watcher tests.

## Recommended stack

1. **Editor**: VS Code on macOS (or any editor with Go/gopls support)
2. **Build host**: macOS with Go 1.22+
3. **Linux test runtime**: OrbStack Ubuntu 24.04 VM (free for personal
   use, near-zero overhead on Apple Silicon)
4. **CI**: GitHub Actions (ubuntu-latest) for cross-platform validation
5. **Pi**: production Raspberry Pi for final manual end-to-end testing

## OrbStack setup

### Installation

```bash
brew install orbstack
```

### Create the development VM

```bash
orb create ubuntu-24.04 dev
```

The repository is automatically mounted at the same path inside the VM.
No manual syncing or volume configuration is needed.

### Run tests inside the VM

```bash
# Run all homedrive tests with race detection
orb run -m dev -- go test -race ./homedrive/...

# Run a specific package
orb run -m dev -- go test -race -v ./homedrive/internal/watcher/...

# Run tests with coverage
orb run -m dev -- go test -race -coverprofile=coverage.out ./homedrive/...
orb run -m dev -- go tool cover -func=coverage.out
```

### Install Go in the VM (first time)

If Go is not pre-installed in the OrbStack VM:

```bash
orb run -m dev -- bash -c '
  curl -fsSL https://go.dev/dl/go1.22.2.linux-arm64.tar.gz | sudo tar -C /usr/local -xzf -
  echo "export PATH=\$PATH:/usr/local/bin" >> ~/.bashrc
'
```

## Cross-compilation from macOS

### Build for ARM64 (Raspberry Pi)

```bash
GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" \
  -o dist/homedrive-arm64 ./homedrive/cmd/homedrive
```

### Build for AMD64 (x86_64 Linux / CI)

```bash
GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" \
  -o dist/homedrive-amd64 ./homedrive/cmd/homedrive
```

### Build for macOS (type-checking only)

```bash
go build -o dist/homedrive ./homedrive/cmd/homedrive
```

The macOS build is useful for fast type-checking and running
non-platform-specific tests locally, but the binary is not intended
for production use.

### Makefile targets

The project provides convenience targets:

```bash
make build-mac      # macOS local build
make build-arm64    # cross-compile for Pi
make build-amd64    # cross-compile for x86_64 Linux
make test-linux     # run tests inside OrbStack
make test-pi        # run tests on the production Pi via SSH
make deploy-pi      # build-arm64 + scp + systemctl restart
```

## Deploy to Pi for manual testing

```bash
# Build and deploy in one step
make deploy-pi

# Or manually:
GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" \
  -o dist/homedrive-arm64 ./homedrive/cmd/homedrive
scp dist/homedrive-arm64 fix@nas.local:/tmp/
ssh fix@nas.local 'sudo install /tmp/homedrive-arm64 /usr/local/bin/homedrive'
ssh fix@nas.local 'sudo systemctl restart homedrive@fix.service'
```

## Build tags for Linux-only code

Files that use Linux-specific syscalls (inotify constants, etc.) must
use a build tag:

```go
//go:build linux

package watcher
```

This ensures the macOS build does not fail on missing Linux-specific
symbols. The `go build` invocation with `GOOS=linux` includes these
files; a plain `go build` on macOS excludes them.

For shared logic that works on both platforms, no build tag is needed.

## Test conventions for platform differences

Tests that require real inotify cookies (rename pairing, event ordering)
must skip on non-Linux:

```go
func TestRenamePairer_CookieMatching(t *testing.T) {
    if runtime.GOOS != "linux" {
        t.Skip("rename pairer requires Linux inotify cookies")
    }
    // ... test implementation
}
```

Tests that use the filesystem but do not depend on inotify-specific
behavior (e.g., config loading, store operations, HTTP endpoint tests)
run on both macOS and Linux.

## VS Code configuration

Recommended `.vscode/settings.json` for the repository:

```json
{
  "go.testEnvVars": {
    "HOMEDRIVE_LOG": "stderr"
  },
  "go.testFlags": ["-v", "-race"],
  "go.toolsEnvVars": {
    "GOOS": "linux",
    "GOARCH": "arm64"
  },
  "gopls": {
    "build.env": {
      "GOOS": "linux"
    }
  }
}
```

Key settings explained:

- `go.testEnvVars.HOMEDRIVE_LOG=stderr`: enables debug logging when
  running tests from VS Code.
- `go.testFlags`: always run with `-v` (verbose) and `-race` (race
  detector).
- `go.toolsEnvVars`: tells the Go toolchain to target Linux, so
  cross-compilation issues surface in the editor.
- `gopls.build.env.GOOS=linux`: makes gopls type-check the Linux build.
  This catches macOS-only code paths at edit time rather than in CI.

### Launch configuration

`.vscode/launch.json` for debugging the agent locally:

```json
{
  "version": "0.2.0",
  "configurations": [
    {
      "name": "homedrive run",
      "type": "go",
      "request": "launch",
      "mode": "auto",
      "program": "${workspaceFolder}/homedrive/cmd/homedrive",
      "args": ["run", "--dry-run", "--config", "/tmp/homedrive-dev.yaml"],
      "env": {
        "HOMEDRIVE_LOG": "stderr",
        "HOMEDRIVE_LOG_LEVEL": "debug"
      }
    }
  ]
}
```

## CI invariants

Every PR must verify these invariants before merge:

```bash
# Binary size must be under 25 MB (stripped)
GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" \
  -o homedrive-bin ./homedrive/cmd/homedrive
du -h homedrive-bin
# Expected: < 25M

# Exactly 1 rclone backend registered
go tool nm homedrive-bin | grep -c rclone/backend/
# Expected: 1

# Test coverage above 70%
go test -race -coverprofile=coverage.out ./homedrive/...
go tool cover -func=coverage.out | tail -1
# Expected: total > 70.0%

# No fmt.Println in source
grep -r 'fmt.Println' homedrive/ --include='*.go' | grep -v _test.go
# Expected: no output

# No panic outside main
grep -rn 'panic(' homedrive/ --include='*.go' | grep -v main.go | grep -v _test.go
# Expected: no output
```

## Troubleshooting

### "too many open files" on Linux

If tests fail with `EMFILE` errors, increase the inotify limits:

```bash
sudo sysctl fs.inotify.max_user_watches=524288
sudo sysctl fs.inotify.max_user_instances=512
```

These are set permanently by the `99-homedrive-inotify.conf` sysctl
file installed during packaging.

### gopls shows errors for Linux-only code on macOS

Ensure `gopls.build.env.GOOS` is set to `linux` in VS Code settings
(see above). If using a different editor, set the `GOOS=linux`
environment variable for the gopls process.

### Tests pass locally but fail in CI

Common causes:

- Filesystem timing differences (CI runners may be slower; increase
  debounce timeouts in flaky tests).
- Missing `t.Skip` for inotify-dependent tests on non-Linux CI
  environments (CI uses ubuntu-latest, so this should not happen in
  practice).
- Race conditions exposed by the `-race` flag (fix the race; do not
  remove the flag).
