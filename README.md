# homedrive

**Bidirectional Google Drive sync agent for headless Linux NAS (ARM64).**

A Dropbox replacement for Raspberry Pi NAS use cases, written in Go, with
real-time push (fsnotify), 30s pull (Drive Changes API), an hourly bisync
safety net, MQTT publishing for Home Assistant integration, and minimal
rclone import (only the Google Drive backend).

> **Status: pre-alpha.** This repository contains the execution plan
> ([`PLAN.md`](./PLAN.md)) and skill definitions for Claude Code. No
> production code has been written yet. See `PLAN.md` §14 for the phased
> roadmap.

## Goals

- Offline-first storage — the local disk is the source of truth.
- Real-time push of local modifications to Drive (on file close).
- Periodic pull of remote modifications (Drive Changes API every 30s).
- Conflict policy: *newer wins*, the loser kept as `.old.<N>`.
- Headless ARM64, packaged as a templated systemd service.
- HTTP control endpoint (`/status`, `/pause`, `/resync`, `/healthz`, `/metrics`).
- MQTT publisher for Home Assistant Discovery + custom automations.
- Designed for future cross-device sync via the same MQTT broker.

## Non-goals (v0.1)

- Multi-pair sync, GUI, Windows, macOS clients.
- Google Docs/Sheets/Slides binary export — skipped + warned.
- Cross-device peer sync — *designed for*, not implemented.

## Repository layout

```
home-drive/
├── PLAN.md                          # full execution plan (start here)
├── README.md                        # this file
├── .claude/
│   ├── agents/
│   │   └── homedrive-implementer.md # agent prompt for Claude Code
│   └── skills/                      # 8 skills (see PLAN.md §18)
│       ├── homedrive-conventions/
│       ├── homedrive-rclone-import/
│       ├── homedrive-mqtt-wrapper/
│       ├── homedrive-watcher-rename/
│       ├── homedrive-test-mocks/
│       ├── homedrive-systemd/
│       ├── homedrive-conflict-resolution/
│       └── homedrive-issue/
├── docs/                            # ADRs and reference docs
├── linux/                           # systemd unit, sysctl, logrotate, postinst
└── .github/
    ├── workflows/                   # CI
    └── dependabot.yml
```

## Getting started (development)

Once Phase 0 lands, this section will describe:

```bash
make build-mac        # local Mac build for type-check
make build-arm64      # cross-compile for the Pi
make test-linux       # tests inside OrbStack Ubuntu VM (real inotify)
make deploy-pi        # scp + systemctl restart on the NAS
```

See `PLAN.md` §19 for the full macOS host → Linux target dev environment
setup (OrbStack, build tags, gopls config).

## Roadmap

13 phases, ~10 person-days of focused work, each phase shipped as one
atomic PR. See `PLAN.md` §14 for the full breakdown.

| Phase | Title | Effort |
|---|---|---|
| 0 | Bootstrap module | 0.5d |
| 1 | Watcher with rename pairer | 1.5d |
| 2 | Minimal rclone wrapper | 1d |
| 3 | Store + conflict resolution | 1d |
| 4 | MQTT wrapper | 0.5d |
| 5 | Push syncer | 1d |
| 6 | Pull via Drive Changes API | 1d |
| 7 | Bisync safety net | 0.5d |
| 8 | HTTP control + metrics | 0.5d |
| 9 | HA Discovery + state publisher | 0.5d |
| 10 | Quota handling | 0.5d |
| 11 | Packaging (systemd + sysctl + logrotate) | 0.5d |
| 12 | CI / GitHub Actions | 0.5d |
| 13 | Docs + 0.1.0 release | 0.5d |

## License

TBD (likely MIT or Apache-2.0 to match the parent
[home-automation](https://github.com/asnowfix/home-automation) project).

## Related

- [github.com/asnowfix/home-automation](https://github.com/asnowfix/home-automation) —
  parent project this borrows conventions from (Go workspace, templated
  systemd, MQTT patterns).
