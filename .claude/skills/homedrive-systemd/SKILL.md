---
name: homedrive-systemd
description: Linux packaging conventions for homedrive — templated systemd unit, /etc/default loading, sysctl tuning at install, logrotate, and hardening directives. Apply when modifying anything in homedrive/linux/.
---

# homedrive Linux packaging

## Templated unit file

`linux/homedrive@.service` is templated by user (`%i`):

```ini
[Unit]
Description=homedrive sync agent for %i
Documentation=https://github.com/asnowfix/home-drive/blob/main/README.md
After=network-online.target
Wants=network-online.target

[Service]
Type=notify
User=%i
Group=%i
EnvironmentFile=/etc/default/homedrive
EnvironmentFile=-/etc/default/homedrive.%i
ExecStart=/usr/bin/homedrive run --config ${HOMEDRIVE_CONFIG}
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure
RestartSec=10
WatchdogSec=60

# Hardening — keep these
ProtectSystem=strict
ProtectHome=read-only
ReadWritePaths=/var/lib/homedrive /var/log/homedrive
ReadOnlyPaths=/etc/homedrive
PrivateTmp=true
NoNewPrivileges=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true

LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
```

Activation:
```sh
sudo systemctl enable --now homedrive@fix.service
```

## /etc/default loading rules

- `/etc/default/homedrive` — global, loaded for every instance. Required.
- `/etc/default/homedrive.<user>` — per-user override. Optional, hence
  the `-` prefix in `EnvironmentFile=-`.
- Both files contain only shell variables (no logic).
- Rich config lives in `${HOMEDRIVE_CONFIG}` (YAML).

Sample `/etc/default/homedrive`:
```sh
HOMEDRIVE_CONFIG=/etc/homedrive/config.yaml
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

`linux/homedrive.logrotate` → installed to `/etc/logrotate.d/homedrive`:

```
/var/log/homedrive/audit.jsonl {
    weekly
    rotate 12
    compress
    delaycompress
    missingok
    notifempty
    create 0640 homedrive homedrive
    sharedscripts
    postrotate
        systemctl reload 'homedrive@*.service' > /dev/null 2>&1 || true
    endscript
}
```

Weekly rotation, keep 12 (~3 months of audit history). Reload all
instances after rotation so they reopen the file.

## postinst.sh (idempotent)

```sh
#!/bin/sh
set -e
install -d -m 0755 -o root -g root /etc/homedrive
install -d -m 0750 -o root -g root /var/lib/homedrive
install -d -m 0750 -o root -g root /var/log/homedrive
install -m 0644 99-homedrive-inotify.conf /etc/sysctl.d/
install -m 0644 homedrive.logrotate /etc/logrotate.d/homedrive
sysctl --system
```

Must be idempotent — running it twice should not error.

## File ownership

| Path | Owner | Mode | Notes |
|---|---|---|---|
| `/etc/homedrive/` | root:root | 0755 | config dir |
| `/etc/homedrive/config.yaml` | root:root | 0644 | non-secret config |
| `/etc/default/homedrive` | root:root | 0644 | systemd env |
| `/etc/default/homedrive.<user>` | root:`<user>` | 0640 | per-user env |
| `/var/lib/homedrive/` | root:root | 0750 | state, walked by all instances |
| `/var/log/homedrive/` | root:root | 0750 | audit logs |

The daemon runs as `User=%i` (the templated user), but reads the state
DB and audit log under paths owned by root with group rw — sort this
out at install time depending on whether a `homedrive` group exists.

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
- Don't relax `ProtectSystem=strict`. Add to `ReadWritePaths` instead.
- Don't disable `WatchdogSec` — the daemon must call `sd_notify` or be
  restarted.
