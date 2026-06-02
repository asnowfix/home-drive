#!/bin/sh
set -e
# Per-user config lives at ~/.config/homedrive/config.yaml.
# Per-user state/log dirs are created by systemd at service start
# via StateDirectory=homedrive/%i and LogsDirectory=homedrive/%i.
# Apply inotify sysctl (file already placed by the package).
sysctl --system
systemctl daemon-reload
