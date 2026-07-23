---
name: homedrive-systemd
description: Linux packaging conventions for homedrive — templated systemd unit, /etc/default loading, sysctl tuning at install, logrotate, and hardening directives. Apply when modifying anything in homedrive/linux/.
---

# homedrive Linux packaging

## Templated unit file

`linux/homedrive@.service` is templated by user (`%i`). Config is per-user
XDG (`~/.config/homedrive/config.yaml`, resolved by the binary itself via
`os.UserConfigDir()` — see `defaultConfigPath()` in `cmd/homedrive/main.go`),
**not** a shared `/etc/homedrive/config.yaml`. This file is the source of
truth; keep it in sync with `linux/homedrive@.service`:

```ini
[Unit]
Description=homedrive sync agent for %i
Documentation=https://github.com/asnowfix/home-drive/blob/main/homedrive/README.md
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=%i
Group=%i
EnvironmentFile=/etc/default/homedrive
EnvironmentFile=-/etc/default/homedrive.%i
ExecCondition=/bin/sh -c 'test -f $(getent passwd %i | cut -d: -f6)/.config/homedrive/config.yaml'
ExecStart=/usr/bin/homedrive run
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure
RestartSec=10
StateDirectory=homedrive/%i
LogsDirectory=homedrive/%i

# Hardening — keep these
ProtectSystem=strict
ProtectHome=read-only
PrivateTmp=true
NoNewPrivileges=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true

LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
```

`ExecStart` omits `--config` on purpose: the binary resolves the per-user
config path itself, which sidesteps the `%h` systemd specifier (it resolves
to `/root` in system units regardless of `User=`). `ExecCondition` gates
startup on that same path so a missing config fails fast with a clear
`systemctl status` reason instead of a crash-restart loop.
`StateDirectory=`/`LogsDirectory=` (not `postinst.sh`) create
`/var/lib/homedrive/<user>/` and `/var/log/homedrive/<user>/`, owned by that
user, at service start.

Activation (requires `~/.config/homedrive/config.yaml` to already exist for
that user — see "Configuration" in `homedrive/README.md`):
```sh
sudo systemctl enable --now homedrive@fix.service
```

## /etc/default loading rules

- `/etc/default/homedrive` — global, loaded for every instance. Required.
- `/etc/default/homedrive.<user>` — per-user override. Optional, hence
  the `-` prefix in `EnvironmentFile=-`.
- Both files contain only shell variables (no logic).
- **No config-path variable lives here.** Rich config is always
  `~/.config/homedrive/config.yaml`, resolved by the binary itself; do not
  reintroduce a `HOMEDRIVE_CONFIG` env var or an `/etc/homedrive/` path.

Sample `/etc/default/homedrive`:
```sh
HOMEDRIVE_LOG_LEVEL=info
HOMEDRIVE_LOG=stderr
```

## sysctl at install (NOT in unit file)

Do **not** put sysctl tuning in the systemd unit. Apply it at package
install time via `linux/postinst.sh`:

```sh
install -m 0644 99-homedrive-inotify.conf /etc/sysctl.d/
sysctl --system
```

`linux/99-homedrive-inotify.conf`:
```
fs.inotify.max_user_watches=524288
fs.inotify.max_user_instances=512
```

Reasons:
1. sysctl is host-wide; the unit file is per-instance.
2. The unit file may run before `/etc/sysctl.d/` is applied at boot.
3. Users on shared hosts can audit `/etc/sysctl.d/` separately.

## logrotate

`linux/homedrive.logrotate` → installed to `/etc/logrotate.d/homedrive`.
Note the glob: each user instance logs to its own
`/var/log/homedrive/<user>/audit.jsonl` (via `LogsDirectory=homedrive/%i`),
so the pattern must match all of them, and `copytruncate` is used instead of
a reload hook since there's no single PID to signal:

```
/var/log/homedrive/*/audit.jsonl {
    weekly
    rotate 12
    compress
    delaycompress
    missingok
    notifempty
    copytruncate
}
```

Weekly rotation, keep 12 (~3 months of audit history).

## postinst.sh (idempotent)

```sh
#!/bin/sh
set -e
# Per-user config lives at ~/.config/homedrive/config.yaml.
# Per-user state/log dirs are created by systemd at service start
# via StateDirectory=homedrive/%i and LogsDirectory=homedrive/%i.
# Apply inotify sysctl (file already placed by the package).
sysctl --system
systemctl daemon-reload
```

Must be idempotent — running it twice should not error. Notably absent:
no `/etc/homedrive` directory creation (config is per-user, created by the
user/operator, not by the package) and no manual `/var/lib`/`/var/log`
directory creation (systemd's `StateDirectory=`/`LogsDirectory=` do that).

## File ownership

| Path | Owner | Mode | Notes |
|---|---|---|---|
| `~/.config/homedrive/config.yaml` | `<user>`:`<user>` | 0600 | per-user config, created by the user/operator, not the package |
| `/etc/default/homedrive` | root:root | 0644 | systemd env |
| `/etc/default/homedrive.<user>` | root:`<user>` | 0640 | per-user env |
| `/var/lib/homedrive/<user>/` | `<user>`:`<user>` | 0750 | state DB, created by systemd `StateDirectory=` |
| `/var/log/homedrive/<user>/` | `<user>`:`<user>` | 0750 | audit log, created by systemd `LogsDirectory=` |

Each instance's state DB and audit log are owned by that instance's own
user — `StateDirectory=`/`LogsDirectory=` create and `chown` them
automatically, so there is no shared/root-owned data directory to reconcile
across instances.

## Verification on Pi

After install:
```sh
systemctl status homedrive@$(whoami)
journalctl -u homedrive@$(whoami) -f
curl http://127.0.0.1:6090/healthz
sysctl fs.inotify.max_user_watches  # should be 524288
```

## What NOT to do

- Don't put credentials in `/etc/default/homedrive`. They go in
  `~/.config/rclone/rclone.conf` owned by the user.
- Don't run as root. `User=%i` is mandatory.
- Don't relax `ProtectSystem=strict`. `StateDirectory=`/`LogsDirectory=`
  already grant write access to their own per-user paths; add further
  `ReadWritePaths=` explicitly if a new writable path is ever needed.
- Don't reintroduce a shared `/etc/homedrive/config.yaml` or a
  `HOMEDRIVE_CONFIG` env var — config is per-user XDG, resolved by the
  binary itself (§ "Templated unit file" above).
- Don't add `--config` to `ExecStart` — it would require the `%h` systemd
  specifier, which resolves to `/root` in system units regardless of
  `User=`.
