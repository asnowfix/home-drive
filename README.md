# homedrive

**Bidirectional Google Drive sync agent for headless Linux NAS (ARM64).**

A Dropbox replacement for Raspberry Pi NAS use cases, written in Go, with
real-time push (fsnotify), 30s pull (Drive Changes API), an hourly bisync
safety net, MQTT publishing for Home Assistant integration, and minimal
rclone import (only the Google Drive backend).

> **Status: v0.1.0 released.** All 14 implementation phases are complete
> and tagged as [`homedrive/v0.1.0`](https://github.com/asnowfix/home-drive/releases/tag/homedrive%2Fv0.1.0).
> See [`RELEASE_NOTES.md`](./RELEASE_NOTES.md) for what shipped and
> [`homedrive/README.md`](homedrive/README.md) for install/usage docs.

## Features

- Offline-first storage — the local disk is the source of truth.
- Real-time push of local modifications to Drive (on file close).
- Periodic pull of remote modifications (Drive Changes API every 30s) +
  an hourly bisync safety net.
- Conflict policy: *newer wins*, the loser kept as `.old.<N>`.
- Efficient directory rename handling (single Drive API call, regardless
  of subtree size).
- Headless ARM64, packaged as a templated systemd service.
- HTTP control endpoint (`/status`, `/pause`, `/resync`, `/healthz`, `/metrics`).
- MQTT publisher for Home Assistant Discovery + custom automations.
- Designed for future cross-device sync via the same MQTT broker (not
  implemented in v0.1).

## Non-goals (v0.1)

- Multi-pair sync, GUI, Windows, macOS clients.
- Google Docs/Sheets/Slides binary export — skipped + warned.
- Cross-device peer sync — *designed for*, not implemented.

## Repository layout

```
home-drive/
├── README.md                        # this file
├── RELEASE_NOTES.md                 # v0.1.0 release notes
├── homedrive/                       # the Go module
│   ├── PLAN.md                      # architecture reference + phase history
│   ├── README.md                    # install, configuration, CLI usage
│   ├── cmd/ internal/ pkg/          # Go source
│   ├── docs/                        # architecture, conflict resolution,
│   │                                 # rename pairing, dev setup, HA
│   │                                 # integration, manual validation
│   └── linux/                       # systemd unit, sysctl, logrotate, postinst
├── migrate/                          # one-shot Dropbox → Drive migration scripts
├── .claude/
│   ├── agents/
│   │   └── homedrive-implementer.md # agent prompt for Claude Code
│   └── skills/                      # 8 skills (conventions, rclone import,
│                                     # MQTT wrapper, watcher rename, test
│                                     # mocks, systemd, conflict resolution,
│                                     # issue creation)
└── .github/
    ├── workflows/                   # CI
    └── dependabot.yml
```

## Getting started

```bash
git clone https://github.com/asnowfix/home-drive.git
cd home-drive/homedrive

make build-mac        # local Mac build for type-checking
make build-arm64      # cross-compile for the Pi
make test-linux       # tests inside OrbStack Ubuntu VM (real inotify)
make deploy-pi        # build-arm64 + scp + systemctl restart on the NAS
```

See [`homedrive/README.md`](homedrive/README.md) for installation,
configuration, and CLI usage, and `homedrive/PLAN.md` §19 for the full
macOS host → Linux target dev environment setup (OrbStack, build tags,
gopls config).

## Roadmap

All 14 phases (0–13) shipped as atomic PRs; see `homedrive/PLAN.md` §14
for the full phase-by-phase history.

## License

[MIT](./LICENSE).

## Related

- [github.com/asnowfix/home-automation](https://github.com/asnowfix/home-automation) —
  parent project this borrows conventions from (Go workspace, templated
  systemd, MQTT patterns).
