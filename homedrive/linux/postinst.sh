#!/bin/sh
set -e
# Per-user config lives at ~/.config/homedrive/config.yaml.
# Per-user state/log dirs are created by systemd at service start
# via StateDirectory=homedrive/%i and LogsDirectory=homedrive/%i.

# The package installs the binary at /usr/bin/homedrive (matching the
# systemd unit's ExecStart=/usr/bin/homedrive run, an absolute path that
# is unaffected by PATH). /usr/local/bin is ordered before /usr/bin in
# the default Debian/Ubuntu PATH, so a stray binary left there by a
# manual dev deploy (e.g. an old `make deploy-pi`) would silently shadow
# this package's binary for any operator running `homedrive`
# interactively -- systemd keeps working, but `homedrive ctl status` or
# a manual `homedrive run` could run the wrong build. Flag it loudly
# rather than fail the install: the package itself is fine either way,
# and failing dpkg's postinst leaves the package half-configured, which
# is worse than the PATH-shadowing risk this is warning about.
if [ -e /usr/local/bin/homedrive ]; then
	echo "ERROR: /usr/local/bin/homedrive exists and will shadow the just-installed /usr/bin/homedrive on PATH." >&2
	echo "        This is usually a stale manual deploy (e.g. 'make deploy-pi'). Remove it with:" >&2
	echo "            sudo rm /usr/local/bin/homedrive" >&2
fi

# Apply inotify sysctl (file already placed by the package).
sysctl --system
systemctl daemon-reload
