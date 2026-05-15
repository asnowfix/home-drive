#!/bin/sh
set -e
# /etc/homedrive is created here; per-user state and log dirs are created by
# systemd at service start via StateDirectory=homedrive/%i / LogsDirectory=homedrive/%i.
install -d -m 0755 -o root -g root /etc/homedrive
install -m 0644 99-homedrive-inotify.conf /etc/sysctl.d/
install -m 0644 homedrive.logrotate /etc/logrotate.d/homedrive
sysctl --system
