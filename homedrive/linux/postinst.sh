#!/bin/sh
set -e
install -d -m 0755 -o root -g root /etc/homedrive
install -d -m 0750 -o root -g root /var/lib/homedrive
install -d -m 0750 -o root -g root /var/log/homedrive
install -m 0644 99-homedrive-inotify.conf /etc/sysctl.d/
install -m 0644 homedrive.logrotate /etc/logrotate.d/homedrive
sysctl --system
