# homedrive

Bidirectional Google Drive sync agent for headless ARM64 Linux
(Raspberry Pi NAS), written in Go.

## Overview

homedrive replaces paid cloud sync services with a self-hosted,
offline-first Google Drive sync agent designed for always-on ARM64
devices like a Raspberry Pi NAS. The local disk is the source of truth;
changes are pushed in real time and pulled every 30 seconds.

Key properties:

- **Offline-first**: the external disk is authoritative. Network outages
  are tolerated; a job queue with exponential backoff drains when
  connectivity returns.
- **Real-time push**: fsnotify watches detect file changes on close and
  feed a per-path debouncer, then a worker pool that calls the Google
  Drive API via rclone's library.
- **Periodic pull**: the Drive Changes API is polled every 30 seconds.
  An hourly bisync pass acts as a safety net for missed events.
- **Efficient directory renames**: inotify cookie-based pairing collapses
  `mv dir_50k_files new_name` to a single Drive API metadata update,
  regardless of subtree size.
- **Conflict resolution**: newer-wins policy by default; the losing
  version is preserved as `<file>.old.<N>`.
- **Home Assistant integration**: MQTT auto-discovery publishes sync
  status, queue depth, quota usage, and conflict events.
- **Minimal binary**: only the `drive` rclone backend is linked,
  keeping the stripped binary under 25 MB.

## Features

| Area | Description |
|---|---|
| Push sync | fsnotify watcher with 2s debounce, 2-worker pool, retry with backoff |
| Pull sync | Drive Changes API (30s) + bisync safety net (1h) |
| Directory rename | Cookie-paired inotify events, O(1) Drive call |
| Conflict handling | `newer_wins` / `local_wins` / `remote_wins` policies, `.old.<N>` archive |
| Loop prevention | mtime-based echo suppression via the local journal |
| Quota awareness | MQTT warning at 90%, push pause at 99%, hysteresis resume |
| HTTP control | `/status`, `/pause`, `/resume`, `/resync`, `/reload`, `/healthz`, `/metrics` |
| MQTT publishing | HA Discovery, state sensors, event stream, LWT |
| Dry-run mode | `--dry-run` flag logs intended actions without remote writes |
| Exclusion filters | Glob patterns for `.git`, editor temps, `node_modules`, etc. (exclude-only, no includes -- see [migration doc](docs/migrating-rclone-filters.md)) |
| Systemd packaging | Templated per-user unit, hardened, with logrotate and sysctl tuning |

### Directory semantics

Only files are synced; directories exist as a side effect of the files
beneath them.

- **Push**: a local directory create/write event is ignored (never uploaded
  as a file). Drive folders are created implicitly when a file under them is
  uploaded, so an **empty local directory is not created on Drive**.
- **Pull**: a Drive folder reported by the Changes API is ignored. Local
  directories are created when a file beneath them is downloaded, so an
  **empty Drive folder is not created locally**. Folders are not journaled.

Empty directories therefore do not sync in either direction (issue #105).

## Quick start

### Prerequisites

- Go 1.22+ (build host)
- An `rclone.conf` with a configured Google Drive remote (see
  [rclone drive docs](https://rclone.org/drive/)) -- **use your own
  `client_id`/`client_secret`** (`rclone config` → "Google Application
  Client Id"), not rclone's shared default client. `homedrive` polls the
  Drive Changes API directly (PLAN.md §7.1) using the OAuth2 token stored
  in `rclone.conf`; without your own client credentials there, token
  refresh will start failing once the currently cached access token
  expires (a warning is logged at startup when they're missing).
- A Linux ARM64 or AMD64 target (Raspberry Pi 4/5, any Ubuntu/Debian box)

### Install from source

```bash
# Clone the repository
git clone https://github.com/asnowfix/home-drive.git
cd home-drive

# Cross-compile for the Pi
GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" \
  -o dist/homedrive ./homedrive/cmd/homedrive

# Copy to the target
scp dist/homedrive user@pi:/usr/local/bin/

# Install systemd unit, sysctl, logrotate
# (see linux/ directory for packaging files)
```

### Run

```bash
# Create the per-user config first (see "Configuration" below)
mkdir -p ~/.config/homedrive
cp homedrive/linux/config.yaml ~/.config/homedrive/config.yaml
$EDITOR ~/.config/homedrive/config.yaml

# Start the sync agent -- `--config` is optional; it defaults to
# ~/.config/homedrive/config.yaml (XDG per-user config dir)
homedrive run

# Dry-run mode (no remote changes, useful for verification)
homedrive run --dry-run
```

### Systemd (production)

`homedrive@<user>.service` refuses to start (via `ExecCondition`) until
`/home/<user>/.config/homedrive/config.yaml` exists -- create it first (see
"Run" above), as the target user, before enabling the unit.

```bash
# Enable and start for a specific user
sudo systemctl enable --now homedrive@fix.service

# Check status
sudo systemctl status homedrive@fix.service
journalctl -u homedrive@fix.service -f
```

### User-mode install (`systemctl --user`, no sudo)

An additive alternative to the system unit, for driving the daily
build/install/restart loop from a session with no tty (so sudo cannot
prompt), e.g. a remote-control session. It installs
`homedrive/linux/homedrive.service` as `~/.config/systemd/user/homedrive.service`
and the binary as `~/.local/bin/homedrive`. The system unit, the `.deb` and
the existing `make install`/`start`/... targets are unchanged. **Run one or
the other against a given `local_root`, never both**: the user-mode targets
refuse to proceed while `homedrive@<user>.service` is active or enabled.

#### One-time setup (root, on a console)

The user-mode targets detect each of these and print the command; they never
call sudo. Replace `fix` with your user.

```bash
# 1. Disable and stop the system unit (migration only; skip on a fresh host)
sudo systemctl disable --now homedrive@fix.service

# 2. Let the user manager run without a login session (survives logout and
#    starts at boot). Check: loginctl show-user fix -p Linger
sudo loginctl enable-linger fix

# 3. Host-wide inotify limits, if the live values are below the shipped ones
#    (fs.inotify.max_user_watches=524288, max_user_instances=512). Not needed
#    if the system package/`make install` was already run on this host.
sudo install -m 0644 homedrive/linux/99-homedrive-inotify.conf /etc/sysctl.d/
sudo sysctl --system
```

#### Paths: state and audit log

`state.path` and `state.audit_log` have no built-in defaults, and the agent
creates their parent directories itself, so no code or unit change is
needed: the paths are simply set in `~/.config/homedrive/config.yaml`.
Explicit values are never reinterpreted.

- **Fresh host**: use user-writable locations (the XDG state dir):

  ```yaml
  state:
    path: /home/fix/.local/state/homedrive/state.db
    audit_log: /home/fix/.local/state/homedrive/audit.jsonl
  ```

  (systemd `StateDirectory=` was not used: the agent reads absolute paths
  from the config, so the unit would have to be kept in sync with it for no
  gain.)
- **Migrating from the system unit**: leave the config unchanged. On an
  installed host `/var/lib/homedrive/<user>/` and `/var/log/homedrive/<user>/`
  are already owned by that user, so the user-mode agent keeps using the
  existing `state.db`. The journal matters for loop prevention (PLAN.md §7.3)
  and `.old.<N>` numbering (§11); starting a new one would forget both. The
  audit log also keeps being rotated by the installed logrotate rule. Moving
  to `~/.local/state` later is: stop the agent, copy `state.db`, edit the
  config, start the agent.

#### Daily flow

```bash
make install-user   # first time: build, install binary + unit, enable (not started)
make start-user     # once config.yaml and the rclone remote exist
make deploy-local   # every update: build, atomically replace the binary, restart
make status-user    # also: logs-user, stop-user, restart-user
make uninstall-user # removes unit + binary; keeps config, state and audit log
```

The binary is written to a temp name in `~/.local/bin` and renamed over the
old one, so a running agent is never exposed to a half-written file.
Without a login session, `systemctl --user` needs
`XDG_RUNTIME_DIR=/run/user/$(id -u)` (and linger, above).

#### What differs from the system unit

- **Hardening reduced to `NoNewPrivileges=true`.** Verified on systemd 259:
  `ProtectSystem=`, `ProtectHome=`, `PrivateTmp=`, `ProtectKernelTunables=`
  and `ProtectControlGroups=` are accepted but have no effect in an
  unprivileged user manager (the service's mount namespace is the host's),
  and `ProtectKernelModules=true` makes the unit fail with
  `218/CAPABILITIES`. They were dropped rather than kept as decoration. The
  agent runs as your user with write access to your whole home. Behaviour on
  the NAS's systemd 252 was not verified.
- **No `After=network-online.target`**: a user manager cannot order against
  system targets; `Restart=on-failure` (60 s) covers an early start.
- **Starts at boot only with linger**; without it, only while you are
  logged in.
- **No `StateDirectory=`/`LogsDirectory=`**: directories come from the
  configured paths. The audit log is rotated only while it stays under
  `/var/log/homedrive/*/` (the shipped logrotate rule); under
  `~/.local/state` it is not rotated.
- **Logs** go to the user journal (`make logs-user`).
- `/etc/default/homedrive` is not read; log defaults are in the unit and can
  be overridden in `~/.config/homedrive/env`.

## Configuration

homedrive uses two configuration layers:

1. `/etc/default/homedrive` -- minimal environment variables for systemd
   (log level, log destination).
2. `~/.config/homedrive/config.yaml` -- full YAML configuration, per-user
   (XDG config dir). This is the file the `homedrive@<user>.service`
   systemd instance requires before it will start (`ExecCondition`), and
   the path `homedrive run` defaults `--config` to when the flag is
   omitted. It is *not* set via `/etc/default/homedrive`; the binary
   resolves it itself via `os.UserConfigDir()` at startup (see
   [PLAN.md §4.2](PLAN.md#4-configuration)).

### Example `config.yaml`

```yaml
local_root: /mnt/external/gdrive
remote: gdrive:
rclone_config: /home/fix/.config/rclone/rclone.conf

watcher:
  debounce: 2s
  dir_rename_pair_window: 500ms
  exclude:
    - "**/.git/**"
    - "**/.DS_Store"
    - "**/*.swp"
    - "**/node_modules/**"

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
  policy: newer_wins
  retention:
    max_per_file: 3    # keep N newest .old.<N> siblings per base file; 0 = unlimited
    max_age: 0s         # expire siblings older than this; 0s = never (default)
    sweep_interval: 24h  # periodic full-journal sweep; 0s = disabled
  repair_chains: true  # one-time collapse of pre-existing nested chains, see PLAN.md §11.5

state:
  path: /var/lib/homedrive/state.db
  audit_log: /var/log/homedrive/audit.jsonl

http:
  listen: 127.0.0.1:6090
  # auth_token: ""   # required if listen is ever bound off loopback
  # ctl_timeout: 10s # default `homedrive ctl` HTTP timeout; overridden by --timeout

mqtt:
  enabled: true
  broker: tcp://192.168.1.2:1883
  base_topic: homedrive
  ha_discovery_prefix: homeassistant

dry_run: false
```

See [PLAN.md](PLAN.md) section 4 for the full configuration reference
with all available fields.

### HTTP control endpoint auth

`http.listen` defaults to `127.0.0.1:6090` and, with `http.auth_token`
left unset, the control endpoint stays unauthenticated for loopback
access -- this is unchanged, zero-config behavior.

If you set `http.listen` to anything other than a loopback address
(`127.0.0.1`, `::1`, or `localhost`) -- e.g. to let another host on the
LAN poll `/status` directly -- you **must** also set `http.auth_token`.
The server fails closed: it refuses to start if it would otherwise bind
off loopback without a token. When `http.auth_token` is set, every
request to every route (`/status`, `/pause`, `/resume`, `/resync`,
`/reload`, `/conflict/repair`, `/healthz`, `/metrics`) must carry a matching
`Authorization: Bearer <auth_token>` header, regardless of bind address.
`homedrive ctl <cmd>` reads `http.auth_token` from the same `--config`
file and sends it automatically.

### `ctl` HTTP timeout

Every `homedrive ctl` subcommand bounds its HTTP call to the control
endpoint with a timeout, 10s by default. Most subcommands (`status`,
`pause`, `resume`, `resync`) are fast, in-memory operations well under
that budget, but `ctl conflict repair` without `--dry-run` does a live
remote `RemoteFS.List` call that can legitimately take longer under Drive
API slowness or rate-limiting. Two ways to raise it, in precedence order:

1. `homedrive ctl --timeout 60s conflict repair` -- a one-off override.
2. `http.ctl_timeout: 60s` in `config.yaml` -- the default for every
   `ctl` call when `--timeout` isn't passed, useful on a NAS with known
   slow/rate-limited Drive access.

If neither is set, the 10s default is unchanged. A zero or negative value
from either `--timeout` or `http.ctl_timeout` is treated as **unset**, not
as "no timeout" -- it falls through to the next level of precedence (and
ultimately to the 10s default) instead of disabling the timeout, since an
unbounded wait against an unresponsive agent is a worse outcome for an
interactive CLI than a bounded one.

## CLI usage

```
homedrive [flags] <command>

Commands:
  run               Start the sync agent
  ctl status        Show agent status (queries HTTP endpoint)
  ctl pause         Pause sync operations
  ctl resume        Resume sync operations
  ctl resync        Force an immediate bisync
  ctl conflict repair [--dry-run]
                    Collapse pre-existing nested .old.<N> chains onto
                    their base file (PLAN.md §11.5); runs automatically
                    once on upgrade, this triggers it on demand

Global flags:
  --dry-run         Log intended actions without making remote changes
  --config string   Path to config.yaml
  --version         Print version and exit

ctl flags:
  --timeout duration  HTTP timeout for control endpoint calls
                       (default: http.ctl_timeout from config, or 10s)
```

### Examples

```bash
# Check the running agent's status
homedrive ctl status

# Pause sync before maintenance
homedrive ctl pause

# Resume after maintenance
homedrive ctl resume

# Force a full bisync (useful after restoring from backup)
homedrive ctl resync

# Preview what a nested .old.<N> chain repair would do, then run it
homedrive ctl conflict repair --dry-run
homedrive ctl conflict repair
```

## Home Assistant integration

When MQTT is enabled, homedrive publishes auto-discovery messages so
that Home Assistant creates entities automatically. Sensors include sync
status, queue depth, quota usage, and conflict counts. Events are
published for push/pull success/failure, conflicts, directory renames,
and quota warnings.

See [docs/home-assistant.md](docs/home-assistant.md) for the full entity
list, topic structure, and example automations.

## Development

### Build commands

```bash
# Build for the host into dist/homedrive (the default target)
make build

# Install on this Linux machine as homedrive@$USER.service (uses sudo; run
# from your own terminal). Create ~/.config/homedrive/config.yaml and the
# rclone remote first, then `make start`.
make install
make start     # also: stop, restart, status, logs, uninstall

# Same, per-user via `systemctl --user` (no sudo); see "User-mode install"
make install-user
make deploy-local   # daily: build, atomic binary swap, restart

# Local Mac build (type-checking only, not runnable on macOS in production)
make build-mac

# Cross-compile for Raspberry Pi (linux/arm64)
make build-arm64

# Cross-compile for x86_64 Linux
make build-amd64
```

### Testing

```bash
# Base suite: go vet + all packages under the race detector
make check

# Run tests on Linux via OrbStack (required for inotify-dependent tests)
make test-linux

# Run tests on the production Pi
make test-pi

# Run a single package's tests
orb run -m dev -- go test -race ./homedrive/internal/watcher/...
```

### CI invariants

Every PR must pass these checks:

- Binary size < 25 MB (stripped)
- Exactly 1 rclone backend registered (`drive`)
- Test coverage > 70%
- No `panic` outside `main`
- No `fmt.Println` -- structured `slog` only

See [docs/dev-environment.md](docs/dev-environment.md) for the full
development setup guide.

## Documentation

| Document | Description |
|---|---|
| [PLAN.md](PLAN.md) | Full execution plan, architecture, and phase tracking |
| [docs/architecture.md](docs/architecture.md) | Runtime topology, module layout, data flows |
| [docs/conflict-resolution.md](docs/conflict-resolution.md) | Newer-wins algorithm and `.old.<N>` naming |
| [docs/directory-rename.md](docs/directory-rename.md) | Cookie-based rename pairing and performance |
| [docs/dev-environment.md](docs/dev-environment.md) | macOS + OrbStack setup, cross-compilation, VS Code |
| [docs/home-assistant.md](docs/home-assistant.md) | MQTT entities, topics, and HA automations |
| [docs/manual-validation.md](docs/manual-validation.md) | End-to-end test checklist for Pi validation |
| [docs/migrating-rclone-filters.md](docs/migrating-rclone-filters.md) | Translating rclone `--filter`/`--exclude` rules to `watcher.exclude`; no include/allow-list support |
| [docs/nas-install-log.md](docs/nas-install-log.md) | Log of the actual install on `gruissan` (2026-05-31) |

## License

See the repository root [LICENSE](../LICENSE).
