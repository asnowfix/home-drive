# Desktop baseline: session-bound `rclone mount` (D0)

This page documents the **interim baseline** of desktop mode: a plain
`rclone mount` of your Google Drive on `~/GoogleDrive`, started and stopped with
your graphical session by a systemd **user** unit. It needs no homedrive binary
and no Go code. Phases D1 and later of
[the desktop-mode design](design/desktop-mode.md) (epic #147) replace it with
`homedrive mount`; this unit stays useful until then and is the baseline those
phases are compared against.

Do not confuse it with NAS mode (`homedrive.service`, a two-way sync of a local
folder): here nothing is synced. Files are fetched from Drive when opened and
kept in a local cache.

## Prerequisites

- A desktop session that starts `graphical-session.target` (recent GNOME, KDE
  Plasma 6). Lighter desktops that do not will never start the unit.
- `fuse3` (provides `fusermount3`), with your user allowed to use FUSE.
- **rclone v1.76.0**, the version `homedrive/go.mod` pins and the one the trial
  ran. The distribution package may be older (v1.60.1 on the trial host) and was
  **not tested**. The unit runs `~/.local/bin/rclone` by default (install
  v1.76.0 there, for example with `go install github.com/rclone/rclone@v1.76.0`
  and copying the binary, or from the rclone release archive); set `HD_RCLONE`
  in the settings file to use another path.
- `~/GoogleDrive` must be absent or empty: rclone refuses a non-empty mount
  point, and the unit would then retry every minute for ever.
- Enough disk for the cache (see "Cache size").

## Paths

All follow the XDG base directories; the unit uses systemd's user-manager
specifiers, so it honours `XDG_CACHE_HOME` and `XDG_CONFIG_HOME` **as seen by
your systemd user manager** (not only your shell).

| What | Path | Default |
|---|---|---|
| Mount point | `%h/GoogleDrive` | `~/GoogleDrive` |
| Cache | `$XDG_CACHE_HOME/home-drive` (`%C/home-drive`) | `~/.cache/home-drive` |
| Private token file | `$XDG_CONFIG_HOME/homedrive/rclone.conf` (`%E/homedrive/rclone.conf`) | `~/.config/homedrive/rclone.conf` |
| Optional settings | `$XDG_CONFIG_HOME/homedrive/desktop-rclone.env` | `~/.config/homedrive/desktop-rclone.env` (absent by default) |

rclone's VFS writes `vfs/<remote>/` and `vfsMeta/<remote>/` (and a small `kv/`
database) under the cache directory, nothing outside it (checked in the rclone
v1.76.0 source: every cache user resolves through `--cache-dir`).

## Token setup

The token store is **private to homedrive**: never `~/.config/rclone/rclone.conf`
(shared with the `rclone` CLI) and never the token file of another running
instance. Two processes refreshing one file race each other.

```bash
mkdir -p ~/.config/homedrive
rclone config --config ~/.config/homedrive/rclone.conf   # create a Drive remote named "gdrive"
chmod 600 ~/.config/homedrive/rclone.conf
```

Use your own OAuth client id (see the [README](../README.md) and
[oauth-troubleshooting.md](oauth-troubleshooting.md)). The unit mounts the
remote named `gdrive:`. If `XDG_CONFIG_HOME` is set, use that directory instead
of `~/.config`.

The unit's defaults are written once (`Environment=` in the unit). Override them
without editing the unit, in the optional settings file
`~/.config/homedrive/desktop-rclone.env` (`KEY=value` lines):

| Variable | Default | Meaning |
|---|---|---|
| `HD_MOUNT` | `%h/GoogleDrive` | mount point |
| `HD_REMOTE` | `gdrive:` | rclone remote (and path) to mount |
| `HD_RCLONE` | `%h/.local/bin/rclone` | rclone binary |
| `HD_CONFIG` | `%E/homedrive/rclone.conf` | token file (also set in `AssertPathExists=`: change both, with `systemctl --user edit`) |
| `RCLONE_VFS_CACHE_MAX_SIZE` | unset | cache size cap, see "Cache size" |

## Install, enable, disable, uninstall

Written for you to run; nothing here is done by the repository.

```bash
# install and enable
mkdir -p ~/.config/systemd/user
cp homedrive/linux/homedrive-desktop-rclone.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now homedrive-desktop-rclone.service

# check
systemctl --user status homedrive-desktop-rclone.service
journalctl --user -u homedrive-desktop-rclone.service -f

# disable (stop now and at the next login)
systemctl --user disable --now homedrive-desktop-rclone.service

# uninstall
rm ~/.config/systemd/user/homedrive-desktop-rclone.service
systemctl --user daemon-reload
```

### What uninstall leaves behind

Nothing below is removed by the steps above:

- **The cache**, `${XDG_CACHE_HOME:-$HOME/.cache}/home-drive`. It can be large
  and **may hold files not yet uploaded to Drive** (see "Stop"). Before deleting
  it, mount once more and wait until the journal shows no pending uploads, then
  `rm -rf "${XDG_CACHE_HOME:-$HOME/.cache}/home-drive"`.
- The optional settings file `~/.config/homedrive/desktop-rclone.env` and the
  desktop token file `~/.config/homedrive/rclone.conf`: remove each by name
  if you want them gone.
- **Never remove `~/.config/homedrive/` as a whole**: it also holds the
  NAS-mode `config.yaml`.

If the token file does not exist, the start **fails visibly** (`AssertPathExists=`):
`systemctl --user status homedrive-desktop-rclone.service` shows "Assertion
failed ... rclone.conf". Create the token, then run `systemctl --user start
homedrive-desktop-rclone.service`; it is not retried by itself.

`enable` links the unit under `graphical-session.target.wants`, so the mount
appears when your session starts and disappears at logout (`PartOf=`). The unit
is deliberately **never** `WantedBy=default.target`: with `loginctl
enable-linger` that would mount Drive at boot without a session. `Type=notify`
is used because rclone v1.76.0's foreground `mount` sends `READY=1` once the
mount is live (`cmd/mountlib/mount.go` calls `lib/systemd.Notify`), so
`systemctl start` returns when the mount works.

**Stop does not flush.** Stopping sends SIGTERM. In rclone v1.76.0 that
unmounts, cancels in-flight uploads and exits at once (`TimeoutStopSec=45` only
bounds a slow unmount); it does **not** wait for the upload queue. Files still
dirty at logout stay in the cache (marked dirty) and upload after the next
start. Before logging out, check `journalctl --user -u
homedrive-desktop-rclone.service` for pending uploads. D1 replaces this with a
real drain (design section 9.3).

Cleanup of a crashed mount: before start and after stop, the unit runs
`fusermount3 -uz` **only** if the mount point is an `rclone` FUSE mount whose
daemon is gone (`stat` fails); a live mount is never touched. By hand:
`fusermount3 -u ~/GoogleDrive`. A clean stop exits with status 143, which the
unit treats as success.

If the start fails (bad token, wrong remote), it is retried every 60 s without
limit.

## Cache size

The unit sets `--vfs-cache-mode full`, `--vfs-cache-min-free-space 5G`,
`--vfs-cache-poll-interval 10s`, `--poll-interval 15s` (Drive change poll) and
`--vfs-cache-max-age 87600h`. The long max age is deliberate: rclone's default
of 1 h would empty the cache hourly. Eviction is therefore by **size** only.

The recommended cap is **25% of the filesystem holding the cache**. rclone has
no percentage option, so compute it once and put it in the optional settings
file, which the unit reads:

```bash
cache_parent=${XDG_CACHE_HOME:-$HOME/.cache}
mkdir -p "$cache_parent"
# 25% of the filesystem size, in GiB (df reports 1K blocks)
gib=$(( $(df --output=size -k "$cache_parent" | tail -n 1) / 1024 / 1024 / 4 ))
[ "$gib" -ge 1 ] || gib=1   # 0 would mean "no cap" to rclone
mkdir -p ~/.config/homedrive
echo "RCLONE_VFS_CACHE_MAX_SIZE=${gib}G" > ~/.config/homedrive/desktop-rclone.env
systemctl --user restart homedrive-desktop-rclone.service
```

Without that file the cache has no size cap; only the 5 GiB minimum-free-space
guard bounds it. The cap is **soft** (see below).

## Known limits (measured in the 2026-11-15 trial)

Full data: design doc sections 3.2-3.3. The ones that matter day to day:

- **No pinning.** Nothing can be kept "always available"; any file can be
  evicted.
- **`touch` and `stat` do not refresh the LRU position.** Only open, read and
  write count as access.
- **The cap is soft.** It is enforced at each cache poll tick (10 s here); the
  cache reached 60 MiB against a 50 MiB cap in the trial. Open files are never
  evicted.
- **No useful offline mode.** Cached content is readable. Uncached reads,
  listing a never-listed folder, `mkdir`, rename and delete **block for minutes**
  (150-543 s measured) even with short timeouts, so a file manager browsing an
  unlisted folder freezes. New files and appends to cached files are queued and
  upload on rclone's own retry timer (up to 5 minutes after the network returns).
- **A dirty local file silently overwrites a newer Drive version.** If a cached
  file was edited locally and also changed on Drive meanwhile, rclone uploads the
  local version, with no check, no `.old.<N>` copy and no event. Drive's own
  revision history is the only recovery.
- **Logout does not flush queued uploads** (see "Stop"): a file written just
  before logout reaches Drive only at your next login, which makes the silent
  overwrite above more likely if you edit it elsewhere meanwhile.
- **No MQTT, no Home Assistant entities, no journal of sync events.** Only
  rclone's logs in the journal.

## Coexistence

- **Never put the mount point inside, or around, a NAS-mode `local_root`.** The
  default `~/GoogleDrive` must not be, contain or sit inside the `local_root`
  of `config.yaml` (for example `~/gdrive`). The unit does not check this.
- **A mount and a root-syncing NAS-mode agent on the same Drive fight**: a
  NAS-mode agent syncing the Drive root creates `.old.<N>` copies for files that
  appear on Drive (design 3.4, #146). If NAS mode syncs only a sub-folder, that
  folder is reachable both ways; edits propagate but disk use doubles.
- **Never run this unit together with D1+ desktop mode (`homedrive mount`) on
  the same mount point or the same token file.** Disable this unit first.
- Start and stop of this unit unmount a dead rclone mount on the mount point
  (never a live one).
- Never point `--config` at another running instance's token file.
