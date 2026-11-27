# homedrive

Bidirectional Google Drive sync agent for headless ARM64 Linux
(Raspberry Pi NAS), written in Go.

## Overview

homedrive replaces paid cloud sync services with a self-hosted,
offline-first Google Drive sync agent designed for always-on ARM64
devices like a Raspberry Pi NAS. The local disk is the source of truth;
changes are pushed in real time and pulled every 30 seconds.

Key properties:

- **Offline-first**: the external disk is authoritative. Network outages
  are tolerated; a job queue with exponential backoff drains when
  connectivity returns.
- **Real-time push**: fsnotify watches detect file changes on close and
  feed a per-path debouncer, then a worker pool that calls the Google
  Drive API via rclone's library.
- **Periodic pull**: the Drive Changes API is polled every 30 seconds.
  An hourly bisync pass acts as a safety net for missed events.
- **Efficient directory renames**: inotify cookie-based pairing collapses
  `mv dir_50k_files new_name` to a single Drive API metadata update,
  regardless of subtree size.
- **Conflict resolution**: newer-wins policy by default; the losing
  version is preserved as `<file>.old.<N>`.
- **Home Assistant integration**: MQTT auto-discovery publishes sync
  status, queue depth, quota usage, and conflict events.
- **Minimal binary**: only the `drive` rclone backend is linked,
  keeping the build lean enough for a Raspberry Pi 3B+.

## Features

| Area | Description |
|---|---|
| Push sync | fsnotify watcher with 2s debounce, 2-worker pool, retry with backoff |
| Pull sync | Drive Changes API (30s) + bisync safety net (1h) |
| Directory rename | Cookie-paired inotify events, O(1) Drive call |
| Conflict handling | `newer_wins` / `local_wins` / `remote_wins` policies, `.old.<N>` archive |
| Loop prevention | mtime-based echo suppression via the local journal |
| Quota awareness | MQTT warning at 90%, push pause at 99%, hysteresis resume |
| HTTP control | `/status`, `/pause`, `/resume`, `/resync`, `/bisync/allow-deletes`, `/reload`, `/healthz`, `/metrics` |
| MQTT publishing | HA Discovery, state sensors, event stream, LWT |
| Dry-run mode | `--dry-run` flag logs intended actions without remote writes |
| Exclusion filters | Glob patterns for `.git`, editor temps, `node_modules`, etc. (exclude-only, no includes -- see [migration doc](docs/migrating-rclone-filters.md)) |
| Systemd packaging | Templated per-user unit, hardened, with logrotate and sysctl tuning |

### Directory semantics

Only files are synced; directories exist as a side effect of the files
beneath them.

- **Push**: a local directory create/write event is ignored (never uploaded
  as a file). Drive folders are created implicitly when a file under them is
  uploaded, so an **empty local directory is not created on Drive**.
- **Pull**: a Drive folder reported by the Changes API is ignored. Local
  directories are created when a file beneath them is downloaded, so an
  **empty Drive folder is not created locally**. Folders are not journaled.

Empty directories therefore do not sync in either direction (issue #105).

## Quick start

### Prerequisites

- Go 1.22+ (build host)
- An `rclone.conf` with a configured Google Drive remote (see
  [rclone drive docs](https://rclone.org/drive/)) -- **use your own
  `client_id`/`client_secret`** (`rclone config` → "Google Application
  Client Id"), not rclone's shared default client. `homedrive` polls the
  Drive Changes API directly (PLAN.md §7.1) using the OAuth2 token stored
  in `rclone.conf`; without your own client credentials there, token
  refresh will start failing once the currently cached access token
  expires (a warning is logged at startup when they're missing).
- A Linux ARM64 or AMD64 target (Raspberry Pi 4/5, any Ubuntu/Debian box)

### Install from source

```bash
# Clone the repository
git clone https://github.com/asnowfix/home-drive.git
cd home-drive

# Cross-compile for the Pi
GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" \
  -o dist/homedrive ./homedrive/cmd/homedrive

# Copy to the target
scp dist/homedrive user@pi:/usr/local/bin/

# Install systemd unit, sysctl, logrotate
# (see linux/ directory for packaging files)
```

### Run

```bash
# Create the per-user config first (see "Configuration" below)
mkdir -p ~/.config/homedrive
cp homedrive/linux/config.yaml ~/.config/homedrive/config.yaml
$EDITOR ~/.config/homedrive/config.yaml

# Start the sync agent -- `--config` is optional; it defaults to
# ~/.config/homedrive/config.yaml (XDG per-user config dir)
homedrive run

# Dry-run mode (no remote changes, useful for verification)
homedrive run --dry-run
```

### Systemd (production)

`homedrive@<user>.service` refuses to start (via `ExecCondition`) until
`/home/<user>/.config/homedrive/config.yaml` exists -- create it first (see
"Run" above), as the target user, before enabling the unit.

```bash
# Enable and start for a specific user
sudo systemctl enable --now homedrive@fix.service

# Check status
sudo systemctl status homedrive@fix.service
journalctl -u homedrive@fix.service -f
```

### User-mode install (`systemctl --user`, no sudo)

An additive alternative to the system unit, for driving the daily
build/install/restart loop from a session with no tty (so sudo cannot
prompt), e.g. a remote-control session. It installs
`homedrive/linux/homedrive.service` as `~/.config/systemd/user/homedrive.service`
and the binary as `~/.local/bin/homedrive`. The system unit, the `.deb` and
the existing `make install`/`start`/... targets are unchanged. **Run one or
the other against a given `local_root`, never both**: the user-mode targets
refuse to proceed while `homedrive@<user>.service` is active or enabled.

#### One-time setup (root, on a console)

The user-mode targets detect each of these and print the command; they never
call sudo. Replace `fix` with your user.

```bash
# 1. Disable and stop the system unit (migration only; skip on a fresh host)
sudo systemctl disable --now homedrive@fix.service

# 2. Let the user manager run without a login session (survives logout and
#    starts at boot). Check: loginctl show-user fix -p Linger
sudo loginctl enable-linger fix

# 3. Host-wide inotify limits, if the live values are below the shipped ones
#    (fs.inotify.max_user_watches=524288, max_user_instances=512). Not needed
#    if the system package/`make install` was already run on this host.
sudo install -m 0644 homedrive/linux/99-homedrive-inotify.conf /etc/sysctl.d/
sudo sysctl --system
```

#### Check the prerequisites (no root needed)

Run these before and after the root steps above; each prints what the
corresponding `make` guard checks.

```bash
# Old system unit must be inactive AND disabled (two agents on one local_root
# would fight; the user-mode targets refuse otherwise).
systemctl is-active homedrive@fix; systemctl is-enabled homedrive@fix
# -> inactive / disabled

# Linger must be on, or the user manager (and the agent) dies at logout.
loginctl show-user fix -p Linger
# -> Linger=yes

# inotify limits must be at least the shipped values (524288 / 512).
sysctl fs.inotify.max_user_watches fs.inotify.max_user_instances
# -> fs.inotify.max_user_watches = 524288
# -> fs.inotify.max_user_instances = 512
```

Why each matters: the unit and `make` refuse to start next to the system
unit (double sync of one directory); without linger a remote-control or ssh
session has no user manager to talk to; too-low inotify limits make the
watcher silently miss events on large trees. You also need
`~/.config/homedrive/config.yaml` and a working `~/.config/rclone/rclone.conf`
for the `remote:` named in it; the agent validates the token at startup and
logs `startup oauth validation: token refresh succeeded`.

#### Paths: state and audit log

`state.path` and `state.audit_log` have no built-in defaults, and the agent
creates their parent directories itself, so no code or unit change is
needed: the paths are simply set in `~/.config/homedrive/config.yaml`.
Explicit values are never reinterpreted.

- **Fresh host**: use user-writable locations (the XDG state dir):

  ```yaml
  state:
    path: /home/fix/.local/state/homedrive/state.db
    audit_log: /home/fix/.local/state/homedrive/audit.jsonl
  ```

  (systemd `StateDirectory=` was not used: the agent reads absolute paths
  from the config, so the unit would have to be kept in sync with it for no
  gain.)
- **Migrating from the system unit**: leave the config unchanged. On an
  installed host `/var/lib/homedrive/<user>/` and `/var/log/homedrive/<user>/`
  are already owned by that user, so the user-mode agent keeps using the
  existing `state.db`. The journal matters for loop prevention (PLAN.md §7.3)
  and `.old.<N>` numbering (§11); starting a new one would forget both. The
  audit log also keeps being rotated by the installed logrotate rule. Moving
  to `~/.local/state` later is: stop the agent, copy `state.db`, edit the
  config, start the agent.

#### Daily flow

```bash
make install-user   # first time: build, install binary + unit, enable (not started)
make start-user     # once config.yaml and the rclone remote exist
make deploy-local   # every update: build, atomically replace the binary, restart
make status-user    # also: logs-user, stop-user, restart-user
make uninstall-user # removes unit + binary; keeps config, state and audit log
```

The binary is written to a temp name in `~/.local/bin` and renamed over the
old one, so a running agent is never exposed to a half-written file.
The targets default `XDG_RUNTIME_DIR` to `/run/user/<uid>` when it is
unset, so they work from sessions without a login (ssh without PAM, cron,
remote runners); linger (above) is still required for the user manager to
exist there.

#### Verify it is running

```bash
make status-user                      # Active: active (running)
homedrive --version                   # must match the commit you deployed
curl -s 127.0.0.1:6090/status         # "state":"running", last_pull advancing
make logs-user                        # JSON log lines via journalctl --user
```

A healthy start logs, in order: token refresh succeeded, `rclone remote
initialized`, `starting homedrive agent`, `http server starting`,
`watcher started`. `last_pull` in `/status` should move every
`pull.changes_api_interval` (30 s by default).

#### What differs from the system unit

- **Hardening reduced to `NoNewPrivileges=true`.** Verified on systemd 259:
  `ProtectSystem=`, `ProtectHome=`, `PrivateTmp=`, `ProtectKernelTunables=`
  and `ProtectControlGroups=` are accepted but have no effect in an
  unprivileged user manager (the service's mount namespace is the host's),
  and `ProtectKernelModules=true` makes the unit fail with
  `218/CAPABILITIES`. They were dropped rather than kept as decoration. The
  agent runs as your user with write access to your whole home. Behaviour on
  the NAS's systemd 252 was not verified.
- **No `After=network-online.target`**: a user manager cannot order against
  system targets; `Restart=on-failure` (60 s) covers an early start.
- **Starts at boot only with linger**; without it, only while you are
  logged in.
- **No `StateDirectory=`/`LogsDirectory=`**: directories come from the
  configured paths. The audit log is rotated only while it stays under
  `/var/log/homedrive/*/` (the shipped logrotate rule); under
  `~/.local/state` it is not rotated.
- **Logs** go to the user journal (`make logs-user`).
- `/etc/default/homedrive` is not read; log defaults are in the unit and can
  be overridden in `~/.config/homedrive/env`.

### Local trash

When a file is deleted on Drive, homedrive moves its local copy to the
[freedesktop.org Trash](https://specifications.freedesktop.org/trash-spec/latest/)
instead of erasing it (issue #155). Empty directories are simply removed:
there is nothing in them to recover. The Drive side keeps rclone's default
(Drive's own trash).

Where the trash lives:

- **User unit (`homedrive.service`)**: the home trash,
  `$XDG_DATA_HOME/Trash` (`~/.local/share/Trash`), by a plain rename. GNOME
  shows it.
- **System unit (`homedrive@<user>.service`)**: `ProtectHome=read-only` and a
  per-path `ReadWritePaths` give the service its own bind mounts, so renaming
  into the home trash fails (`EXDEV`/`EROFS`). homedrive then uses the spec's
  topdir trash of the mount that holds the file, found in
  `/proc/self/mountinfo`. When `local_root` is itself a mount point, as on the
  NAS, that is **`<local_root>/.Trash-<uid>`**, inside the synced tree. A
  valid admin-created `<topdir>/.Trash/<uid>` is used instead when it exists.
  GNOME does not show this trash on a headless NAS; browse it on disk
  (`files/` holds the data, `info/*.trashinfo` the original path and date).
- `.Trash-<uid>` and `.Trash` directly under `local_root` are **always
  excluded** (watcher, push, bisync, pull), whatever `watcher.exclude`
  says, so trashed files are never pushed back to Drive. A Drive folder
  named `.Trash` or `.Trash-<digits>` at the root of the remote therefore
  stops syncing too.
- A topdir trash is refused (the file is kept and an error logged) when the
  file's mount is nested *inside* `local_root`, because that trash would sit
  in the synced tree under a name that is not excluded. Trash directories
  that are symlinks are never used or purged through.
- Conflict-loser retention (`.old.<N>` eviction) still deletes outright; it
  does not use the trash.
- If neither trash can take a file, homedrive never copies it and never
  erases it: it logs an error, writes an audit line (`op: trash` with an
  `error`), and leaves the file and its journal entry, so the deletion is
  replanned on the next pull or bisync.

Purging: GNOME's housekeeping does not run on a headless host, so homedrive
purges **only the entries it trashed itself** (recorded in its state
database, with the trash directory, entry name and `DeletionDate`) once they
are older than `trash.purge_after` (default `720h`, 30 days; `0s` disables).
An entry is purged only while its `.trashinfo` still matches the record;
anything else in a trash is never touched. Each trash and each purge writes
one audit line (`op: trash`, `op: trash_purge`). With `--dry-run` nothing is
trashed or purged; the deletion is audited as before.

## Configuration

homedrive uses two configuration layers:

1. `/etc/default/homedrive` -- minimal environment variables for systemd
   (log level, log destination).
2. `~/.config/homedrive/config.yaml` -- full YAML configuration, per-user
   (XDG config dir). This is the file the `homedrive@<user>.service`
   systemd instance requires before it will start (`ExecCondition`), and
   the path `homedrive run` defaults `--config` to when the flag is
   omitted. It is *not* set via `/etc/default/homedrive`; the binary
   resolves it itself via `os.UserConfigDir()` at startup (see
   [PLAN.md §4.2](PLAN.md#4-configuration)).

### Example `config.yaml`

```yaml
local_root: /mnt/external/gdrive
remote: gdrive:
rclone_config: /home/fix/.config/rclone/rclone.conf

watcher:
  debounce: 2s
  dir_rename_pair_window: 500ms
  exclude:
    - "**/.git/**"
    - "**/.DS_Store"
    - "**/*.swp"
    - "**/node_modules/**"

push:
  workers: 2
  retry:
    max_attempts: 5
    initial_backoff: 5s
    max_backoff: 5m

pull:
  changes_api_interval: 30s
  bisync_interval: 1h

bisync:
  # Mass-deletion guard: a bisync run's deletions (local + Drive together) are
  # blocked when there are more than 10 AND (more than max_delete_count OR more
  # than max_delete_pct % of the journaled entries). An empty local walk or an
  # empty Drive listing against a non-empty journal blocks them whatever the
  # count. Pushes, pulls and conflicts of that run still happen. Allow one run:
  # `homedrive ctl allow-deletes` then `homedrive ctl resync`.
  max_delete_count: 50  # >= 1
  max_delete_pct: 10    # 1..100

conflict:
  policy: newer_wins
  retention:
    max_per_file: 3    # keep N newest .old.<N> siblings per base file; 0 = unlimited
    max_age: 0s         # expire siblings older than this; 0s = never (default)
    sweep_interval: 24h  # periodic full-journal sweep; 0s = disabled
  repair_chains: true  # one-time collapse of pre-existing nested chains, see PLAN.md §11.5

trash:
  purge_after: 720h  # purge entries homedrive trashed after this age; 0s = never (default 720h = 30 days)

state:
  path: /var/lib/homedrive/state.db
  audit_log: /var/log/homedrive/audit.jsonl

http:
  listen: 127.0.0.1:6090
  # auth_token: ""   # required if listen is ever bound off loopback
  # ctl_timeout: 10s # default `homedrive ctl` HTTP timeout; overridden by --timeout

mqtt:
  enabled: true
  broker: tcp://192.168.1.2:1883
  base_topic: homedrive
  ha_discovery_prefix: homeassistant

dry_run: false
```

See [PLAN.md](PLAN.md) section 4 for the full configuration reference
with all available fields.

### Bisync mass-deletion guard

Bisync propagates deletions both ways from the journal, so a bad listing, a
stale journal or an unmounted `local_root` could plan many at once. Before it
executes anything, bisync counts the deletions it planned (local deletions of
files removed on Drive, plus Drive deletions of files removed locally) as `n`
and vetoes all of them when

- `n > 10` **and** (`n > bisync.max_delete_count` **or** `n` is more than
  `bisync.max_delete_pct` percent of the journaled entries); or
- the local walk, or the Drive listing, is empty while `n > 0` (the journal
  is non-empty by construction): an unmounted or emptied `local_root`, or a
  replaced root folder or an rclone remote re-pointed to another account.
  This blocks whatever the count, and **aborts the whole bisync pass**
  (no pushes, pulls or conflict handling either), so the agent cannot
  repopulate an empty side or push to a wrong remote and let the next run
  slip under the floor. The block persists until the operator overrides it
  or the side really holds files again.

Runs with 10 deletions or fewer are blocked only by the empty-side rule. A
blocked run still pushes, pulls and resolves conflicts, and leaves the
journal untouched for the vetoed paths, so the next run plans the same
deletions. It writes a `delete_blocked` audit line (planned count, threshold,
reason, first 20 paths), publishes `<base_topic>/<host>/<user>/events/bisync/delete_blocked`
(`{ts,type,planned,threshold,reason,paths,dry_run}`; `reason` is `threshold`,
`empty_local` or `empty_remote`) and shows `bisync_delete_blocked`
(`planned`, `threshold`, `reason`, `since`) in `/status` until a run is no
longer vetoed. In dry-run mode the same report is made and nothing is deleted.

To allow the deletions once, after checking they are intended:

```bash
homedrive ctl allow-deletes   # POST /bisync/allow-deletes (loopback, like /resync)
homedrive ctl resync
```

The override can only be armed while a run is blocked (otherwise the
endpoint answers 409). It is consumed by the next bisync run (hourly or
forced), whether or not that run had anything to delete, and releases only
a block of the same reason with no more planned deletions than the one it
was armed against; anything else is blocked again with its own figures. The
journal denominator counts only rows with a sync baseline that are not
excluded. The Changes API pull path has no count guard of its own; it is
covered by the `local_root` guard below.

### Symlinks are never synced

A symlink inside `local_root` is skipped, whatever it points to (dangling,
outside `local_root`, or inside it): it is never uploaded, never retried and
never followed, and the watcher does not watch through a link to a directory.
Each skipped link is logged once per run at ERROR (`symlink skipped: not
synced`, with `path` and `reason` `unresolved`, `outside_root` or `in_root`).
`GET /status` shows `"skipped_symlinks": N` and the status v2 document shows
`guards.skipped_symlinks` (`{count, since}`); both are absent/null at zero and
clear once the links are deleted or replaced by regular files (the next
bisync pass notices). No MQTT event is published: the retained status topic
carries the alert state. A symlink already pushed to Drive as a file by an
earlier version is left alone on Drive (not deleted, not overwritten).
By default, replace links with real copies to sync their content, or opt in
as below.

#### Opt-in: `symlinks.copy_resolved_links`

```yaml
symlinks:
  copy_resolved_links:
    - "Brain/.claude/skills/**"   # links under here are candidates
    - "Brain/shared/**"           # ...and their targets must be under here
```

The default is an empty list: nothing changes. The list is machine-local
config, read only from this file (never from the synced tree, so nothing
written remotely can enable it), and applies after a restart. A symlink under
a folder matched by a pattern (patterns are `doublestar` globs relative to
`local_root`; absolute patterns and `..` are rejected at start-up) is copied
to Drive as content, not skipped, only when all of these hold:

1. the link resolves (Lstat, Readlink, then resolve) to a regular file or a
   directory tree, on the same filesystem as the link (same device ID);
2. the resolved target is inside `local_root`, under a folder that is itself
   matched by a pattern in the same list (targets outside `local_root` are
   not copied);
3. a directory tree is copied recursively, and any symlink inside it is
   skipped and reported by the rule above (never dereferenced).

A link failing a check stays skipped and reported, with the reason in the
ERROR line: `unresolved`, `outside_root`, `in_root` (not under a matched
folder), `target_not_matched`, `cross_device` or `not_regular`. A pattern that
matches no folder logs one WARN at start. Content behind a copied link is
push-only: it is uploaded on a file event (a link to a file) or by bisync (a
link to a directory, and later changes), never pulled, deleted locally or
renamed through the link, and Drive-side changes there are left alone with a
WARN. Drive paths that are or go through any symlink are refused for every
local write, copied or not. `GET /status` lists the effective patterns
(`copy_resolved_links`, `copy_resolved_links_count`); the status v2 document
carries `copy_resolved_links` (the count only).

### local_root guard (`.homedrive-root`)

The puller, the push syncer and bisync act on `local_root`. If the data disk
holding it is absent they would act on whatever sits at that path (the root
filesystem, an empty mount point). Before each unit of work the agent checks
that `local_root` resolves (symlinks followed) to a directory that carries
the identity it recorded:

- On the first start (empty journal) the agent writes a random id to
  `<local_root>/.homedrive-root` (atomically: temp file, fsync, rename) and
  then records it in the state DB. The marker is local bookkeeping: it is
  never synced and a Drive entry of that name is ignored. **Do not delete
  it.** Upgrading an existing install adopts the tree once, provided at
  least one journaled path exists under `local_root`. `dry_run` writes
  nothing.
- When the check fails (reason `missing`, `marker_missing`,
  `identity_mismatch` or `unreadable`) pulls, pushes, bisync, `ctl resync`,
  `ctl conflict repair` (both answer 503) and the trash purge do nothing: no
  download, no local delete or trash, no journal change, no remote
  operation. `/status` shows `"state":"root_blocked"` and
  `"local_root_blocked":{"reason","detail","since"}`, an ERROR log names the
  reason, the audit log gets a `root_blocked` line (`root_resumed` on
  recovery), and MQTT gets `events/root/blocked` / `events/root/resumed`
  (`{ts,type:"root.blocked"|"root.resumed",reason,detail}`, not retained).
- If the data disk is absent when the agent starts and the journal is
  populated, the agent does not create `local_root`; it starts in a
  blocked-only mode (HTTP, MQTT, audit and logs, no watcher, pull, push or
  bisync) and re-checks every `pull.changes_api_interval`.
- **Recovery is a restart.** When the check passes again the agent logs it,
  audits `root_resumed`, publishes the event and exits non-zero, so the
  supervisor (`Restart=on-failure`, `RestartSec=60` in the shipped units)
  restarts it with fresh inotify watches. The old watches die with the
  unmounted tree and are not re-armed in process. Changes made while paused
  are picked up by the next bisync pass.

Notes:

- **systemd `ReadWritePaths=`**: write the `local_root` entry with the `-`
  prefix (`ReadWritePaths=-/path/to/local_root`). Without it systemd refuses
  to start the unit with `226/NAMESPACE` when the path does not exist (an
  absent data disk), so the agent never reaches the blocked-only mode above.
  `make install` now generates it that way. A unit installed earlier needs the
  same one-character edit in its drop-in (for the system unit,
  `/etc/systemd/system/homedrive@<user>.service.d/rw-paths.conf`), followed
  by `systemctl daemon-reload`; this is not done automatically on existing
  hosts.
- **After a resume restart** the watcher starts from scratch and local
  changes made while paused are only picked up by the next bisync pass, which
  can be up to `pull.bisync_interval` (one hour by default) after the
  restart. `ctl resync` forces it.
- **Never reset (delete) the state DB while the data disk is absent.** The
  guard cannot protect that case. With an empty journal and a `local_root`
  that is a real directory (not a symlink) under an unmounted mount point,
  the agent sees a first install: it records a new identity for that stray
  empty directory and starts downloading the whole Drive into it, on the root
  filesystem. Once the disk is mounted again that copy stays hidden under the
  mount point, and the agent is blocked with `identity_mismatch` (the disk's marker
  differs from the new id). To clean up: stop the agent, unmount the disk,
  delete the stray files left in the mount point directory, mount the disk,
  restore the previous state DB (or, if there is no copy, delete the new one
  so the next start adopts the disk's own marker and rebuilds the journal
  from a bisync pass), then start the agent. Do not edit the
  marker to match. Upgrading an install that predates the guard adopts the
  tree only when a journaled path exists under the resolved `local_root`.

If the tree is correct but the marker was lost (deleted by hand, a copy that
skipped dotfiles), the block reason is `marker_missing` and `detail` carries
the expected id. With the agent running, restore the marker (the agent then
exits and restarts by itself):

```bash
printf '%s\n' <expected-id> > <local_root>/.homedrive-root
```

For `identity_mismatch` the directory belongs to another install or tree:
fix the mount or `local_root`; do not edit the marker to match unless you
are certain it is the right tree.

### HTTP control endpoint auth

`http.listen` defaults to `127.0.0.1:6090` and, with `http.auth_token`
left unset, the control endpoint stays unauthenticated for loopback
access -- this is unchanged, zero-config behavior.

If you set `http.listen` to anything other than a loopback address
(`127.0.0.1`, `::1`, or `localhost`) -- e.g. to let another host on the
LAN poll `/status` directly -- you **must** also set `http.auth_token`.
The server fails closed: it refuses to start if it would otherwise bind
off loopback without a token. When `http.auth_token` is set, every
request to every route (`/status`, `/pause`, `/resume`, `/resync`,
`/reload`, `/conflict/repair`, `/healthz`, `/metrics`) must carry a matching
`Authorization: Bearer <auth_token>` header, regardless of bind address.
`homedrive ctl <cmd>` reads `http.auth_token` from the same `--config`
file and sends it automatically.

### `ctl` HTTP timeout

Every `homedrive ctl` subcommand bounds its HTTP call to the control
endpoint with a timeout, 10s by default. Most subcommands (`status`,
`pause`, `resume`, `resync`) are fast, in-memory operations well under
that budget, but `ctl conflict repair` without `--dry-run` does a live
remote `RemoteFS.List` call that can legitimately take longer under Drive
API slowness or rate-limiting. Two ways to raise it, in precedence order:

1. `homedrive ctl --timeout 60s conflict repair` -- a one-off override.
2. `http.ctl_timeout: 60s` in `config.yaml` -- the default for every
   `ctl` call when `--timeout` isn't passed, useful on a NAS with known
   slow/rate-limited Drive access.

If neither is set, the 10s default is unchanged. A zero or negative value
from either `--timeout` or `http.ctl_timeout` is treated as **unset**, not
as "no timeout" -- it falls through to the next level of precedence (and
ultimately to the 10s default) instead of disabling the timeout, since an
unbounded wait against an unresponsive agent is a worse outcome for an
interactive CLI than a bounded one.

## CLI usage

```
homedrive [flags] <command>

Commands:
  run               Start the sync agent
  ctl status        Show agent status (queries HTTP endpoint)
  ctl pause         Pause sync operations
  ctl resume        Resume sync operations
  ctl resync        Force an immediate bisync
  ctl allow-deletes Allow the next bisync run's deletions once, bypassing
                    the mass-deletion guard
  ctl conflict repair [--dry-run]
                    Collapse pre-existing nested .old.<N> chains onto
                    their base file (PLAN.md §11.5); runs automatically
                    once on upgrade, this triggers it on demand

Global flags:
  --dry-run         Log intended actions without making remote changes
  --config string   Path to config.yaml
  --version         Print version and exit

ctl flags:
  --timeout duration  HTTP timeout for control endpoint calls
                       (default: http.ctl_timeout from config, or 10s)
```

### Examples

```bash
# Check the running agent's status
homedrive ctl status

# Pause sync before maintenance
homedrive ctl pause

# Resume after maintenance
homedrive ctl resume

# Force a full bisync (useful after restoring from backup)
homedrive ctl resync

# Preview what a nested .old.<N> chain repair would do, then run it
homedrive ctl conflict repair --dry-run
homedrive ctl conflict repair
```

## Home Assistant integration

When MQTT is enabled, homedrive publishes auto-discovery messages so
that Home Assistant creates entities automatically. Sensors include sync
status, queue depth, quota usage, and conflict counts. Events are
published for push/pull success/failure, conflicts, directory renames,
and quota warnings.

See [docs/home-assistant.md](docs/home-assistant.md) for the full entity
list, topic structure, and example automations.

## Development

### Build commands

```bash
# Build for the host into dist/homedrive (the default target)
make build

# Install on this Linux machine as homedrive@$USER.service (uses sudo; run
# from your own terminal). Create ~/.config/homedrive/config.yaml and the
# rclone remote first, then `make start`.
make install
make start     # also: stop, restart, status, logs, uninstall

# Same, per-user via `systemctl --user` (no sudo); see "User-mode install"
make install-user
make deploy-local   # daily: build, atomic binary swap, restart

# Local Mac build (type-checking only, not runnable on macOS in production)
make build-mac

# Cross-compile for Raspberry Pi (linux/arm64)
make build-arm64

# Cross-compile for x86_64 Linux
make build-amd64
```

### Testing

```bash
# Base suite: go vet + all packages under the race detector
make check

# Run tests on Linux via OrbStack (required for inotify-dependent tests)
make test-linux

# Run tests on the production Pi
make test-pi

# Run a single package's tests
orb run -m dev -- go test -race ./homedrive/internal/watcher/...
```

### CI invariants

Every PR must pass these checks:

- Runs on a Raspberry Pi 3B+ alongside other workloads (binary size reported, not gated)
- Exactly 1 rclone backend registered (`drive`)
- Test coverage > 70%
- No `panic` outside `main`
- No `fmt.Println` -- structured `slog` only

See [docs/dev-environment.md](docs/dev-environment.md) for the full
development setup guide.

## Documentation

| Document | Description |
|---|---|
| [PLAN.md](PLAN.md) | Full execution plan, architecture, and phase tracking |
| [docs/architecture.md](docs/architecture.md) | Runtime topology, module layout, data flows |
| [docs/conflict-resolution.md](docs/conflict-resolution.md) | Newer-wins algorithm and `.old.<N>` naming |
| [docs/directory-rename.md](docs/directory-rename.md) | Cookie-based rename pairing and performance |
| [docs/dev-environment.md](docs/dev-environment.md) | macOS + OrbStack setup, cross-compilation, VS Code |
| [docs/home-assistant.md](docs/home-assistant.md) | MQTT entities, topics, and HA automations |
| [docs/live-testing.md](docs/live-testing.md) | Exercising a build against real Drive with an isolated instance; cleanup pitfalls |
| [docs/manual-validation.md](docs/manual-validation.md) | End-to-end test checklist for Pi validation |
| [docs/migrating-rclone-filters.md](docs/migrating-rclone-filters.md) | Translating rclone `--filter`/`--exclude` rules to `watcher.exclude`; no include/allow-list support |
| [docs/nas-install-log.md](docs/nas-install-log.md) | Log of the actual install on `gruissan` (2026-05-31) |

## License

See the repository root [LICENSE](../LICENSE).
