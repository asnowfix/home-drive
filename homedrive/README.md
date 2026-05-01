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
| Exclusion filters | Glob patterns for `.git`, editor temps, `node_modules`, etc. |
| Systemd packaging | Templated per-user unit, hardened, with logrotate and sysctl tuning |

## Quick start

### Prerequisites

- Go 1.22+ (build host)
- An `rclone.conf` with a configured Google Drive remote (see
  [rclone drive docs](https://rclone.org/drive/))
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
# Start the sync agent
homedrive run --config /etc/homedrive/config.yaml

# Dry-run mode (no remote changes, useful for verification)
homedrive run --dry-run --config /etc/homedrive/config.yaml
```

### Systemd (production)

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
   (config path, log level).
2. `/etc/homedrive/config.yaml` -- full YAML configuration.

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

state:
  path: /var/lib/homedrive/state.db
  audit_log: /var/log/homedrive/audit.jsonl

http:
  listen: 127.0.0.1:6090

mqtt:
  enabled: true
  broker: tcp://192.168.1.2:1883
  base_topic: homedrive
  ha_discovery_prefix: homeassistant

dry_run: false
```

See [PLAN.md](PLAN.md) section 4 for the full configuration reference
with all available fields.

## CLI usage

```
homedrive [flags] <command>

Commands:
  run               Start the sync agent
  ctl status        Show agent status (queries HTTP endpoint)
  ctl pause         Pause sync operations
  ctl resume        Resume sync operations
  ctl resync        Force an immediate bisync

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

## License

See the repository root [LICENSE](../LICENSE).
