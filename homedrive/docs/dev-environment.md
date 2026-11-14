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
make test-pi        # run tests on the production Pi via SSH (cannot work today, see below)
make deploy-pi      # build-arm64 + scp + restart the per-user unit over SSH (no sudo)
```

The two remote targets need `PI_HOST`, which has **no default** (this
repository is public, so no hostname or address is written in it); they fail
early with a usage message when it is unset. `PI_USER` defaults to the local
user (`whoami`) and is both the ssh login and the owner of the user unit.
`PI_SSH_OPTS` (empty by default) is passed to both `ssh` and `scp`, for a
non-default key or port (`PI_SSH_OPTS='-i ~/.ssh/nas -o Port=2222'`; use
`-o Port=`, since `ssh -p` and `scp -P` differ).

`make test-pi` runs `go test` on the NAS, which has no Go toolchain (and
building on the NAS is against the release rules), so it cannot work as is.
It is kept, pointed at `PI_HOST`, until the maintainer decides to remove it.

## Deploy to Pi for manual testing

`make deploy-pi` cross-compiles locally (never on the NAS), then deploys the
**per-user** unit (`systemctl --user`, see "User-mode install" in
`homedrive/README.md`) over ssh. No sudo is used anywhere. The remote has no
repo, make or Go, so every remote step is a plain ssh/scp command.

```bash
# Build and deploy in one step
make deploy-pi PI_HOST=<nas-host> [PI_USER=<user>]

# Or manually (what the target runs; <user>@<nas-host> is your login there):
GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" \
  -o dist/homedrive-arm64 ./homedrive/cmd/homedrive
# 1. Preflight on the NAS: the system unit must be neither active nor enabled
#    (systemctl is-active / is-enabled homedrive@<user>.service), and
#    `export XDG_RUNTIME_DIR=/run/user/$(id -u)` must make
#    `systemctl --user show-environment` succeed (non-PAM ssh sessions do not
#    set it). The target refuses, and prints the root command, otherwise.
ssh <user>@<nas-host> 'mkdir -p ~/.local/bin ~/.config/systemd/user'
# 2. Copy under temp names, then rename into place (atomic)
scp dist/homedrive-arm64 <user>@<nas-host>:.local/bin/.homedrive.new
scp homedrive/linux/homedrive.service <user>@<nas-host>:.config/systemd/user/.homedrive.service.new
ssh <user>@<nas-host> 'export XDG_RUNTIME_DIR=/run/user/$(id -u); cd &&
  chmod 0755 .local/bin/.homedrive.new &&
  mv -f .local/bin/.homedrive.new .local/bin/homedrive &&
  mv -f .config/systemd/user/.homedrive.service.new .config/systemd/user/homedrive.service &&
  systemctl --user daemon-reload && systemctl --user enable homedrive.service &&
  systemctl --user restart homedrive.service'
```

Differences from what CI ships: the release `.deb` installs the *system*
unit (`/lib/systemd/system`); `deploy-pi` runs the same binary under the
user unit, with reduced hardening (`NoNewPrivileges=true` only) and, under
journald `Storage=volatile`, logs readable only from a root/`adm` account
(`sudo journalctl _UID=<uid>`; `make logs-user` shows nothing for a plain
user). See "What differs from the system unit" in `homedrive/README.md`.

### Switching a host from the system unit to the user unit

Do this only with the owner's agreement: it changes how the production agent
is supervised. Two agents must never share a `local_root`, so `deploy-pi`
refuses while `homedrive@<user>.service` is active or enabled.

Prerequisites on the NAS (check, do not assume): `loginctl show-user <user>
-p Linger` is `yes` (otherwise `loginctl enable-linger <user>` as root); the
inotify limits from `99-homedrive-inotify.conf` are applied; the user's
`~/.config/homedrive/config.yaml` and rclone remote exist. The user unit
reuses the same config, state database and audit log as the system unit.

```bash
# 1. One-time, as root on the NAS (a console or a sudo-capable account):
systemctl disable --now homedrive@<user>.service

# 2. From the dev host:
make deploy-pi PI_HOST=<nas-host> PI_USER=<user>

# 3. Verify: version matches, state is running, last_pull advances
ssh <user>@<nas-host> 'curl -s 127.0.0.1:6090/status'
```

Rollback (the system unit's binary, `/usr/bin/homedrive`, is untouched by
`deploy-pi`, so it is still the previously installed version):

```bash
# as <user> on the NAS (export XDG_RUNTIME_DIR=/run/user/$(id -u) first in a non-PAM ssh session)
systemctl --user disable --now homedrive.service
# then as root on the NAS
systemctl enable --now homedrive@<user>.service
```

Logs: with journald `Storage=volatile`, `journalctl --user` fails for a
non-root user ("No journal files were opened due to insufficient
permissions"), because no per-user journal files exist and the user is in
neither `adm` nor `systemd-journal`. Read the logs from a root or `adm`
account: `sudo journalctl _UID=<uid-of-the-agent-user>`.

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

# Exactly 1 rclone backend explicitly imported (crypt is a transitive
# dependency of drive itself, so a raw binary symbol count over-counts;
# verify at the source-import level instead)
grep -rn '"github.com/rclone/rclone/backend/' homedrive/ --include='*.go' \
  | grep -v '_test.go' | grep -v 'rclone/backend/drive'
# Expected: no output

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
