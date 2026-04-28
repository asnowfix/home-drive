# homedrive

Bidirectional Google Drive sync agent for headless ARM64 Linux
(Raspberry Pi NAS).

## Status

Pre-alpha. See [PLAN.md](PLAN.md) for the full execution plan and
architecture.

## Features (planned)

- Offline-first: local disk is the source of truth.
- Real-time push of local changes via fsnotify.
- Periodic pull via Drive Changes API (30s) + bisync safety net (1h).
- Efficient directory rename handling (single Drive API call).
- Conflict resolution: newer wins, loser kept as `.old.<N>`.
- MQTT publishing for Home Assistant integration.
- HTTP control endpoint on 127.0.0.1:6090.
- Minimal binary size (< 25 MB, single rclone backend).

## Build

```bash
# Local Mac build (type-checking)
make build-mac

# Cross-compile for Raspberry Pi
make build-arm64

# Cross-compile for x86_64 Linux
make build-amd64
```

## Usage

```bash
# Start the sync agent
homedrive run --config /etc/homedrive/config.yaml

# Dry-run mode (no remote changes)
homedrive run --dry-run --config /etc/homedrive/config.yaml

# Control a running agent
homedrive ctl status
homedrive ctl pause
homedrive ctl resume
homedrive ctl resync
```

## Configuration

See [PLAN.md](PLAN.md) section 4 for the full configuration reference.

## Development

See [docs/dev-environment.md](docs/dev-environment.md) for the
development setup (macOS host + OrbStack Linux VM for tests).

## License

See the repository root [LICENSE](../LICENSE).
