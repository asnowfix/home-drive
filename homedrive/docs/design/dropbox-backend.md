# Design: Dropbox as a second sync backend

- **Status**: Proposed (not approved). **Date**: 2026-11-14.
- **Inputs**: `homedrive/docs/dropbox-backend-research.md` (cited as *research*, with its section numbers),
  `homedrive/PLAN.md`, `homedrive/docs/architecture.md`, `AGENTS.md`, the `homedrive-*` skills, and the code at
  `b8b425d`.
- **Reference caveat**: the repo has no `docs/architecture/ARCHITECTURE.md`. PLAN.md and `architecture.md`
  disagree with the code in places (see §15). Where they disagree, this design follows the code. A
  `principal-documenter` refresh would help.
- **Evidence labels**: **[code]** means verified by reading the code. **[inferred]** means my reasoning; each one
  says what it rests on. **[U#]** means it depends on an open unknown from the register in §16. A design
  choice that rests on a [U#] is built to work under either answer, or says that it is not.

## 1. The ask

Add Dropbox as a second backend so homedrive can replace Maestral on the NAS. The sync is local directory to
Dropbox, in both directions, with the same semantics as Drive: local is the source of truth, newer-wins
conflicts keep the loser as `.old.<N>`, the journal prevents loops, pushes go watcher -> debouncer -> queue,
and pulls use a change feed plus a periodic safety net. This is not a data migration.

## 2. Context: what is Drive-specific today [code]

Wiring: `cmd/homedrive/agent.go:newAgent` builds one `store.Journal`, one `rcloneclient.RcloneFS`, one
watcher, one push `syncer.Syncer`, one `syncer.Puller` and one `syncer.Bisync`. They share one `bisyncMu`, one
HTTP server and one MQTT client. Everything below the agent depends on the `rcloneclient.RemoteFS`
interface (9 methods in `remotefs.go`). `MemFS`, `FlakyFS` and `DryRunFS` implement it.

The production pull path is `agent_run.go:runPullLoop` -> `pollOnceGuarded` -> `Puller.PollOnce` -> `poll`.
`poll` calls `ensurePageToken` (`RemoteFS.GetStartPageToken`) and then `fetchChanges` (`RemoteFS.ListChanges`).
On `errors.Is(err, ErrGone)`, `fetchChanges` mints a new token and retries. `RcloneFS.ListChanges` treats a
token with the `initial_sync:` prefix as "full walk first" (`fullWalkThenResume`). The full walk is
`listRecursive` over rclone `fs.List`. `Puller.Run` is **not** on this path. Any change to how changes
arrive must go through `PollOnce`, and its tests must drive `PollOnce` or `runPullLoop`.

Drive-specific pieces, extending the brief's list:

| Piece | Where | Why it is Drive-only |
|---|---|---|
| `*drive.Fs` type assertion | `rclonefs.go:158` | rejects any other backend |
| Changes API client, token from rclone.conf | `driveapi.go` (`driveService`, `pollChanges`, `translateChange`, `isBadPageTokenErr`) | Drive REST |
| Root folder ID, ID-to-path cache | `rootid.go`, `idpathcache.go`; `listRecursive` seeds the cache | Drive reports parent IDs, not paths |
| OAuth endpoint and scope | `driveapi.go:buildOAuthConfig` (`google.Endpoint`, `DriveScope`) | Google token endpoint |
| Startup forced refresh | `oauthvalidate.go` (`forceOAuthRefresh` is generic; messages say "Google", cite `oauth-troubleshooting.md`) | provider text |
| #67 "no client_id" classification | `oauthstatus.go:isOAuthClientMissingErr`, `Puller.oauthMissingStreak` | Google `invalid_request` behaviour |
| Dangling shortcut | `rclonefs.go:DownloadFile` mime check | Drive shortcuts. Harmless elsewhere (never matches) |
| MD5 everywhere | `RemoteObject.MD5`, `JournalEntry.RemoteMD5` (JSON `remote_md5`), `store.ConflictInput`, `push_journal.go:fileMD5`, `bisync_ops.go:hasDivergence` | Dropbox has `content_hash`, not MD5 (research summary #2) |
| Folder skip | `pull_process.go`, `pull_dir.go` (`IsDir` only set by the Drive feed) | generic. The Dropbox feed must set `IsDir` |
| HA entity text | `mqtt/discovery.go` "Drive Quota Used", `mdi:google-drive` | cosmetic |
| Discovery IDs | `mqtt/discovery.go` `homedrive_<host>_<user>_<key>` | collide if two instances run as one user (§6.1) |

Backend-neutral already [code]: `CopyFile` (`operations.Rcat`), `DeleteFile`, `MoveFile`, `Stat`, `List`,
`DownloadFile`, `Quota` (`fs.Abouter`), the `initial_sync:` and `ErrGone` recovery contract, the exclude filter
(`pathfilter`), `DryRunFS`, and all of `syncer`/`store` except the hash.

## 3. Goals and non-goals

**Goals**
1. A homedrive process whose rclone remote has `type = dropbox` syncs with the same push, pull, conflict and
   loop-prevention semantics as Drive.
2. The Dropbox pull is an incremental, persistable change feed that recovers from cursor loss with a full
   re-walk, like Drive's #64 recovery. It runs through `PollOnce`.
3. Existing Drive configs and Bolt databases keep working unchanged after the upgrade: no config edits, no
   data migration, and the same journal JSON.
4. Drive and Dropbox can run side by side for the same Unix user on the NAS.
5. A cutover from a fully synced Maestral folder does not re-download or overwrite the dataset.
6. Both release binaries stay under the 26,214,400-byte CI gate.

**Non-goals** (each would be nice, and each is excluded on purpose)
- **Several remotes in one process** (one config with a list of `pairs:`). PLAN §1 already lists multi-pair
  as out of scope. See §7 alt. B.
- **A homedrive-owned OAuth flow or secret store** (SDK PKCE or no-redirect helpers). This is #86's deferred
  "own the secrets" scope. Authorisation stays with `rclone config` / `rclone authorize`.
- **Long-poll (`list_folder/longpoll`) for sub-30 s pull latency.** Pull stays on the timer (§6.4).
- **Remote rename pairing by Dropbox `id`.** A server-side move becomes a local delete plus a download, not a
  local `mv` (§6.4, optional Phase 24).
- **Propagating case-only renames** in either direction.
- **Dropbox Business team spaces / `root_namespace`, shared-folder modes, Paper export editing.** Personal
  accounts only, and exports are skipped.
- **Honouring `.mignore` syntax or the `com.dropbox.ignored` xattr.** Both are unverified (research Q8). Users
  translate them into `watcher.exclude` by hand.
- **`rev`-conditional uploads** (`WriteMode.update(rev)`) to avoid lost updates. rclone always uses overwrite.
- **Importing Maestral's index or hash cache.**
- **Fixing the pre-existing Drive issues in §15.** They are flagged, not fixed here.

## 4. Quality drivers

1. **No silent loss of remote changes.** When the cursor is rejected (`reset`) or a poll errors, the next
   `PollOnce` either resumes from the persisted cursor or re-walks everything. No change made while the agent
   was down or erroring is skipped without a later full reconciliation. This is the reason rclone
   `ChangeNotify` is rejected (§7 alt. A).
2. **Upgrade safety for the live Drive agent.** After `make deploy-pi` with this work, the Drive agent
   starts against its existing `state.db`, polls with its existing page token, and produces byte-identical
   journal JSON for unchanged entries. Phase 17 must show zero behaviour change.
3. **Binary size (amd64 binds).** Measured with backend/dropbox imported: amd64 is 25,092,256 bytes (1.07 MiB
   headroom) and arm64 is 23,265,440 bytes (2.8 MiB). All new Dropbox code must fit in the amd64 headroom,
   measured with **CI's toolchain** (`setup-go 1.26`, not the go1.27.4 used for the measurement).
4. **Data safety on a case-insensitive remote.** Two local names that differ only in case never silently
   overwrite each other, either remotely or locally. Each such collision produces an error event and the
   sync of the affected paths stops; no data is removed.

## 5. Fit

Mostly as-is, plus one **small architectural increment** at the remote boundary and one at the
process/instance boundary:

- **Fits as-is**: push path, conflict algorithm, `.old.<N>`, retention, journal shape, the pull loop and its
  `ErrGone` recovery, `pathfilter`, the HTTP and MQTT code paths, and the `RemoteFS` public interface (no new
  methods).
- **Increment 1 (inside `rcloneclient`)**: an unexported `changeFeed` seam, so `RcloneFS` holds a Drive *or*
  Dropbox change source. `RemoteObject.MD5` and `JournalEntry.RemoteMD5` become a backend-typed "remote hash".
- **Increment 2 (deployment)**: run one process per backend, selected by a config *profile*, with MQTT
  discovery IDs that stay unique per instance.
- **Syncer**: three behaviour flags in the config structs, off by default and turned on by the agent for
  Dropbox (§6.6, §6.8). No new syncer types.

## 6. Proposed design

### 6.1 One backend per process, several instances per user

A process has exactly one `remote`. Running Drive and Dropbox together means running two processes. Each
has its own config file, `state.path`, `state.audit_log`, `http.listen`, `mqtt.client_id_prefix` and
`mqtt.base_topic`. The live-testing procedure already runs isolated instances this way
(`homedrive/docs/live-testing.md` step 2).

What collides, and how loudly [code, inferred from the code paths]:
- Same `state.path`: Bolt's 1 s open timeout fails loudly.
- Same `http.listen`: the bind error fails loudly.
- Same `audit_log`: **silent**, the lines interleave.
- Same MQTT client ID: **silent**, the broker drops one connection and the two flap.
- Same discovery IDs (`homedrive_<host>_<user>_<key>`, built from host and user only): **silent**, Home
  Assistant shows one device.

Changes:
- `cmd/homedrive/main.go`: add `--profile <name>` on `run` and `ctl`, with the env var `HOMEDRIVE_PROFILE` as
  its default. The config path becomes `<UserConfigDir>/homedrive/<name>.yaml`. An explicit `--config` still
  wins. With no profile, `defaultConfigPath()` is unchanged.
- `internal/config`: add an optional `mqtt.instance` (default `""`). `internal/mqtt/discovery.go`
  (`buildDeviceBlock`, `discoveryTopic`, `UniqueID`) inserts `_<instance>` only when it is set, so the existing
  HA entities keep their IDs.
- `homedrive/linux/homedrive-dropbox@.service`: a copy of `homedrive@.service` with
  `ExecStart=/usr/bin/homedrive run --profile %j` and an `ExecCondition` that tests for `.../homedrive/%j.yaml`.
  `%j` is "final component of the unit prefix", here `dropbox`. Users clone it as `homedrive-<profile>@.service`.
  **Verify that `%j` resolves on the NAS's systemd 252** [inferred from systemd.unit(5); not tested]. Ship a
  user-mode twin `homedrive-dropbox.service` the same way. Add `homedrive/linux/config-dropbox.yaml` with
  distinct `http.listen: 127.0.0.1:6091`, state paths, MQTT prefix/topic/instance and the Maestral excludes
  (§6.7).
- Shared per-user limits: `fs.inotify.max_user_watches=524288` counts both instances' watches together
  [inferred: inotify limits are per real UID].

### 6.2 Backend selection: inferred from rclone.conf, no new config key

`rcloneclient.DetectBackend(configPath, remote string) (Backend, error)` reads the `type` key of the remote's
rclone.conf section (`config.FileGetValue`, already used by `readOAuthConfig`). It returns `BackendDrive` or
`BackendDropbox`, or `ErrUnsupportedBackend`. Existing Drive configs need no edit, and the config cannot
disagree with rclone.conf. The agent logs `backend` at startup and adds it to `/status`. The agent then sets
the syncer flags (`CaseInsensitiveRemote`, `AdoptUnjournaled`, `ExpandDirDeletes`, `HashType`) from the
backend, so no new YAML is needed.

Dropbox-specific rclone options are overridden only when the user has not set them in rclone.conf. The
override goes through rclone's connection-string form when building the Fs (`name,chunk_size=8Mi,skip_exports=true:path`).
`remoteSectionName` still reads the original `cfg.Remote`. homedrive never writes rclone.conf.

### 6.3 The seam: `changeFeed` inside `rcloneclient`

```go
// unexported; one implementation per backend
type changeFeed interface {
    startToken(ctx context.Context) (string, error)                // raw token, no initial_sync: prefix
    poll(ctx context.Context, token string) (Changes, error)       // must wrap ErrGone when the token is unusable
    observeWalk(id, path string, isDir bool)                       // Drive seeds idPathCache; Dropbox no-op
    validateCredential(ctx context.Context) error                  // the #86/#97 forced refresh
    oauthStatus() OAuthStatus
}
```

- `RcloneFS` gains `feed changeFeed` and `backend Backend`. `GetStartPageToken`, `ListChanges`,
  `fullWalkThenResume` and `listRecursive` stay in `rclonefs.go`, are shared, and call the feed. The
  `initial_sync:` convention stays in one place.
- New `drivefeed.go` gets today's `driveService`, `pollChanges`, `translateChange`, `ensureRootID`,
  `pathCache`, `oauthHTTPClient` and #67 classification **moved, not rewritten**. The `*drive.Fs` assertion
  moves into `newDriveFeed`.
- `newRcloneFS(ctx, cfg, validate, buildFs)` keeps its order invariant: validate, then `buildFs`, then build
  the feed. The default `validate` becomes `r.feed.validateCredential`. The existing `TestNewRcloneFS_Ordering*`
  tests keep guarding it.
- `remoteObjectFromRclone(obj, hashType)` fills `Hash` from `obj.Hash(ctx, hashType)` when `hashType` is
  set. For Dropbox it passes `dropbox.DbHashType`, so no `fs/hash` import is needed. Drive keeps an empty
  hash on walk and upload paths to preserve behaviour (§15 item 5).
- `RemoteFS`, `MemFS`, `FlakyFS`, `DryRunFS` and `syncer` see **no interface change**. Capabilities the agent
  needs (`OAuthStatus`) keep the existing "optional interface declared at use" pattern (`agent_control.go:oauthStatusReporter`).

### 6.4 Dropbox change feed: own SDK client, `list_folder/continue`, timer-driven

`dropboxfeed.go` builds a `files.Client` from `github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox` and
`.../dropbox/files`. Its `dropbox.Config.Client` is an `oauth2.Config.Client(ctx, tok)` built from the
rclone.conf token, client_id and client_secret, against Dropbox's token URL. This mirrors `driveService` and
what rclone itself does (research Q1, option 1).
- `startToken`: `list_folder/get_latest_cursor` with `path` = the remote root (`""` or `/Sub`),
  `recursive=true` and **`include_deleted=true`**. Setting `true` is robust to both answers of [U1]. We never
  call `list_folder` with it, so it adds no noise.
- `poll(token)`: loop `list_folder/continue` while `has_more`. Return `Changes{Items, NextPageToken: lastCursor}`.
  - `FileMetadata` -> `Change{Path, Object{Size, ModTime: client_modified, Hash: content_hash, RemoteID: id}}`.
  - `FolderMetadata` -> `Object{IsDir: true}`. The puller already skips folders.
  - `DeletedMetadata` -> `Change{Path, Deleted: true}`.
  - Paths: drop the root's component count from `path_display`, then decode through rclone's Dropbox
    encoding (`lib/encoder`, §11). This keeps feed paths equal to the paths rclone's walk produces.
  - **Per-poll de-duplication [U3]**: ordering within and across pages is not guaranteed. Entries are keyed by
    case-folded path, and the last state wins only if every entry for that path agrees in kind. If one batch has
    both a deleted entry and a live entry for a path, the feed resolves it with one `Stat` (rclone
    `NewObject`) and reports the server's current state.
  - `ListFolderContinueAPIError` with tag `reset` -> `NewTokenRejectedErr(err)` (wraps `ErrGone`). The
    existing `fetchChanges` then re-mints the token and full-walks. This is the #64 path, with no syncer change
    [U2: the causes and lifetime of `reset` are unknown; the recovery does not depend on them].
  - `401 expired_access_token`: the oauth2 transport refreshes. A refresh `*oauth2.RetrieveError` is
    returned as a plain error (not `ErrGone`), so the puller logs it and retries at the next interval. 429 is
    handled the same way: the 30 s interval is the backoff, and `Retry-After` is not honoured (non-goal).
- The cursor is persisted in the existing `page_token` meta key, after processing, by `Puller.poll`
  (unchanged). A crash between processing and persisting replays the batch. That is safe because downloads and
  deletes are idempotent against the journal.
- Renames [U4]: delete old + add new (same `id`) is processed as `removeLocal` + `downloadFile`. That is
  correct, but costs a re-download. Pairing by id is optional Phase 24.

### 6.5 Journal: typed remote hash, zero-migration

- Go rename (mechanical, with gopls): `RemoteObject.MD5` -> `Hash`, `JournalEntry.RemoteMD5` -> `RemoteHash`, and
  `store.ConflictInput.{Local,Remote}MD5` -> `{Local,Remote}Hash`. **Keep the JSON tag `remote_md5` and the
  slog keys `local_md5`/`remote_md5`**, so existing Bolt values decode unchanged and log queries keep working.
  The cost is a misleading tag name for Dropbox, documented on the field.
- The hash type is a property of the journal, not of each entry, because one process means one backend means
  one `state.db`. New meta key `remote_backend` in the `meta` bucket. In `OpenJournal` (or a new
  `Journal.EnsureBackend(b)` called by `newAgent`):
  - The key is absent and the journal is empty: write the current backend.
  - The key is absent and the journal is non-empty: treat it as `drive`, since only Drive existed before.
    Then write it.
  - The stored value differs from the current backend: return `ErrJournalBackendMismatch` and fail startup.
    This catches a Dropbox profile pointed at the Drive `state.db`.
- New `internal/contenthash`: `Type` (`MD5`, `Dropbox`) and `HashFile(t, path)`. `Dropbox` is the two-level
  SHA-256 over 4 MiB blocks (research Q3a, about 30 lines). `push_journal.go:fileMD5` is replaced by
  `contenthash.HashFile(p.cfg.HashType, …)`. `PullerConfig.HashType` defaults to `MD5`.

### 6.6 Credentials and startup validation

- **Storage**: an rclone.conf section with `type = dropbox` and the user's own `client_id`, `client_secret`
  and `token` (JSON with `refresh_token`). It is created on a desktop with `rclone config`, or headless with
  `rclone authorize "dropbox" "<id>" "<secret>"` and a paste. This mirrors `homedrive/docs/oauth-troubleshooting.md`
  ([research Q6f]; the exact v1.75.2 output was not run).
- **A user-registered app is required**, with both `client_id` and `client_secret`. homedrive's own SDK client
  cannot use rclone's built-in app key, and requiring the secret sidesteps [U6] (PKCE refresh without a secret).
  A missing id or secret fails startup with `ErrOAuthClientMisconfigured` ("homedrive needs your own Dropbox
  app"). Drive degrades with backoff in the same situation (#67). The difference is deliberate: there are no
  legacy Dropbox configs to keep alive, and the feed can never work without them.
- **Forced refresh at startup**: `dropboxFeed.validateCredential` reuses `forceOAuthRefresh` unchanged with a
  Dropbox `oauth2.Config` (TokenURL `https://api.dropboxapi.com/oauth2/token`, `AuthStyleInParams`). It runs
  before `buildFs`, like #97. `classifyOAuthRefreshErr` takes a provider name and a doc path. Dropbox documents
  no refresh error codes [U7], so it uses the same RFC 6749 bucket as Drive: `invalid_client`,
  `unauthorized_client`, `unsupported_grant_type`, `invalid_scope` mean misconfigured, and anything else
  (forum: `invalid_grant`) means `ErrOAuthTokenInvalid`. The live test records the real codes. Network errors
  are swallowed, as for Drive.
- **Two refreshers, one token**: rclone's Fs and homedrive's feed each refresh in memory. Dropbox refresh
  responses carry no new refresh token (research Q6d), so neither invalidates the other. Nothing is written
  back to rclone.conf, which is #86's existing decision.

### 6.7 Exclusions, exports, upload memory

- **Mandatory excludes** (`rcloneclient.MandatoryExcludes(BackendDropbox)`, appended to `cfg.Watcher.Exclude`
  before the watcher and `RcloneFS` are built, and appended again on `Reload`):
  - rclone's refused names: `**/desktop.ini`, `**/thumbs.db`, `**/.ds_store`, `**/.dropbox`,
    `**/.dropbox.attr`, and `icon\r`. Pushing these always fails with a no-retry error (research Q1, Put), but
    homedrive's own retry loop would try 5 times.
  - Maestral state: `**/.maestral`, `**/.maestral.cache/**`, `**/.dropbox.cache/**`, `**/.mignore`.
  - **Case**: `pathfilter` matching is case-sensitive [inferred: doublestar default]. Generate both the
    lowercase and the canonical-case variants, or add a case-insensitive option to `pathfilter`. Phase 21
    decides.
- **Maestral selective sync**: the first walk downloads folders Maestral had excluded. `watcher.exclude`
  already applies on the pull side (Phase 16), so translating `maestral excluded list` into excludes before
  cutover gives the same result.
- **Paper/exports**: override `skip_exports=true`. This is consistent with PLAN §2's "Google native files: skip".
- **Upload memory**: override `chunk_size=8Mi` (a multiple of 4 MiB, per Dropbox's advice), keep rclone's
  default `batch_mode=sync`, and let users tune both in rclone.conf. `CopyFile` uses `Rcat` with unknown size, so
  files over the streaming cutoff go through upload sessions [inferred from research Q1 and `operations.Rcat`].
  Peak buffer is about `chunk_size × concurrent uploads` (2 push workers plus bisync, so about 24 MiB instead of
  about 144 MiB at 48 MiB). This is an estimate; the live test measures RSS.

### 6.8 Case-insensitive remote (flag `CaseInsensitiveRemote`, Dropbox only)

Facts (research Q5): Dropbox is case-insensitive and case-preserving. `list_folder/continue` does not report
case-only renames. Only the last component of `path_display` is guaranteed to be correctly cased. rclone's
recursive walk builds every path from leaf names, so walk paths are correctly cased [inferred from research Q5b].

- **Pull (incremental)**: `Puller.resolveLocalCase(rel)` walks the path components. For each component it uses
  an exact local match if one exists, otherwise a unique `strings.EqualFold` sibling, otherwise the remote
  spelling. Two or more fold-equal siblings mean `ErrCaseCollision`: the change is skipped, `pull.failure` is
  emitted and an audit entry is written. The journal is keyed by the resolved path.
- **Push**: before `CopyFile`, `Syncer` checks the local parent directory for a fold-equal sibling with a
  different spelling. If one exists, it returns `ErrCaseCollision` without retrying, emits `push.failure` with
  `reason: case_collision`, and does not upload.
- **Bisync**: `diff` keys both maps by `strings.ToLower` when the flag is set:
  - The same file with a different case is "same file". It is logged as `case_mismatch` and nothing is
    renamed, because case-only renames are a non-goal.
  - Two local files that fold together are reported and skipped.

  This also prevents the loop where a case-only server rename shows up as a local-only plus remote-only pair
  and creates both spellings locally.
- Directory-level collisions (`Docs/` and `docs/`) are caught by the same pull-side resolver and by the bisync
  fold check on full paths.

### 6.9 Cutover from Maestral: adopt instead of download (flag `AdoptUnjournaled`)

Today `processFileChange` with no journal entry calls `downloadFile`, which runs `os.Create` and truncates any
existing local file [code]. On a Maestral cutover that means re-downloading the whole dataset and silently
overwriting any local edit Maestral had not pushed yet. With the flag on, an unjournaled change for a path that
exists locally works like this:
- `HashFile(Dropbox)` equals `content_hash`: the remote state is journaled and nothing is downloaded. This
  generalises `adoptIdenticalRemote`.
- Otherwise the change goes to the existing `handleConflict` (newer wins, loser `.old.<N>`), with
  `localMtime` from `stat`.

The first walk hashes every local byte once. Duration on the Pi is unknown [U9].

### 6.10 Folder deletes (flag `ExpandDirDeletes`) [U8]

Whether deleting a folder yields deleted entries for its descendants is undocumented. With the flag on,
`processDelete` for path P also removes the **journaled** files under `P/` (`Store.ListOldSiblings(P+"/")`)
and their journal entries. Unsynced local files are never touched, and `removeLocal` keeps non-empty
directories (#114). Without this, a missed child delete is resurrected by bisync's local-only push.

## 7. Alternatives considered

**A. rclone `ChangeNotify` as the Dropbox change source.** *Strongest form*: zero new dependencies, no
SDK code, and maintained upstream. It already does `get_latest_cursor` + longpoll + `continue` with 30–480 s
latency, and bisync covers whatever it misses. *Why it loses* (driver 1):
- Its cursor is internal and is re-seeded from "latest" on every start, so every change made while the agent
  was down is lost to the feed. This is the same reason Phase 15 rejected Drive's `ChangeNotify`.
- By code reading it also drops changes after any poll error, and does no `reset` re-list (research Q2f,
  [inferred, untested]).
- It is a push-style callback goroutine, which would need a second production pull path next to `PollOnce`.
  That is the `Puller.Run` trap this repo has already hit (#67).
- It emits paths only, so every change needs a `Stat`.

Bisync cannot fully compensate, because it resurrects missed remote deletes instead of applying them. The SDK
import costs about 0 extra binary size (§8).

**B. Several remotes in one process** (`pairs:` list, N engines, shared HTTP and MQTT). *Strongest form*: one
RSS, one MQTT connection, one HA device and one `ctl` endpoint. Rclone's process-global config already
supports several sections. *Why it loses* (drivers 2, 3):
- It changes the config schema (old configs need a compatibility layer), `/status` and `ctl` (per-pair
  namespacing), MQTT topics, `bisyncMu` scoping and the agent lifecycle. That is roughly 1.5–2k lines against
  about 250 for profiles.
- It couples the failure domains: a Dropbox credential failure at startup (§6.6 fails fast) would stop the
  Drive sync too.
- The saving is one Go runtime (tens of MB RSS, not measured).

**C. Generic hash/ID columns per entry** (`remote_hash`, `remote_hash_type`, `rev` added to every
`JournalEntry` with a read-time migration from `remote_md5`). *Strongest form*: self-describing entries and room
for `rev`-based conflict checks. *Why it loses* (driver 2): it rewrites or dual-reads every live Drive entry for
no functional gain. With one backend per `state.db`, a per-DB `remote_backend` key carries the same
information, and the backend-mismatch guard comes with it.

**D. Pure SDK Dropbox `RemoteFS`, without `backend/dropbox`.** *Strongest form*: it avoids the rclone allow-list
change and could link fewer SDK packages. *Why it loses*: homedrive would own upload sessions,
`incorrect_offset` recovery, batching, pacing, 429 handling and name encoding, which is the bulk of rclone's
2,300-line backend. For a single maintainer that is the wrong trade.

## 8. Binary size: how the design stays under the amd64 gate

- `backend/dropbox` costs +1.18 MB (measured). It adds exactly 20 packages; the only new module is the SDK
  (research Q7).
- The feed imports only SDK `dropbox` and `dropbox/files`. Both are already among those 20 packages, so they
  add **nothing** beyond our own code [inferred from research Q7's package list]. **Do not import
  `dropbox/oauth`** (not in the list). Use `golang.org/x/oauth2`, which is already linked.
- `lib/encoder` is already linked through rclone. `contenthash` uses only `crypto/sha256` and `crypto/md5`.
- Our own production code is estimated at about 1.2k lines. Expected cost is about 0.1–0.25 MiB against
  1.07 MiB of amd64 headroom [inferred; no measurement].
- Risk: a future rclone, SDK or Go bump can eat the remaining margin. Phases 20 and 21 must record both arch
  sizes in the PR, built with CI's Go 1.26. See open question Q4 for the gate policy.

## 9. Consequences

**Better**:
- Maestral can be retired.
- A second Drive remote becomes possible through profiles.
- The journal refuses the wrong backend.
- The unjournaled-overwrite risk (§6.9) and the folder-delete gap (§6.10) get a fix path that Drive can opt into
  later (Q2).

**Worse**:
- Two processes, two ports and two HA devices per user.
- The `remote_md5` JSON tag lies for Dropbox.
- A third direct dependency outside rclone (the SDK).
- Cutover hashes the whole local dataset once.
- A remote move re-downloads the moved data.

**Harder to change later**:
- One-backend-per-`state.db` is now enforced. Moving to multi-pair later means splitting journals.
- `--profile` naming becomes user-facing.
- `changeFeed` becomes the place every future backend plugs into. Keep it unexported so it stays cheap to change.

## 10. Cross-cutting concerns

- **Security**:
  - Tokens stay in the user's rclone.conf. homedrive never logs or writes them; log only section names, as
    `oauthvalidate.go` does.
  - The Full Dropbox scope is broad. Request rclone's default scopes and nothing more (research Q6c).
  - Longpoll's `noauth` host is not used.
- **Privacy**: file paths appear in logs and MQTT, as today. File contents are never logged; hashing reads
  them only in memory.
- **Observability**: add `backend` to the startup log, `/status` and `/healthz`'s `rclone` component. New
  audit and MQTT reasons: `case_collision`, `case_mismatch`, `adopted`, and `cursor reset` (the `pull.failure`
  text distinguishes it from Drive's 410/400 text).
- **Failure modes**:
  - Cursor reset: full walk.
  - Refresh rejected at startup: loud exit, then the systemd restart loop (#88: one refresh per restart).
  - 429: retried at the next interval.
  - Case collision: the affected path is skipped and visible.
  - Bisync safety net: see §15 item 6 before relying on it.
- **Rollout and migration**:
  - Drive users: the upgrade is transparent. On the first start, `remote_backend=drive` is written.
  - Dropbox: an App-folder rehearsal (Phase 22), then cutover. Wait until `maestral status` is idle, stop and
    disable Maestral, keep its folder as `local_root`, add the excludes, and start
    `homedrive-dropbox@<user>`. The first walk adopts files by hash.
  - **Never run Maestral and homedrive on the same folder at once.**

## 11. Impact on existing conventions

- **rclone allow-list (AGENTS.md, `homedrive-rclone-import` skill, PLAN §10, CI)**:
  - Add `"github.com/rclone/rclone/backend/dropbox"`. It is imported by name for `dropbox.DbHashType`.
  - Add `lib/encoder`.
  - Record that `fs/config` is already imported but missing from the list (§15).
  - CI grep in `.github/workflows/homedrive.yml` becomes
    `grep -v -E '"github.com/rclone/rclone/backend/(drive|dropbox)"'` with the closing quote, so that
    `backend/dropbox/dbhash` etc. still fail. Do the same in the AGENTS.md snippet.
  - The skill's "user approval" requirement is met by approving this doc.
- **Non-rclone direct dependency**: the SDK is added as a PLAN §2 row next to `drive/v3`.
- **"Only `internal/rcloneclient` imports rclone"**: kept. The SDK is imported only there too.
- **PLAN §1 "multi-pair out of scope"**: kept by using profiles.
- **#67's "warn and back off when client_id is missing"**: Dropbox fails fast instead (§6.6), on purpose.
- **Files < 500 lines**: `drivefeed.go` and `dropboxfeed.go` are separate files. `rclonefs.go` (441 lines)
  shrinks.

## 12. Test strategy

Unit and integration tests (no real Dropbox, per AGENTS.md):
- `dropboxfeed_test.go`: the SDK client gets an `http.Client` whose `RoundTripper` serves `httptest` handlers
  for `files/list_folder/get_latest_cursor` and `files/list_folder/continue`. This does not depend on an SDK URL
  hook. Table cases:
  - paging with `has_more`
  - file, folder and deleted entries
  - root-prefix stripping for `""` and `/Sub`
  - `reset` -> `errors.Is(ErrGone) && errors.Is(ErrTokenRejected)`
  - 401 refresh failure -> plain error
  - 429 -> plain error
  - delete+add of the same path in one batch -> a single `Stat` call
  - excluded paths dropped
  - `include_deleted=true` sent in the request body
- `contenthash_test.go`: the Dropbox type cross-checked against the SDK's `contenthash` package on 0 B,
  1 B, 4 MiB, 4 MiB+1 and 9 MiB inputs.
- `rclonefs_test.go`: `ListChanges` with a fake `changeFeed` plus the existing `fakeListingFS` covers the
  initial walk, then poll, then reset, then re-walk. The existing `TestNewRcloneFS_Ordering*` tests are
  extended to the Dropbox validate.
- **Production-path test** (the `Puller.Run` lesson): `cmd/homedrive/agent_run_test.go` builds an `Agent`
  whose `rfs` is a `MemFS`-backed fake. The fake returns `NewTokenRejectedErr` once. The test drives
  `pollOnceGuarded` and asserts a full walk plus a persisted fresh token.
- Syncer, with the flags on and `MemFS`:
  - `TestPuller_CaseCollision_Skips`
  - `TestPuller_ResolveLocalCase_ParentMiscased`
  - `TestSyncer_Push_CaseCollision_NoUpload`
  - `TestBisync_CaseFold_NoDuplicate`
  - `TestPuller_AdoptUnjournaled_IdenticalNoDownload`
  - `TestPuller_AdoptUnjournaled_DifferentConflicts`
  - `TestPuller_ExpandDirDeletes_OnlyJournaled`

  Each one is also run with the flag off, to prove Drive's behaviour is unchanged.
- Store: `TestJournal_DecodesLegacyRemoteMD5` (golden JSON) and `TestJournal_EnsureBackend_{Empty,LegacyDrive,Mismatch}`.
- MQTT: discovery golden IDs unchanged when `instance==""`, and suffixed when it is set.

**Live test (Phase 22), App folder only**:
- Isolation:
  - The maintainer registers a **separate** Dropbox app with *App folder* access.
  - Its rclone section `dbxtest` lives in a **scratch rclone.conf** that contains no Full-Dropbox section, so the
    test process cannot reach the real root even by mistake.
  - Before the first write, `rclone lsf dbxtest:` must list only the empty app folder [U5: confinement is
    documented but untested].
  - Follow `homedrive/docs/live-testing.md`: scratch `local_root`, state, audit, port, and MQTT instance.
  - Pitfall: if Maestral syncs the whole Dropbox, it will mirror `/Apps/<app>/` into the Maestral folder. Add
    it to `maestral excluded` first, or accept it and clean up.
  - Never point `local_root` at the Maestral folder.
- Checklist. Each item resolves or records an unknown:
  - deletes arrive on `continue` [U1]
  - a garbage cursor recovers through the full walk [U2-adjacent]
  - a rename appears as delete+add with the same id and order is observed [U3, U4]
  - folder-delete granularity [U8]
  - a case-only server rename is invisible, and bisync does not duplicate
  - a local `A.txt` + `a.txt` collision is guarded
  - the journal hash equals the local `contenthash`
  - loop prevention with `client_modified` rounding (no re-upload echo)
  - RSS during a 500 MB upload at `chunk_size=8Mi`
  - wrong secret and revoked token give the classified startup errors, and the actual error codes are recorded [U7]
  - restart resumes from the persisted cursor
  - cutover rehearsal: seed an identical local copy, empty journal, zero downloads, zero conflicts, and record
    the hash time [U9]

## 13. Phases (PLAN.md §14 numbering continues)

Each phase is one PR. Sizes are rough diff lines including tests.

| Phase | Scope | Main files | Size | Tests | Depends |
|---|---|---|---|---|---|
| **17 Backend seam, Drive only, zero behaviour change** | `changeFeed` + `drivefeed.go` (move); `MD5`->`Hash` rename (JSON tag kept); `internal/contenthash` (MD5 only); provider-parametrised `classifyOAuthRefreshErr` (same Drive text) | `rcloneclient/{rclonefs,remotefs,driveapi,drivefeed,oauthvalidate}.go`, `store/{journal,conflict}.go`, `syncer/*`, tests | ~700 (80% mechanical) | full suite green with assertions unmodified except renames; legacy-JSON golden; fake-feed `ListChanges` | — |
| **18 Profiles: second instance per user** | `--profile`/`HOMEDRIVE_PROFILE`; `mqtt.instance`; `homedrive-dropbox@.service` (+ user twin); `config-dropbox.yaml` stub; README + `homedrive-systemd` skill | `cmd/homedrive/main.go`, `ctl.go`, `config`, `mqtt/discovery.go`, `linux/*` | ~250 | path resolution table; discovery goldens; `ctl --profile` | — (parallel to 17) |
| **19 Syncer flags for case-insensitive remotes** | `CaseInsensitiveRemote`, `AdoptUnjournaled`, `ExpandDirDeletes`, `HashType` in `PullerConfig`/`syncer.Config`/`BisyncConfig`; `ErrCaseCollision`; `contenthash.Dropbox`; `pathfilter` case option | `syncer/{pull_process,pull_dir,push_journal,syncer,bisync_ops}.go`, `contenthash`, `pathfilter` | ~650 | flag-on/flag-off pairs (§12) | 17 |
| **20 Dropbox change feed (SDK)** | `dropboxfeed.go` (cursor, continue, dedupe, reset->`ErrGone`, Dropbox `oauth2.Config`, validate); go.mod adds SDK as direct dep; PLAN §2 row | `rcloneclient/dropboxfeed.go` (+test) | ~800 | §12 feed table; `contenthash` cross-check | 17 |
| **21 Wire the Dropbox backend** — **adds `backend/dropbox` import** | `DetectBackend`; `newRcloneFS` picks feed; `remote_backend` guard; mandatory excludes; connection-string overrides; `lib/encoder` decode; agent sets flags; `/status` `backend`. **Same PR updates AGENTS.md allow-list + snippet, `homedrive-rclone-import` skill, PLAN §10, and the CI grep in `.github/workflows/homedrive.yml`**; records both arch sizes | `rcloneclient/{backend,rclonefs}.go`, `store/journal.go`, `cmd/homedrive/agent.go`, CI, docs | ~600 | ordering test with Dropbox validate; `EnsureBackend`; `agent_run_test` production-path reset; size + grep in CI | 18, 19, 20 |
| **22 Live validation + docs** | App-folder run of §12 checklist; `homedrive/docs/dropbox.md` (app registration, headless `rclone authorize`, Maestral cutover, excludes); Dropbox section in `oauth-troubleshooting.md`; unknowns register updated; `RELEASE_NOTES.md` -> `homedrive/v0.2.0` | docs only (+ fixes found) | ~300 | live checklist pasted in PR | 21 |
| 23 *(optional, recommend not now)* Long-poll wake | longpoll goroutine signalling `runPullLoop`'s timer | `agent_run.go`, `dropboxfeed.go` | ~300 | — | 21 |
| 24 *(optional, recommend not now)* Remote move pairing by id | pair delete+add within one batch via journal `RemoteID` -> local `os.Rename` + `RewritePrefix` | `syncer/pull_*` | ~400 | — | 22 |

No PR makes Dropbox selectable (21) before the safety flags (19) exist.

## 14. Cost, and smaller options

**Core cost (17–22)**: about 3.3k diff lines, of which about 1.2k are production Go. That is roughly 6 phase
PRs, comparable to Phases 14–16 together.

**Smaller option S1, rclone-only Dropbox** (Phases 17, 18, 19 and 21 minus 20). `dropboxFeed` becomes a thin
adapter over rclone `ChangeNotify`, buffering the paths it reports between polls. It saves about 800 lines and
the SDK dependency. It gives up driver 1: changes made while the agent is down are lost, changes after any poll
error are lost [inferred], and there is no `reset` recovery. Bisync then resurrects remote deletes that were
missed. That is not acceptable for a primary copy of the user's Dropbox.

**Smaller option S2, build nothing in homedrive**:
- Keep Maestral running ("usable in the medium term", research Q8) until it breaks.
- Or use rclone's own `rclone bisync` on a systemd timer against the Maestral folder: about 30 lines of
  unit/timer and a doc, and zero homedrive code.
- Either way it gives up real-time push, homedrive's newer-wins/`.old.<N>` semantics, the journal-based loop
  prevention, and MQTT/HA. rclone bisync's conflict naming and listing-snapshot model differ (not evaluated
  here).

**My recommendation**:
- Build 17–22.
- **Do not build 23 or 24** until the live test shows the need: 30 s latency is fine for a NAS, and remote
  moves are rare.
- If the maintainer only needs Dropbox kept alive this quarter, S2 is the honest choice. Phase 18 (profiles) is
  still worth building on its own.

## 15. Discrepancies between the references and the code

1. PLAN §16.2, `architecture.md` and both skills list a 6-method `RemoteFS`. The code has 9 (`List`,
   `GetStartPageToken`, `DownloadFile`) [code].
2. The `homedrive-rclone-import` skill describes these, none of which match the code:
   - `ListChanges` "via cast to `*drive.Fs`". The code uses a separate `drive/v3` client; the cast is only a
     defensive check.
   - `fs.GlobalConfig.ConfigPath`. The code uses `config.SetConfigPath`.
   - Dry-run inside `RcloneFS`. The code uses the `DryRunFS` wrapper.
   - The allow-list omits `fs/config`, which `rclonefs.go` and `driveapi.go` import [code].
3. `architecture.md` pull flow says "Moved/renamed: move on local disk". The pull path never moves locally; it
   only downloads and deletes [code].
4. `oauthvalidate.go` cites rclone v1.75.0. The pin is v1.75.2 (research, "Local usage").
5. `remoteObjectFromRclone` never fills `MD5`, so Drive journal hashes come only from the Changes API.
   Entries written by push and bisync have `remote_md5:""` [code]. The comment says that `fs/hash` "would add
   backend imports". That is inaccurate: it is not a backend, though it is off the allow-list.
6. **`Bisync.listRemote` calls the non-recursive `RemoteFS.List(ctx, "")`** [code]. MemFS mirrors this. So the
   hourly safety net appears to see only root-level remote files, and nested local files would come out as
   `DiffLocalOnly` on every pass [inferred; verify before relying on bisync as Dropbox's safety net; file an
   issue].
7. **Push `DirRename` calls `RcloneFS.MoveFile`, which uses `NewObject(src)`.** For a directory path that is
   expected to fail with `ErrorIsDir`. `operations.DirMove` would be the right call [inferred from the rclone
   `NewObject` contract; not verified on Drive]. It matters equally for Dropbox.
8. `processFileChange` overwrites an existing unjournaled local file without a conflict check [code]. This is
   a latent data-loss path on Drive too (§6.9, Q2).
9. CI builds with Go 1.26 (`setup-go '1.26'`, `go 1.26.2` in go.mod with no toolchain line). The size
   measurements in the brief used go1.27.4.

## 16. Unknowns register (from the research, plus two of mine)

| # | Unknown | Design stance |
|---|---|---|
| U1 | `include_deleted` semantics on `continue` | request `true`; works either way |
| U2 | cursor lifetime, `reset` causes | `reset` -> `ErrGone` -> full walk; independent of cause |
| U3 | ordering within/across pages | per-path dedupe, `Stat` on contradiction |
| U4 | rename = delete+add, same id | handled as delete+download; pairing optional (24) |
| U5 | App-folder confinement at API level | live test isolated by separate app **and** scratch rclone.conf |
| U6 | PKCE refresh without secret | sidestepped: secret required |
| U7 | refresh error codes | RFC 6749 bucket; record live codes |
| U8 | folder-delete granularity (mine) | `ExpandDirDeletes` |
| U9 | Pi hash throughput (research Q3c) | cutover time unknown; measure in 22 |
| U10 | real size with CI toolchain | measure in 20/21 |
| U11 | rate limits, `Retry-After` presence | 30 s interval as backoff; revisit if 429s seen live |

## 17. Confirmation

- CI enforces the size gate on both arches and the tightened backend grep.
- Phase 17's PR shows the existing test suite passing **with no assertion changes other than identifier
  renames**. A reviewer diffs the test files to confirm.
- `agent_run_test.go` covers Dropbox-style reset through `pollOnceGuarded`, not `Puller.Run`.
- Review questions for every Dropbox PR:
  - Does any new pull behaviour live outside `PollOnce`?
  - Does any path reach `os.Create` on an unjournaled existing file with `AdoptUnjournaled` on?
  - Is a Full-Dropbox credential reachable from any test or live-test config?
- Phase 22's PR contains the filled live checklist and the updated unknowns register.

## 18. Open questions for the maintainer (decide before Phase 17 starts)

1. **Build at all?** Build 17–22, or take S2 (keep Maestral, or `rclone bisync` on a timer) and only build
   Phase 18?
2. **Drive opt-in**: should Drive also turn on `AdoptUnjournaled` and `ExpandDirDeletes`, given §15 item 8?
   It would be a behaviour change for the live agent, so it would be a separate PR.
3. **Account type**: is the Dropbox account personal, or Business with team space? Team space needs
   `root_namespace` and path-root handling, which is a non-goal here.
4. **Size gate policy**: keep the 26,214,400-byte gate for amd64 (about 1 MiB of headroom left after this
   work), or gate amd64 differently from arm64, the deployment target?
5. **Allow-list approval**: approve adding `backend/dropbox` and `lib/encoder`, and the Dropbox SDK as a
   direct non-rclone dependency.
6. **Unit naming**: `homedrive-dropbox@.service` using `%j`, or a different profile mechanism?
7. **Fail-fast on a missing Dropbox client_id/secret** (§6.6) instead of #67-style degrade: agreed?
8. **§15 items 6–7** (bisync non-recursive `List`, directory `MoveFile`): file and fix before Dropbox, since
   Dropbox leans on both?
