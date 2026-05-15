#!/bin/sh
set -e
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# /etc/homedrive is created here; per-user state and log dirs are created by
# systemd at service start via StateDirectory=homedrive/%i / LogsDirectory=homedrive/%i.
install -d -m 0755 -o root -g root /etc/homedrive
install -m 0644 "$SCRIPT_DIR/99-homedrive-inotify.conf" /etc/sysctl.d/
install -m 0644 "$SCRIPT_DIR/homedrive.logrotate" /etc/logrotate.d/homedrive
sysctl --system
