# Desktop mode: on-demand Google Drive for a Linux desktop (design, PROPOSED)

Status: **design only, nothing built.** Awaiting a go/no-go from the maintainer
(see "Questions for the maintainer" at the end). Tracking epic: #147. Related
finding from the trial: #146.

## 1. Goal

A Linux desktop sees **every Drive file** in its local filesystem. Content is
downloaded **when a file is opened** and kept in a local cache under a
**configurable size cap**. Eviction is **least recently used**. A user can **pin**
a folder ("keep offline") so its content is always local.

Decided by the maintainer, not re-opened here:

- Linux only (Google's own client covers Windows and macOS).
- Google Drive only.
- Per-folder pinning is wanted.
- The NAS mode (the current `homedrive run`) must not regress.
- Updated 2026-11-15: the rclone import allow-list is lifted **for this study**
  and the 25 MB cap was removed (#149: no size limit, must run on a Raspberry
  Pi 3B+ alongside other workloads). The proposed allow-list is in section 6.
- Decisions of 2026-11-15 (section 12): single executable with two modes, no
  offline mode, default cache cap 25% of the cache filesystem.
- Updated 2026-11-15: desktop mode runs on the maintainer's desktop (the host the
  trial ran on), and it starts and stops **with the desktop session**, unlike NAS
  mode, which is a lingering user service.

Out of scope: Google Docs/Sheets/Slides export (same as NAS mode), Shared
Drives (not asked for; rclone supports them with `team_drive`, but untested
here), a GUI beyond a file-manager emblem (section 9.4, optional).

## 2. Target host (measured 2026-11-15)

| Item | Value |
|---|---|
| Distro | Ubuntu 26.04.1 LTS, x86_64 |
| Kernel | 7.0.0-38-generic |
| FUSE | `fuse3` 3.18.2 (`fusermount3` 3.18.2, setuid helper present), `libfuse3-4` 3.18.2; **no `libfuse-dev`/`libfuse3-dev` headers** |
| `/etc/fuse.conf` | `user_allow_other` **off** (not needed: the mount is only for the user) |
| systemd | 259 |
| Desktop | GNOME Shell 50.1, Wayland, session `ubuntu`; `graphical-session.target` and `gnome-session@ubuntu.target` active in the user manager |
| Linger | `Linger=yes` (the live NAS-mode unit `homedrive.service` is `WantedBy=default.target`) |
| rclone on PATH | distro `rclone v1.60.1-DEV` (2022; **not** used for the trial) |

## 3. Trial: plain `rclone mount` on this host

### 3.1 Setup

- **rclone v1.76.0**, the version `homedrive/go.mod` pins, built from the module
  cache into a scratch directory (`go install github.com/rclone/rclone@v1.76.0`,
  stripped).
- A **temporary copy** of `~/.config/rclone/rclone.conf`, passed with `--config`.
  The live file was never written.
- Scratch Drive folder `gdrive:homedrive-desktop-test`, at the Drive root, outside
  the live agent's `gdrive:Brain`. 60 objects, 165 MiB: eight 20 MiB random files,
  fifty small text files, one nested folder `pinme/sub`. Upload took 37 s.
- Mounted **only that subfolder** on a scratch mount point, outside `~/gdrive`:

```
rclone mount gdrive:homedrive-desktop-test <scratch>/mnt \
  --vfs-cache-mode full --vfs-cache-max-size 50M --vfs-cache-max-age 24h \
  --vfs-cache-poll-interval 10s --poll-interval 15s --dir-cache-time 1h \
  --cache-dir <scratch>/cache --rc --rc-addr 127.0.0.1:5599 --rc-no-auth
```

- `rclone mount` on Linux is `cmd/mount`, which uses `bazil.org/fuse` (pure Go).
  `mount2` (`hanwen/go-fuse`), which section 5 recommends, was **not**
  exercised live. Phase D1 re-runs this matrix on it.
- **Offline simulation without touching the host network:** the second mount ran
  with `HTTPS_PROXY` pointing at a throwaway local CONNECT proxy. Killing that
  proxy cut **only the mount's** traffic (a probe through it then got ECONNREFUSED).
  The live agent was never affected. That mount also had `--contimeout 5s
  --timeout 20s --low-level-retries 2 --retries 1`.

### 3.2 Results

All timings are wall-clock, single samples unless stated, measured 2026-11-15
12:34–19:01 CET on a residential link.

| What | Result |
|---|---|
| First listing of the root (60 entries) | 0.115 s; warm 0.018 s |
| First recursive walk (3 dirs, 62 files) | 0.50 s; warm `find` 0.015 s |
| Open to first byte, uncached 20 MiB file | 0.96 s |
| Full read, uncached 20 MiB file | 3.34 s (about 6 MiB/s including open) |
| Full read, cached 20 MiB file | 0.047 s |
| Small file, uncached / cached | 0.60–0.66 s / 0.014 s |
| Remote change visible in the mount (`--poll-interval 15s`) | 3 s once, then 8.3, 8.8, 8.3, 8.8 s (new file created on Drive by another client) |
| Content of a cached file changed on Drive | new content readable within the same 3 s window |
| Local write visible on Drive | 8 s (default `--vfs-write-back 5s` plus upload) |
| Cache survives unmount and remount | yes: cached files were read at cached speed after remount |

**Eviction under the 50 MiB cap**

- Read `big2`, `big3`, `big1` (again), `big4`. After the next cache poll the cache
  kept `big1` and `big4` and evicted `big2` then `big3`, oldest last access
  first. **Eviction is LRU by access, not by download order.**
- **The cap is soft.** It is only enforced at each `--vfs-cache-poll-interval` tick:
  on disk the cache reached 60 MiB against a 50 MiB cap before the tick removed
  20 MiB. Overshoot is bounded by what is read in one poll interval, plus any file
  that is open (in-use items are never evicted).
- **`touch` does not count as access.** After `touch big1.bin`, `big1` was the next
  file evicted. `stat` does not count either. Confirmed in source:
  `vfs/vfscache/item.go` sets `ATime` only in `open`, `Close` and `readAt`.
  A write counts (it opens and closes the item); a metadata-only update does not.

**Offline** (proxy killed, so no route to Google)

| What | Result |
|---|---|
| List a directory already listed online | 0.019 s, served from the dir cache |
| List a directory **never listed** online (`pinme/sub`) | **blocked 238 s**, killed by `timeout` (rc 124); no error returned in that time |
| Read a cached file | 0.017–0.058 s |
| Read an uncached file (3 files: 20 MiB, 20 MiB, 9 B) | **blocked 150 s each**, killed by `timeout`; rclone logged the download failure only minutes later |
| Create a new file | 0.014 s, readable back at once, queued as a dirty upload |
| Append to a cached file | 0.014 s, queued as a dirty upload |
| `mkdir` / rename / delete | **blocked 207 s / 543 s / 296 s**, killed by `timeout`; rclone logged `Dir.Mkdir failed` and `File.Rename error`. These are **not queued**: they need the remote synchronously |
| Back online (after about 6 h offline) | Queued uploads resumed **only on their own retry timer** (backoff capped at 5 min; tries #48 to #61): the four dirty files reached Drive 1.3 to 5 min after the network returned. Nothing triggers an immediate retry |
| Dirty local file vs the same file edited on Drive meanwhile | rclone uploaded the local version over the newer remote one (`Copied (replaced existing)`), with no check and no copy kept, then saw the remote change and dropped its cache entry (`removed cache file as stale`). See 3.4 for what happened next on Drive |

The offline mount stayed up about 6 h (12:43 to 18:55) because the study session
was interrupted while it was offline. Everything above was measured between
12:34 and 19:01 CET.

### 3.4 Unplanned finding: another homedrive instance syncs the Drive root

Seconds after the upload at 12:34, **every file in the scratch folder had a
`<name>.old.1` twin on Drive** with the original's mtime (`btime` 12:34:38, owner
the maintainer's account). Later edits got one too (`newremote.txt.old.1`,
`prop1..4.txt.old.1`, `localwrite.txt.old.1`). rclone never creates `.old.<N>`
names; homedrive's `newer_wins` does. This host's live agent syncs only
`gdrive:Brain` and logged nothing about the folder. **Some other homedrive
instance is syncing the Drive root** and creates a conflict copy for files that
were simply created remotely. A read-only SSH check of the NAS was refused by
the session's permission policy, so **which host it is was not established.**

It also changed the conflict test: after rclone overwrote the remote `s10.txt`
with the older local edit, the final state on Drive was `s10.txt` = the newer
remote edit and `s10.txt.old.1` = the local edit (23 B, mtime 12:51). The earlier
9-byte `s10.txt.old.1` was **replaced**, not renumbered to `.old.2`. That is the
other agent's behaviour, not rclone's, and it is filed as #146
because it may bear on #83 (deep `.old.1` chains). It also means **a mount and a
root-syncing homedrive on the same Drive will fight**, which section 10 must
account for.

### 3.3 What the trial says

`rclone mount` already does most of the goal: full listing, download on access, a
size-capped LRU cache that survives restarts, write-back, and Drive-change polling
in under 10 s. It does **not** do:

1. **Pinning.** No API to exclude files from eviction.
2. **Touch-as-access.** Metadata updates do not refresh LRU position.
3. **A hard cap.** It is a periodic soft cap.
4. **Fail-fast offline.** Uncached reads, listing an unlisted folder, `mkdir`,
   rename and delete all block for 2.5 to 9 minutes instead of returning `EIO`
   quickly, even with short `--contimeout`/`--timeout`; a file manager browsing an
   unlisted folder freezes. Back online, queued uploads wait up to 5 min for
   their own backoff timer.
5. **Conflict preservation.** When a cached file is dirty and Drive changed
   meanwhile, rclone keeps the local version and overwrites the remote one
   (`item.go:925`, "remote object has changed but local object modified -
   keeping it"). Drive keeps revisions, but there is no `.old.<N>` and no event.
6. **Observability.** Only rclone's own `rc` HTTP (`vfs/stats`: cache bytes, files,
   uploads queued and in progress). No MQTT, no Home Assistant discovery, no
   journal.
7. **Session lifecycle.** A unit is needed either way (section 9).

## 4. Options

| | Option | What it is | Covers |
|---|---|---|---|
| **O0** | Supported `rclone mount` unit plus docs | A `homedrive-desktop.service` user unit and a page in `homedrive/docs/` that runs a pinned rclone release with tested flags. No Go code. | Everything in 3.3 except 1–6. Pinning approximated by a timer that `cat`s pinned folders (fragile, see 8.2). |
| **O1** | Desktop mode in Go, rclone VFS as a library, **no rclone fork** | A homedrive binary/mode that builds an `fs.Fs` for Drive, **wraps it** in a homedrive `Fs` (bounded timeouts, conflict check, events), hands it to `vfs.New` and mounts it with `cmd/mount2`. Pinning by keeping pinned items recently used. | 1 (soft, see 8.2), 4, 5, 6, 7. Not 2 or 3. |
| **O2** | O1 plus a small upstream rclone change | Contribute an eviction filter (`vfscommon.Options.CacheKeep func(name string) bool` or similar) and touch-as-access to rclone. Until it lands, carry it as a `replace` against a fork. | All of 1–7. |
| **O3** | Own FUSE layer and own cache, no rclone VFS | Write the VFS and cache from scratch over `go-fuse`. | Everything, at several thousand lines; reimplements what rclone already does well. **Not recommended.** |

**Recommendation: O1, with O2's upstream patch attempted in parallel and adopted
only if it is accepted upstream.** Do not fork rclone long-term. If the maintainer
would rather spend nothing yet, **O0 is a real option**: it delivers on-demand
access and an LRU cache today, and the trial shows how it fails (no pinning,
freezes offline, silent remote overwrite). Ship O0's unit and docs as Phase D0
either way; it is the fallback and the baseline everything else is compared to.

## 5. rclone packages and the FUSE library

Desktop mode needs, beyond what NAS mode already imports:

| Package | Why |
|---|---|
| `github.com/rclone/rclone/vfs` | The VFS: directory cache, file handles, write-back |
| `github.com/rclone/rclone/vfs/vfscommon` | VFS options (cache mode, max size, max age, poll interval) |
| `github.com/rclone/rclone/cmd/mountlib` | Mount point lifecycle, options, unmount on signal |
| `github.com/rclone/rclone/cmd/mount2` | FUSE binding via `hanwen/go-fuse/v2` (registers with `mountlib`) |

`vfs/vfscache` stays **internal to `vfs`**; homedrive reads cache state through
`vfs.VFS.Stats()` rather than importing it.

Importing `cmd/mount2` also links rclone's `cmd` package (its cobra flag registry).
That cost is inside the measured sizes below.

**FUSE library choice**

| rclone package | Library | cgo | Build/packaging impact |
|---|---|---|---|
| `cmd/mount2` (**recommended**) | `hanwen/go-fuse/v2` v2.11.0 | **no** | `CGO_ENABLED=0` cross-compiles to arm64 as today. Runtime needs the `fusermount3` setuid helper (package `fuse3`, present on this host). |
| `cmd/mount` | `bazil.org/fuse` | no | Same as above, uses `fusermount`. It is what `rclone mount` runs on Linux and what the trial exercised. The library itself sees little upstream activity. |
| `cmd/cmount` | `winfsp/cgofuse` + libfuse | **yes** | Needs `libfuse-dev` headers at build time (**not measured here**: `fuse.h` missing on this host) and a cross C toolchain for arm64 (not installed). Breaks the current pure-Go goreleaser build. **Rejected.** |

Recommend `mount2`: pure Go, keeps the cross-compiling release pipeline as it is,
and `go-fuse` is actively maintained. **Caveat:** the trial exercised `cmd/mount`,
not `mount2`. Phase D1's first deliverable is the same measurement matrix on
`mount2`, and falling back to `cmd/mount` costs one import line.

## 6. Proposed rclone allow-list

Today (`AGENTS.md`, `homedrive-rclone-import` skill) the whole module may import:

```go
_ "github.com/rclone/rclone/backend/drive"
"github.com/rclone/rclone/fs"
"github.com/rclone/rclone/fs/config/configfile"
"github.com/rclone/rclone/fs/operations"
"github.com/rclone/rclone/fs/sync"
```

Proposed: keep that list for NAS code, and allow **only the desktop code paths**
(the package `internal/desktop` and the `mount` subcommand wiring) to
add:

```go
"github.com/rclone/rclone/vfs"
"github.com/rclone/rclone/vfs/vfscommon"
"github.com/rclone/rclone/cmd/mountlib"
_ "github.com/rclone/rclone/cmd/mount2"
"github.com/rclone/rclone/fs/rc"   // vfs.Stats() returns rc.Params; in-process vfs/queue-set-expiry
```

`fs/rc` is already linked by `vfs` (no size cost); listing it makes the
in-process rc calls of section 8.4 explicit. The rc **HTTP server**
(`fs/rc/rcserver`) is **not** on the list: homedrive's own HTTP port serves status.

The `internal/rcloneclient`-only rule becomes "`internal/rcloneclient` and
`internal/desktop`". The CI check gains a second grep: any import of the four
desktop packages outside `internal/desktop` and the `mount` subcommand fails.
Still exactly one backend (`backend/drive`). If #86 lands first (drop
`configfile`), the desktop list follows it.

## 7. Binary architecture: two options, measured

Both options keep the code in the one existing Go module. Sizes are stripped
(`-trimpath -ldflags="-s -w"`), `CGO_ENABLED=0`, rclone v1.76.0, current `main`
plus size probes that keep the VFS and `mount2` code reachable (checked with
`go tool nm`: 1,111 `go-fuse`/`vfs` symbols linked, 0 `watcher`/`syncer` symbols
in the desktop-only probe). Measured 2026-11-15.

| Binary | linux/amd64 | linux/arm64 | vs today |
|---|---|---|---|
| `homedrive` today (NAS) | 24,363,168 B | 22,610,080 B | baseline |
| **(a)** `homedrive` NAS, unchanged | 24,363,168 B | 22,610,080 B | 0 |
| **(a)** `homedrive-desktop` (config, rcloneclient, store, mqtt, http metrics, oldsuffix, vfs, mount2; no watcher/syncer) | 25,768,096 B | 23,920,800 B | +1.40 / +1.31 MB |
| **(b)** single `homedrive` with desktop mode, `mount2` | 26,292,384 B | 24,379,552 B | **+1.93 / +1.77 MB** |
| (b) same with `cmd/mount` (bazil) instead | 26,222,752 B | 24,314,016 B | +1.86 / +1.70 MB |
| (b) same with `cmount` (cgo) | not measured (no `fuse.h`) | not measured (no cross toolchain) | |

The 25 MB guard these figures were measured against was removed by #149: there
is no size limit any more, only the rule that homedrive must run on a Raspberry
Pi 3B+ alongside other workloads (AGENTS.md "Target hardware").

| | (a) Dual executables | (b) Single executable, two modes |
|---|---|---|
| Shape | `cmd/homedrive` (NAS) + `cmd/homedrive-desktop` | `homedrive run` (NAS) + `homedrive mount` (desktop) |
| cgo | no | no |
| NAS binary | unchanged, keeps its guard | carries FUSE code it never runs (+1.9 MB) |
| Packaging | goreleaser: second `builds:` entry; either one `.deb` with both binaries or a second `homedrive-desktop` `.deb` that `Depends: fuse3`. The NAS `.deb` does not need `fuse3`. | One build; the `.deb` gains `Recommends: fuse3` (NAS installs skip it with `--no-install-recommends`) |
| Config | `~/.config/homedrive/desktop.yaml` (own schema) | same file split by mode, or a second file; one loader with a `mode:` key |
| systemd | `homedrive.service`/`homedrive@.service` (NAS, unchanged) + `homedrive-desktop.service` (session-bound) | same two units, both running `homedrive` with different subcommands |
| Test/CI | Two build targets, two size checks, desktop FUSE tests need `/dev/fuse` (Linux runners have it; `-short` skip elsewhere). NAS tests untouched. | One build, one size check at a raised budget; desktop tests as in (a) |
| Code sharing | Shared `internal/` packages; each `cmd/` wires its own | Shared `internal/`; `main` dispatches by subcommand |
| Allow-list enforcement | Per directory, mechanical (grep) | Per package inside one binary; the NAS binary still links the desktop packages |
| Risk to NAS | None at runtime or size | NAS process links FUSE and VFS init code; `init()`s from `cmd/mount2`/rclone `cmd` run in the NAS process too |

**Decision (maintainer, 2026-11-15): (b), a single executable with two modes.**
The dual-binary saving is about 0.5 MB, and the size guard that motivated (a) is
gone. One build, one `.deb`, one config loader, no duplicated wiring. The NAS
process links the FUSE/VFS packages but never initialises a mount; D1 must check
that no rclone `cmd`/`mount2` `init()` does work at NAS start-up (memory, flags)
and record the arm64 RSS of NAS mode before and after on a Pi 3B+-class host.

## 8. Design

### 8.1 Runtime topology

```
homedrive mount (desktop mode of the single binary, started by the graphical session)
  mount2 (go-fuse) ──▶ rclone vfs.VFS (dir cache, LRU file cache, write-back)
                          │
                          ▼
                     desktop.GuardFs  (homedrive's fs.Fs wrapper)
                       - short remote timeouts: EIO instead of minute-long hangs
                       - conflict check before Update/Put (newer_wins, .old.<N>)
                       - events to MQTT + audit log + store journal
                          │
                          ▼
                     backend/drive fs.Fs (rclone.conf via configfile)
  pinner     ── walks pinned folders, keeps them listed and recently read
  http :6091 ── /status /healthz /metrics /pin /unpin
  mqtt ── homedrive/<host>/<user>/desktop/... (section 8.6)
```

The `GuardFs` wrapper is the key seam. rclone's VFS calls the `fs.Fs` and
`fs.Object` interfaces for every remote operation, so wrapping the Drive `Fs`
gives homedrive one place to observe and veto remote I/O without forking rclone.

### 8.2 Pinning

Config: `pins: ["Documents/Taxes", "Projects/current"]`, also `homedrive
pin <path>` / `unpin <path>` over the HTTP control port, persisted in the store.

**O1 (no fork): keep pinned content warm.**

- After each cache-clean tick (the VFS poll interval) and after every Drive change
  under a pinned folder, the pinner walks the folder through the VFS and
  **opens and reads one byte** of each pinned file. That makes the item resident
  and refreshes `ATime`, so unpinned items are always evicted first.
- It keeps pinned folders' directory listings warm (`--dir-cache-time` long, plus
  a `vfs` refresh) so they **list offline** (the trial's 238 s hang).
- Invariant: **the pinned set must fit in the cap with headroom.** The pinner
  computes the pinned total from the listing and refuses a pin (HTTP 409, MQTT
  `pin.rejected`) when pinned bytes exceed, say, 80% of the cap. Without that,
  LRU would evict pinned files under pressure.
- Weakness: it is "pinned by being recently used", not a guarantee. A burst of
  large reads between two ticks can still evict a pinned file (the cap is soft,
  section 3.2), and the next tick re-downloads it. Reading one byte of a sparse
  cache file does not download the whole file either: the pinner must read each
  file fully once after a change (rclone caches by ranges).

**O2 (upstream patch): a real keep-set.** Add to `vfscommon.Options` a predicate
consulted in `purgeOverQuota`/`purgeOld` and make `Setattr`-mtime refresh `ATime`.
That is two small changes in `vfs/vfscache/cache.go` and `item.go`; whether
upstream accepts it is unknown. Pinned bytes are then excluded from the cap and
the 80% rule becomes a separate `pinned_max` budget.

O0's equivalent (a timer that runs `cat` over pinned folders) has O1's weakness
plus no cap check and no offline listing guarantee.

### 8.3 Cache cap and eviction

- `cache.max_size`: default **25% of the size of the filesystem holding the cache
  dir** (256 GiB on the current desktop host, 1 TiB disk), overridable with an
  absolute size (decision, 2026-11-15). `cache.max_age` (default 0: no age limit;
  rclone's default of 1h would evict everything hourly), `cache.min_free_space`
  (default 5 GiB; rclone's `--vfs-cache-min-free-space`), cache dir
  `$XDG_CACHE_HOME/homedrive-desktop`.
- `cache.poll_interval` 10 s: the soft-cap overshoot is bounded by one interval
  of reads; 10 s keeps it small.
- LRU by **open/read/close/write** is rclone's behaviour; **touch is not**.
  Accepted by the maintainer (2026-11-15), so O2 is not needed for eviction.
- Dirty (not yet uploaded) items are never evicted (rclone guarantees this).

### 8.4 Offline: not supported (decision, 2026-11-15)

A Linux desktop is treated as **always online**. There is no offline mode, no
connectivity probe and no queueing of metadata operations:

- Reads of cached content are served by the VFS as usual.
- Any operation that needs Drive while it is unreachable (read of uncached
  content, listing an uncached directory, `mkdir`, rename, delete) returns
  **`EIO`**. The trial showed rclone blocking 150 to 543 s instead, so desktop
  mode bounds remote calls with short timeouts and low retry counts (applied in
  `GuardFs` or via rclone's `--contimeout`/`--timeout`/`--low-level-retries`
  equivalents) so the error comes back in seconds, not minutes.
- Writes to already-open or cached files may still be queued as dirty uploads by
  the VFS; they are uploaded when Drive is reachable again and go through the
  conflict check of section 8.5, which closes the trial's "offline edit
  overwrote a newer Drive version" finding.

### 8.5 Conflicts: newer_wins and `.old.<N>`

Same policy and naming as NAS mode (PLAN.md §11), reusing `internal/oldsuffix`
and the store journal:

- On download, the journal records `{path, remote_id, remote_mtime, remote_md5}`.
- Before the VFS uploads a dirty file (`GuardFs` `Put`/`Object.Update`),
  `GuardFs` stats the remote. If its mtime/MD5 differ from the journal, it is a
  conflict:
  - local newer: server-side move the remote to `<path>.old.<N>` on Drive, then
    upload (loser kept on the remote side, as in NAS mode);
  - remote newer: upload the local content as `<path>.old.<N>` on Drive instead,
    and let the VFS re-fetch the remote version. Both versions are then on Drive
    and visible in the mount (desktop mode has no separate "local side": the cache
    is not a sync root).
- Emits `conflict.detected` / `conflict.resolved` like NAS mode.

This replaces rclone's silent "keep local, overwrite remote".

### 8.6 Observability

- MQTT via the existing `internal/mqtt` wrapper, HA discovery, LWT. Base topic
  `homedrive/<host>/<user>/desktop/`. NAS mode and desktop mode can run on the
  **same host as the same user** (this host does), so they must not share
  `homedrive/<host>/<user>/` or the HA device id; desktop uses device id
  `homedrive_<host>_<user>_desktop`.
- Entities: `status` (`mounted`, `offline`, `error`), `cache_used_bytes`,
  `cache_cap_bytes`, `pinned_bytes`, `uploads_queued`, `uploads_in_progress`,
  `conflicts_24h`, `last_remote_change`, `online`. Values come from
  `vfs.VFS.Stats()` (the same data as the trial's `vfs/stats`).
- Events: `conflict.*`, `pin.added`, `pin.removed`, `pin.rejected`,
  `cache.evicted` (rate-limited), `offline`, `online`, `oauth.refresh_failed`.
- HTTP control on `127.0.0.1:6091` (NAS mode owns 6090).
- slog JSON to the journal (`journalctl --user -u homedrive-desktop`).

### 8.7 What is reused and what is not

| Package | Desktop mode |
|---|---|
| `internal/rcloneclient` | **Reused** for building the Drive `fs.Fs` from `rclone.conf`, OAuth health and `Quota`. Its `RemoteFS` interface is not what the VFS needs (the VFS takes an `fs.Fs`), so `GuardFs` wraps the raw `fs.Fs`, and `rcloneclient` exposes a constructor for it. |
| `internal/store` | **Reused**: journal of remote fingerprints for conflict detection, pins, `.old.<N>` index, auditor. |
| `internal/oldsuffix` | **Reused** as is. |
| `internal/mqtt` | **Reused** (publisher, HA discovery, LWT), new entity set. |
| `internal/http` | **Reused** (server, metrics), new handlers. |
| `internal/config` | Partly: a new `desktop` section or file. |
| `internal/quota` | **Reused** (quota alerts). |
| `internal/pathfilter` | Optional: exclude patterns hidden from the mount. |
| `internal/watcher` | **Not used**: the FUSE layer sees every write; there is nothing to watch. |
| `internal/syncer` (push queue, puller, bisync) | **Not used**: rclone's VFS write-back and change polling replace them. |

## 9. Session lifecycle

### 9.1 systemd user unit bound to the graphical session (recommended)

```ini
[Unit]
Description=homedrive desktop mode (Google Drive on demand)
PartOf=graphical-session.target
After=graphical-session.target
Requisite=graphical-session.target

[Service]
Type=notify            # mountlib can notify readiness after the mount is live
ExecStart=%h/.local/bin/homedrive mount
ExecStop=             # none: SIGTERM triggers flush then unmount (9.3)
TimeoutStopSec=45
Restart=on-failure
RestartSec=10

[Install]
WantedBy=graphical-session.target
```

- `WantedBy=graphical-session.target` starts it when GNOME brings the session up;
  `PartOf=` stops it when the session target stops at logout.
- **Linger does not need to be off.** This host keeps `Linger=yes` for the NAS
  unit. Linger keeps the user manager alive after logout, but
  `graphical-session.target` still stops at logout, and `PartOf=` stops the
  desktop unit with it. What must **not** happen is `WantedBy=default.target`
  (that is what NAS mode uses, and with linger it would mount at boot without a
  session).
- Checked on this host: GNOME 50.1 runs its session through systemd user units
  (`gnome-session@ubuntu.target` and `graphical-session.target` are active), so
  this works here. KDE Plasma (6.x) and recent GNOME both do this; some lighter
  desktops do not start `graphical-session.target`.

### 9.2 XDG autostart `.desktop` entry (alternative)

`~/.config/autostart/homedrive-desktop.desktop` with `Exec=homedrive
mount`. On this host systemd's `systemd-xdg-autostart-generator` turns it into an
`app-homedrive\x2ddesktop@autostart.service` unit anyway. It works on desktops that
do not start `graphical-session.target`, but gives no `Restart=`, no
`TimeoutStopSec`, and less control over stop ordering at logout.
**Recommend the systemd unit; ship the `.desktop` file only as a documented
fallback** for desktops without `graphical-session.target`.

### 9.3 Logout, suspend, resume

- **Logout:** SIGTERM. The process stops accepting new opens, waits for
  in-progress uploads and for dirty items to upload for up to ~30 s (inside
  `TimeoutStopSec=45`), then unmounts. If the mount is busy (`EBUSY`, a file still
  open), it falls back to a **lazy unmount** (`fusermount3 -u -z`). Dirty items not
  uploaded by then **stay in the cache** and rclone re-queues them at the next
  start (verified: the cache survived unmount/remount in the trial; that dirty
  items re-upload after a restart is rclone's documented behaviour, **not
  measured here**, and Phase D1 must measure it).
- **Open files at logout:** GNOME stops applications before
  `graphical-session.target` stops, so normally no handle remains. A process that
  still holds a file gets `ENOTCONN` on its next I/O after the lazy unmount; its
  unsaved data is lost, as with any FUSE filesystem going away. Data it already
  wrote is in the cache and uploads at the next start.
- **Suspend:** no unmount. The risk is the kernel freezer: a task blocked in a FUSE
  request can make freezing fail if the FUSE daemon is frozen first. Whether
  systemd 259's user-session freezing interacts badly with a FUSE daemon in the
  same user session is **not established** (no suspend was tried during this
  study). Mitigation if it does: a `systemd-inhibit --what=sleep --mode=delay`
  lock taken by the daemon, released after it flushes.
  Phase D3 must test suspend/resume on this host.
- **Resume without network:** cached files keep working, uncached access returns
  `EIO` after the bounded timeouts (8.4).
- **Resume with network:** the token is refreshed on the first call, Drive change
  polling resumes from its page token.

### 9.4 Desktop integration (optional, later)

A Nautilus emblem or a `gio` attribute for pinned/cached state needs a Nautilus
extension (Python, GObject). Not proposed for the first phases.

## 10. Coexistence with NAS mode on the same host

| | NAS mode (live today) | Desktop mode |
|---|---|---|
| Unit | `homedrive.service`, `WantedBy=default.target`, lingering | `homedrive-desktop.service`, `WantedBy=graphical-session.target` |
| Config | `~/.config/homedrive/config.yaml` | `~/.config/homedrive/desktop.yaml` |
| State | NAS state dir | `$XDG_STATE_HOME/homedrive-desktop/` |
| Cache | none | `$XDG_CACHE_HOME/homedrive-desktop/` |
| Local path | `local_root` (`~/gdrive`) | mount point, e.g. `~/GoogleDrive` |
| HTTP | 6090 | 6091 |
| MQTT | `homedrive/<host>/<user>/` | `homedrive/<host>/<user>/desktop/` |

Rules the desktop binary enforces at startup:

- The mount point must not be, contain, or sit inside any NAS-mode `local_root`
  it can find in `~/.config/homedrive/config.yaml`.
- **`rclone.conf` is a shared writer.** Both processes refresh the OAuth token and
  rclone writes the refreshed token back to the file. Two writers to one
  `rclone.conf` is a race that NAS mode has never had. Options: desktop mode uses
  its own `rclone.conf` (a second `rclone config` run), or #86 lands first (no
  `rclone.conf`). The trial avoided this by using a copy. **Decision needed.**
- **Overlap in Drive.** If desktop mode mounts the Drive root and NAS mode syncs
  `gdrive:Brain`, `Brain` is reachable both ways on one host: an edit through the
  mount is pulled into `~/gdrive` by the NAS agent within its poll interval,
  and vice versa. That is correct but doubles disk use for `Brain`. Excluding the
  NAS mode's remote from the mount is an option.

Whether desktop mode is meant to **replace** NAS mode on this host, or run beside
it, is a question for the maintainer (below). The design supports both.

## 11. Proposed phases (PROPOSED, for `homedrive/PLAN.md` §14)

Numbered `D0`–`D5` to avoid colliding with the Dropbox proposal (#137, phases
17–22); renumber at merge.

- **D0: supported `rclone mount` unit + docs (O0).** `homedrive/linux/
  homedrive-desktop-rclone.service` (session-bound, flags from section 3), a docs
  page with the trial's findings and known limits. No Go. About 150 lines.
  Useful alone; the baseline for everything below.
- **D1: desktop mode skeleton.** `homedrive mount` subcommand (option b), config, `vfs` + `mount2`, signal handling and flush-then-
  unmount, `Type=notify`. Re-run the section 3 matrix on `mount2`, including
  dirty re-upload after restart. Allow-list and size checks per section 6/7.
- **D2: `GuardFs` + bounded timeouts.** `EIO` within seconds when Drive is
  unreachable (no offline mode, section 8.4); tests with a fake `fs.Fs` (no
  Google calls, per `homedrive-test-mocks`).
- **D3: session unit, suspend/resume.** `homedrive-desktop.service` per 9.1,
  `.desktop` fallback, measured logout and suspend/resume behaviour on this host.
- **D4: conflicts.** Journal fingerprints, `newer_wins` with `.old.<N>` in
  `GuardFs`, events. Mandatory tests mirroring PLAN.md §16.3's conflict cases.
- **D5: pinning + observability.** Pinner, pin API and persistence, 80% rule,
  MQTT entities and HA discovery under the `desktop/` base. Upstream rclone
  keep-set patch (O2) proposed in parallel; adopted only if merged upstream.

Rough size: D1–D5 about 2,000–2,500 lines including tests, five PRs. D0 alone is
one small PR.

## 12. Decisions and open questions

Answered by the maintainer on 2026-11-15:

- **Binary shape (was Q2):** (b), single executable with two modes (section 7).
- **Size budgets (was Q3):** no size limit; must run on a Raspberry Pi 3B+
  alongside other workloads (#149).
- **Touch (was Q4):** LRU by open/read/write is enough.
- **Default cap (was Q9, part):** 25% of the cache filesystem (256 GiB here).
- **Offline (was Q10):** none; a desktop is always online, `EIO` for uncached
  content when Drive is unreachable (section 8.4).
- **Which host made the `.old.1` copies (was Q11):** the NAS agent, a newer_wins
  false positive tracked in #146; desktop mode does not cause it.
- **Alerting:** homedrive only publishes over MQTT; home-automation does any
  alerting.

Still open:

1. **Go/no-go and depth:** O0 only (unit + docs), O1 (D0–D5), or O1 plus the
   upstream rclone patch (O2)?
2. **Pinning guarantee:** is "pinned set must fit in 80% of the cap, best-effort
   re-warm" acceptable for a first version, or is a hard keep-set required (O2)?
3. **This host:** does desktop mode **replace** the NAS-mode agent on this
   desktop, or run **beside** it? If beside: mount the whole Drive, or exclude
   `Brain` (already synced to `~/gdrive`)?
4. **rclone.conf:** a separate `rclone.conf` for desktop mode, or wait for #86?
5. **Conflict side:** in desktop mode both versions end up on Drive (section
   8.5). Is that the intended reading of `newer_wins` here?
6. **Mount point and min free space:** `~/GoogleDrive` and 5 GiB?
