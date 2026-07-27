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
# Local Mac build (type-checking only, not runnable on macOS in production)
make build-mac

# Cross-compile for Raspberry Pi (linux/arm64)
make build-arm64

# Cross-compile for x86_64 Linux
make build-amd64
```

### Testing

```bash
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
