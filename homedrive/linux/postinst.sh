#!/bin/sh
set -e
# /etc/homedrive is owned by root; per-user state and log dirs are created by
# systemd at service start via StateDirectory=homedrive/%i / LogsDirectory=homedrive/%i.
install -d -m 0755 -o root -g root /etc/homedrive
# Apply inotify sysctl (file already placed by the package).
sysctl --system
systemctl daemon-reload
