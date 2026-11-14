# Dropbox as a second sync backend: reference research

> **Version note (2026-11-14):** this research was done against rclone **v1.75.2**. During the same day, Dependabot PR #131 moved `main` to rclone **v1.76.0**, and the Dropbox SDK it pulls in from v6.4.0 to **v6.6.2**. File:line citations into rclone and the SDK refer to the older versions and have **not** been re-checked against v1.76.0. Re-verify any citation that a phase relies on.


## What was asked

Reference for designing a Dropbox backend for `homedrive` (local directory <-> Dropbox, same
semantics as the Drive backend, replacing the archived Maestral client on the NAS; not a data
migration). Covers rclone's `backend/dropbox` at the pinned version, Dropbox's change tracking, hashes,
rate limits, paths and names, OAuth for a user-registered app, binary-size impact, and Maestral specifics.

## Conventions used in this note

- **[doc]** = Dropbox's published documentation, quoted or paraphrased, with URL.
- **[SDK]** = Dropbox's official Go SDK source (`dropbox-sdk-go-unofficial/v6@v6.4.0`), including the doc
  comments generated from Dropbox's API spec (spec revision `b5f7ddd`, per `dropbox/sdk.go`).
- **[code]** = read directly from rclone `v1.75.2` source (`github.com/rclone/rclone@v1.75.2/...`, paths
  below are relative to that module root) or from homedrive's repo.
- **[forum]** = Dropbox Community / forum posts (including Dropbox staff replies). Evidence that
  something is true in practice, not a contract.
- **[inferred]** = my reasoning from the sources above. Each one says what it rests on.

Local module cache paths are omitted on purpose; the rclone and SDK files are in
`$(go env GOMODCACHE)/github.com/rclone/rclone@v1.75.2/` and
`$(go env GOMODCACHE)/github.com/dropbox/dropbox-sdk-go-unofficial/v6@v6.4.0/`.

---

## Summary for the designer

1. **rclone's Dropbox backend is a complete `fs.Fs`** (List/Put/Mkdir/Move/DirMove/Copy/Purge/ChangeNotify/
   Hashes/About/PublicLink). It is registered only if imported; homedrive currently imports only `backend/drive`.
   [code] `backend/dropbox/dropbox.go:2289-2302` (interface assertions).
2. **The Dropbox hash is NOT MD5.** Hash type name `"dropbox"` (alias `DropboxHash`, 64 hex chars), exported
   as `dropbox.DbHashType`. It is `SHA-256(concat of SHA-256(each 4 MiB block))`, hex-encoded. The journal's
   `RemoteMD5` has no direct equivalent. [code] `dropbox.go:172`, `dbhash/dbhash.go`; [doc] content-hash page.
3. **rclone's `ModTime` is `client_modified`, not `server_modified`.** `client_modified` is the mtime the uploader
   sent, rounded to whole seconds (UTC) on upload. Dropbox's own spec says it "should only be used for display
   purposes ... and not, for example, to determine if a file has changed". Change detection should use
   `content_hash` or `rev`. rclone never exposes `server_modified` or `rev`. [code] `dropbox.go:1926,2241`; [SDK] `files/types.go` (FileMetadata doc).
4. **`SetModTime` is unsupported**: setting an mtime means re-uploading the file. [code] `dropbox.go:1983-1987`;
   [rclone doc] `docs/content/dropbox.md` "Modification times and hashes".
5. **`ModTime()` silently returns `time.Now()` if the metadata read fails.** A journal that calls it without
   checking will record "now". [code] `dropbox.go:1971-1978`.
6. **Default `batch_mode=sync` routes every upload through upload sessions + `finish_batch_v2`**, including small
   files. Single-shot `files/upload` is used only with `batch_mode=off` and only for size <= chunk size.
   [code] `dropbox.go:2251-2257`, `:2182-2184`; `lib/batcher/options.go` (default `"sync"`).
7. **rclone's `ChangeNotify` is usable but has a silent-loss gap.** It wraps `get_latest_cursor(recursive)` +
   `list_folder/longpoll` (on the `notify` host, no auth) + `list_folder/continue`. On any error in the poll path
   it returns an empty cursor, and the next tick re-seeds from "latest", so changes made in the gap are dropped.
   It does not do a full re-list on `reset`. [code, inferred from reading] `dropbox.go:1613-1787`.
8. **Case-only server-side renames are not reported by `list_folder/continue`** ([SDK] `Metadata.PathDisplay` doc:
   "Changes to only the casing of paths won't be returned by `listFolderContinue`"). The Fs is
   `CaseInsensitive: true` ([code] `dropbox.go:610-614`), so local `A.txt` and `a.txt` collapse onto one remote path.
9. **Dropbox publishes no rate-limit numbers.** Limited calls return HTTP 429 with `too_many_requests` or
   `too_many_write_operations`; the performance guide says rate-limited responses carry `Retry-After` [doc].
   rclone reads `retry_after` from the JSON body (SDK maps 429 to `RateLimitAPIError`) and uses its own pacer.
   The SDK's own retry layer is disabled unless `Config.RetryPolicy` is set, and rclone does not set it [code, SDK].
10. **rclone's `*Fs` exposes no accessor to its SDK clients** (`srv`, `svc`, `sharing`, `users`, `team` are unexported).
    A homedrive caller that needs raw SDK calls must build its own SDK client from the token (SDK
    `dropbox.Config{TokenSource: ...}` + `files.NewContext`). [code] `dropbox.go:384-401`; [SDK] `sdk.go`.
11. **App-folder apps are confined by Dropbox's own statement**: "Your app gets read and write access to this folder
    only" [doc, developer guide]. The "implied root" behaviour and the rejection of `Dropbox-API-Path-Root`
    for app-folder apps come from forum posts only.
12. **OAuth**: Dropbox's no-redirect flow shows the authorization code on dropbox.com for copy-paste when no
    `redirect_uri` is set [doc, OAuth guide]. Refresh tokens do not expire on their own [doc, dropbox.tech]. The
    SDK ships PKCE and no-redirect helpers; its refresh test sends no `client_secret`. Dropbox does not document
    refresh error codes; forum reports show `invalid_grant` with "refresh token is malformed".
13. **Binary**: adding `backend/dropbox` pulls in 20 extra Go packages; the only new module is the Dropbox SDK.
    Measured delta on a minimal drive-vs-drive+dropbox arm64 binary: +1.18 MB. The current homedrive arm64 build is
    22.15 MB, so the estimate after adding Dropbox is about 23.3 MB, under the 25 MB gate (estimate, not measured on
    the real binary). **But `backend/dropbox` breaks the CI invariant and the allow-list in `AGENTS.md`**, so that
    needs an explicit decision.
14. **Rename handling**: Dropbox staff say a rename/move appears as a deleted entry at the old path plus a new entry at
    the new path, and the new entry keeps the file's `id` [forum]. There is no official doc for this, and ordering
    within a page is not guaranteed [forum].
15. **Maestral is archived** (dates conflict: GitHub banner says archived Jun 28, 2026; README says "As of
    2026-07-28"). Its special names are `.maestral`, `.mignore`, `.maestral.cache`, `.dropbox.cache` [Maestral
    source]. `.mignore` syntax is not verified. `com.dropbox.ignored` xattr is a Dropbox-client feature; whether
    homedrive should honour it is undefined.

---

## Q1. What rclone's `backend/dropbox` exposes at v1.75.2

**Pins.** homedrive `go.mod` pins `github.com/rclone/rclone v1.75.2` [code, `homedrive/go.mod:10`]. rclone's own `VERSION`
file reads `v1.75.2`. rclone pins `github.com/dropbox/dropbox-sdk-go-unofficial/v6 v6.4.0` [code, rclone `go.mod`].
Files: `backend/dropbox/dropbox.go` (2302 lines), `batcher.go`, `dbhash/dbhash.go`; docs `docs/content/dropbox.md`.

**Registration and options** [code, `dropbox.go:171-355`]: backend name `dropbox`, description "Dropbox". Options and defaults:

| Option (config key) | Default | Notes |
|---|---|---|
| `chunk_size` | 48 MiB | max 150 MiB (`dropbox.go:90-91`). Chunks buffered in memory; source comment says "rclone by default does 4 transfers so this should use 4*48 MiB = 192 MiB" (`:87-89`). Relevant to Pi RAM. |
| `batch_mode` | `sync` | `off` / `sync` / `async` (`lib/batcher/options.go`). |
| `batch_size` | 0 | with `sync`, equals `--transfers` (`lib/batcher/batcher.go`: `opt.Size = ci.Transfers`). |
| `batch_timeout` | 0 | `sync` default 500 ms (`dropbox.go:133-138`). |
| `pacer_min_sleep` | 10 ms | pacer max sleep 2 s, decay constant 2 (`:64-66`). |
| `root_namespace` | "" | overrides namespace used as root (see below). |
| `impersonate`, `impersonate_admin` | "" | Team-only; add `members.read` / `team_data.member` scopes. |
| `shared_files`, `shared_folders`, `skip_shared_folders`, `skip_unowned_folders` | false | shared-folder modes (`:243-297`). |
| `export_formats` | `html,md` | Paper docs export (see Q5). |
| `skip_exports`, `show_all_exports` | false | |
| `encoding` | `Slash,BackSlash,Del,RightSpace,InvalidUtf8,Dot` | `dropbox.go:313-317`; `Dot` included via `encoder.Base` (`lib/encoder/standard.go`). |
| `client_id`, `client_secret`, `token`, `auth_url`, `token_url`, `client_credentials` | | rclone docs, `docs/content/dropbox.md` "Standard/Advanced options". |

**Features** [code] `dropbox.go:610-614`: `CaseInsensitive: true`, `CanHaveEmptyDirectories: true`, `ReadMimeType: false`.
`Precision()` = 1 second (`:1326-1328`).

**Operations** (all in `dropbox.go`):

- **List**: `ListP` (`:1081-1204`) uses `files/list_folder` with `recursive=false`, `limit=1000`, then
  `list_folder/continue` while `has_more`. Entries use `PathDisplay` (last component is reliably cased). Shared
  folders are listed with the SDK default `include_mounted_folders=true` (`files/types.go` `NewListFolderArg`).
- **Put / Update**: overwrite mode (`commitInfo.Mode = overwrite`, `:2239`); `ClientModified` set to
  `src.ModTime().UTC().Round(time.Second)` (`:2241`). Files whose names match rclone's `ignoredFiles` regex are
  refused with a no-retry error (`:124`, `:2235-2237`): `desktop.ini`, `thumbs.db`, `.ds_store`, `icon\r`,
  `.dropbox`, `.dropbox.attr`.
- **Upload path**: single-shot `files/upload` only when `!batching && size<=chunk && size>=0` (`:2251-2257`). Otherwise
  `upload_session/start` -> `append_v2` (per chunk, with `incorrect_offset` recovery `:2119-2140`) -> finish (`:2175-2197`).
  With batching, finish is `UploadSessionFinishBatchV2` (`batcher.go:finishBatch`).
- **Copy**: `files/copy_v2` (server-side, `:1339-1386`); removes an existing destination first via `RemoveExisting`.
- **Move**: `files/move_v2` (`:1406-1456`); retries once on `from_lookup/not_found` (issue #8881 comment, `:1433-1438`).
- **DirMove**: `files/move_v2` on folders (`:1539-1575`); returns `fs.ErrorDirExists` if the destination exists.
- **Purge / Rmdir / Remove**: `purgeCheck` (`:1265-1316`), `files/delete_v2` (`:2275-2286`). Rclone docs: `purge` on a
  directory with more than 10,000 files fails with "There are too many files involved in this operation"
  [rclone doc, Limitations].
- **About**: `users/get_space_usage` (`:1578-1605`).
- **Hashes**: `hash.Set(DbHashType)` only (`:1790-1792`); `Object.Hash` returns `hash.ErrUnsupported` for any other type (`:1869-1881`).
- **Shutdown**: closes the batcher (`:1796-1799`).

**Object** [code, `:415-426`]: fields `id`, `remote`, `bytes`, `modTime`, `hash` (content_hash), `exportType`.
`ID()` returns Dropbox's file `id` (`id:...`), so a journal can key on it. `Size()` returns `bytes`. `Storable()` is true.

**Reaching the SDK client from a library caller.** There is none in the public API. `*Fs` has no exported method that
returns `srv`, `svc`, `sharing`, `users` or `team` [code, `grep "^func (f \*Fs) [A-Z]"`; list above]. Homedrive's
`*drive.Fs` cast works because `backend/drive` exposes its service accessors; the Dropbox backend does not
[code, inferred from the method list]. Options:

1. Build a homedrive-owned SDK client from the same OAuth token. The SDK's `dropbox.Config` has `TokenSource`, `Client`,
   `PathRoot`, `RetryPolicy` (nil = off), and helpers `WithRoot(nsID)` / `WithNamespaceID(nsID)` [SDK, `dropbox/sdk.go`
   lines ~85-156]; `files.NewContext(cfg)` builds the files client [SDK, `files/client.go:4182`].
2. Use rclone's `fs.Fs` interface for everything it covers and only build the SDK client for gaps. Note that
   `dropbox.DbHashType` is exported, so the hash type can be referenced without the SDK.

**Namespace / team handling** [code `dropbox.go:664-686`]: if `root_namespace` is set, it is used as the path root. Else
if the root path starts with `/`, rclone calls `users/get_current_account` and uses `RootInfo.RootNamespaceId`
(team root or user root). Else no path-root header (personal root). rclone doc: "When using Dropbox for business
`remote:` and `remote:path/to/file` will refer to your personal folder ... `remote:/TeamFolder`" [rclone doc, "Dropbox for business"].
`headerGenerator` sets `Dropbox-API-Path-Root` only when `ns != ""` (`:706-713`).

---

## Q2. Change tracking: the Drive Changes API counterpart

### 2a. The primitives (Dropbox)

[SDK doc comments, from Dropbox's spec] unless noted.

- **`files/list_folder`** `ListFolderArg` (`files/types.go`, ~L1885-1930): `path`, `recursive` ("If true, the list folder
  operation will be applied recursively to all subfolders ... In some cases, setting ... recursive to true may lead to
  performance issues or errors"), `include_deleted` ("If true, the results will include entries for files and folders
  that used to exist but were deleted"), `include_mounted_folders`, `limit` ("approximate"), others.
- **`files/list_folder/continue`** `ListFolderContinueArg { cursor }`: "The cursor returned by your last call to `listFolder`
  or `listFolderContinue`." Error tags include `reset` (`files/types.go:1966-1971`, `ListFolderContinueErrorReset = "reset"`).
  **The spec has no doc text for the `reset` tag** ("has no documentation (yet)" on the error type).
- **`files/list_folder/get_latest_cursor`**: takes a `ListFolderArg`, returns `{ cursor }` only (`files/types.go:2036-2043`).
- **`files/list_folder/longpoll`** `ListFolderLongpollArg { cursor, timeout }`: "The request will block for at most this
  length of time, plus up to 90 seconds of random jitter added to avoid the thundering herd problem." Cursors from
  `include_media_info=true` are not supported (`types.go:2051-2063`). Result `{ changes: bool, backoff: uint64 }`:
  "Indicates whether new changes are available. If true, call `listFolderContinue` ..." and "If present, backoff for at
  least this many seconds before calling `listFolderLongpoll` again" (`types.go:2083-2092`). Error tags: `reset`, `other`.
- **Auth**: longpoll is routed to host `notify` with `Auth: "noauth"` (`files/client.go:2492-2501`). The SDK removes the
  Authorization header for `noauth` routes (`sdk.go` `newHTTPRequest`). So longpoll needs no access token. [SDK, code]
- **Timeout bounds**: the SDK doc says only "a timeout in seconds" with jitter up to 90 s. rclone's code comment says
  "Dropbox sets a timeout range of 30 - 480" and clamps to that range [code, `dropbox.go:1710-1721`]. **That 30-480 range is
  rclone's claim and I did not find it in a Dropbox doc.** The SDK's `NewListFolderLongpollArg` defaults `Timeout` to 30.

### 2b. Metadata entry types

[SDK] `files/types.go`: `FileMetadata` (`id`, `rev`, `size`, `client_modified`, `server_modified`, `content_hash`,
`is_downloadable`, `path_lower`, `path_display`), `FolderMetadata` (`id`, path fields), `DeletedMetadata` ("Indicates that
there used to be a file or folder at this path, but it no longer exists", `types.go:1016-1020`). **A `DeletedMetadata`
entry carries no `id` and does not say whether the removed item was a file or folder** [forum, staff reply; the SDK type
has only `IsRestorable`].

### 2c. Renames and moves

- [forum, Dropbox staff, "Handling File Rename/Move in the API" thread]: a rename yields a deleted entry at the old path and a
  new entry at the new path; "the new entry will have the same 'id' as the previous (non-deleted) entry". Keep state keyed by
  `id` to track renames.
- [forum]: "there can be a slight delay between the deleted and new entries"; deleted and new entries for a rename can appear in
  the same response, "the processing order is hard to enforce"; deleted entries "just indicate that something used to exist at
  that path, but does not itself have the id".
- **Not documented in any official text I could read.** Treat as observed behaviour.

### 2d. Ordering and paging

- `has_more` = more entries available; pass cursor to `continue` (SDK doc, `ListFolderResult`). rclone's `ListP` and
  `ChangeNotify` both loop on `HasMore` [code, `:1199-1201`, `:1782-1784`].
- Ordering within a page: **not documented**; forum staff indicate it is not guaranteed (see 2c).

### 2e. Cursor lifetime and `reset`

- The SDK doc gives no meaning for `reset`. Forum guidance (Dropbox staff/moderators): a reset means the cursor is
  invalid; "the standard fix is a full restart", i.e. call `list_folder` again for a new cursor, and "the Dropbox API does not
  return a diff of what was missed, so a full re-list is needed" [forum, "help-understanding-how-fileslist-foldercontinue"
  thread; third-party summary]. Causes are not documented; forum posts say cursors "may become invalid at any time, due to
  some kinds of operations in the account" [forum].
- **Cursor expiry period: not found in any source.**
- `include_deleted` vs cursors: [forum, staff] "include_deleted is effectively meaningless" for `continue`; the default is
  false. **A conflicting forum report says deletions still come back from `continue` with `include_deleted=false`.** I could
  not resolve this. See "could not establish".

### 2f. How rclone uses these

- **`ListP`** (`dropbox.go:1081-1204`): plain paging, no cursor kept. Used for full walks.
- **`ChangeNotify`** (`dropbox.go:1613-1787`), code-read:
  1. `changeNotifyCursor`: `list_folder/get_latest_cursor` with `NewListFolderArg(root)` and `Recursive = true`
     (`:1657-1676`). `NewListFolderArg` leaves `include_deleted=false` (`files/types.go`).
  2. Loop on `pollIntervalChan` ticks: `changeNotifyRunner(cursor)` (`:1706-1787`):
     a. `list_folder/longpoll` with `Timeout` clamped 30-480 s (`:1710-1731`), on the unauthenticated `svc` context.
     b. If `!Changes` return same cursor. Else sleep `Backoff` seconds (`:1740-1743`).
     c. Loop `list_folder/continue` until `!HasMore`, calling `notifyFunc(path, type)` per entry (`:1745-1785`).
        Folders -> `EntryDirectory`; files and deleted entries -> `EntryObject` (`:1759-1771`), so a deleted folder is
        reported as an object. Paths are `PathDisplay` trimmed of the root and decoded through the encoder (`:1776-1779`).
  3. **Error handling**: on any error in (a) or (c) the function returns `""` (the named return is never set before the
     early `return`, `:1732-1734` and `:1755-1756`). `ChangeNotify` then stores `""` as `startCursor` (`:1648-1649`), and on
     the next tick re-seeds from `changeNotifyCursor` ("latest") (`:1640-1646`). **Changes that occurred between the failed
     poll and the re-seed are never delivered.** Nothing handles `reset` specifically; it is treated like any other error.
     [inferred from code reading; not exercised by any test I looked at]
- **Usability for homedrive**: `ChangeNotify` emits only paths (no ids, no metadata, no content hash, no rev), so each change
  needs a follow-up `Stat` or `List`. The poll interval comes from the `pollIntervalChan` the caller sends. The silent-loss gap
  above makes it unsafe as the sole pull path without the hourly full reconciliation (PLAN §7.2 already has one).

---

## Q3. `content_hash`, ModTime and their journal implications

### 3a. The algorithm [doc]

Dropbox content-hash page (`docs.dropboxapi.com/.../technical-reference/content-hash`, reached from
`dropbox.com/developers/reference/content-hash`):

- "Split the file into blocks of 4 MB (4,194,304 or 4 * 1024 * 1024 bytes)." The last block may be smaller.
- "Compute the hash of each block using SHA-256."
- "Concatenate the hash of all blocks in the binary format to form a single binary string." Then SHA-256 that string.
- "Output the resulting hash in hexadecimal format." No output length is stated; the example is 64 hex characters (SHA-256).
- "Note there is no block for an empty file of zero length. In this case an empty string would be formed in step 3 above."

**It is not MD5, and not a plain SHA-256 of the file.** It is a two-level SHA-256 tree.

rclone implements exactly this in `backend/dropbox/dbhash/dbhash.go` (`bytesPerBlock = 4*1024*1024`, `sha256` for block and
total, `Sum` writes the final partial block only if `n != 0`) [code]. Its test file exists in the same directory; I did not
run it.

### 3b. How rclone exposes it

- Hash type name `"dropbox"`, alias `"DropboxHash"`, width 64 (`dropbox.go:172`). Exported Go var `dropbox.DbHashType`.
- `Object.Hash(ctx, dropbox.DbHashType)` reads the cached `content_hash` from metadata (`:1869-1881`). The value comes from
  the server's metadata; **nothing is downloaded** to get it.
- After an upload in batch mode, the hash is read from the `FileMetadata` returned by the commit (`setMetadataFromEntry`,
  `:1927`). Async batch mode can leave it empty (`:2262-2270`); not relevant to the default `sync`.

### 3c. Implications for a journal that stores Drive MD5

- **Field mismatch.** `RemoteMD5` (journal `JournalEntry`, `homedrive/internal/store/journal.go:36`) has no Dropbox
  counterpart. A Dropbox-aware journal needs `content_hash`, plus Dropbox's `rev` and `id`, which the SDK exposes but
  rclone does not.
- **Comparing local files costs a full local hash.** The local side must be hashed with the same two-level SHA-256 to compare
  with `content_hash`. For a 4 MiB block size this is one SHA-256 pass over the file, plus a 32-byte digest per 4 MiB. Go's
  arm64 `crypto/sha256` uses ARMv8 SHA2 instructions when the CPU reports `cpu.ARM64HasSHA2` [code, Go 1.27.4 toolchain
  `crypto/internal/fips140/sha256/sha256block_arm64.go:14`]. **Whether the Pi's CPU reports it, and the throughput on the Pi,
  were not measured.** Cost on the NAS is unknown.
- **Stat before hash.** `rev` (SDK: "can be used to detect changes and avoid conflicts") and `server_modified` are cheaper remote
  change signals than `content_hash`, and `content_hash` is needed only to confirm equality. [SDK doc on `Rev`; forum staff suggest
  comparing `content_hash` to avoid re-downloading]
- **Local hash on a Pi for large files** is a whole-file read per comparison. It should be triggered only when size or mtime
  changes, like Maestral's approach (not verified; see Q8).

### 3d. ModTime: `client_modified` vs `server_modified`

- rclone sets `o.modTime = info.ClientModified` (`dropbox.go:1926`). It never reads `server_modified` (grep: no matches in
  `backend/dropbox/`).
- On upload rclone sends `ClientModified = src.ModTime().UTC().Round(time.Second)` (`:2241`), so **sub-second precision is lost and
  rounding is to nearest** (not truncation). With homedrive's 1 s loop-prevention tolerance (PLAN §5.3, §7.3), this is within tolerance
  but should be tested.
- [SDK doc, `FileMetadata.ClientModified`]: "Since this time is not verified (the Dropbox server stores whatever the desktop client
  sends up), this should only be used for display purposes ... and not, for example, to determine if a file has changed or not."
- `server_modified`: "The last time the file was modified on Dropbox." [SDK doc]. Not exposed by rclone.
- Paper (exportable) docs: rclone docs say "Modification times for Dropbox Paper documents are not exact, and may not change for some
  period after the document is edited" [rclone doc, Limitations].

---

## Q4. Rate limits, retries and upload limits

### 4a. Documented behaviour [doc, Dropbox performance guide, `docs.dropboxapi.com/dropbox-api/docs/performance`, reached from `developers.dropbox.com/dbx-performance-guide`]

- "While Dropbox does not publish exact rate limits" ... "Rate limits apply per authorization" ...
- "A limited call returns ... HTTP 429 ... reason `too_many_requests`."
- "Rate limited responses always include a Retry-After header." It gives seconds to wait.
- "Rate limited requests themselves also count towards rate limits."
- "rapid retry loops without pause or respecting this header will be counter-productive."
- **`too_many_write_operations`**: "This suggests that there are too many writes occurring on the destination." Per the guide, for single
  uploads "The exception returns a Retry-After of zero, indicating that your application should simply retry the upload."
- **Namespace write lock**: "Write operations on a file within a namespace first acquire a lock on that namespace ... enough parallel
  threads writing to the namespace may create lock contention, resulting in the 429 `too_many_write_operations` exception."
  Suggested fix: batching (`upload_session/finish_batch`).
- **Forum counterpoint** [forum, `uploads-via-api-returning-many-too-many-write-operatios-error`]: lock-contention 429s often have no
  `Retry-After`. Staff suggest treating it as 0 and using exponential backoff. One rclone user reported `Retry-After` values of 15 s and
  300 s; staff attributed the 300 s case to very many calls for one user in a short period [forum, rclone thread].

### 4b. Published numbers

- Batch limit: "A batch can contain up to 1,000 entries" [doc, performance guide; SDK `finish_batch_v2` doc: "We allow up to 1000 entries"].
- Single-shot `files/upload`: "designed to work with files that are under 150 MBs" [doc].
- Chunked upload: "Consider uploading chunks in multiples of 4 MBs" [doc].
- Maximum file size through an upload session: `2^41 - 2^22` bytes (2,199,019,061,248) [SDK doc on `finish_batch_v2`].
- Help-centre file size limits: 350 GB for the API [Dropbox help, files-not-syncing article; not the same as the upload route ceiling above].
- **Business data-transport limit**: `finish_batch_v2` "will count as data transport calls for any Dropbox Business teams with a limit on
  the number of data transport calls allowed per month" [SDK doc]. Relevant only for Team accounts.
- No requests-per-second or requests-per-minute figures are published anywhere I found.

### 4c. How rclone handles them [code]

- **Pacer**: `fs.NewPacer(pacer.NewDefault(MinSleep=pacer_min_sleep (10 ms), MaxSleep=2 s, DecayConstant=2))` (`dropbox.go:550`, `:64-66`).
  Every API call is wrapped in `f.pacer.Call(...)` with `shouldRetry`.
- **`shouldRetry`** (`:473-492`):
  - `auth.RateLimitAPIError` with `RateLimitError.RetryAfter > 0` -> `pacer.RetryAfterError(err, RetryAfter)` (value taken from the JSON body's `error.retry_after`, not the HTTP header).
  - Any error string containing `too_many_write_operations`, `too_many_requests`, or empty -> retry.
  - Otherwise `fserrors.ShouldRetry(err)`.
  - `shouldRetryExclude` (`:449-469`): `insufficient_space` -> fatal; `malformed_path` -> no retry; context errors -> no retry.
- **Nil-pointer risk** [code, inferred]: `shouldRetry` dereferences `e.RateLimitError` without a nil check (`:480`). The SDK
  builds it from the body's `error` object (`auth/sdk.go:76-82`); a 429 body without an `error` object would leave it nil. Not observed.
- **SDK retries**: `dropbox.Config.RetryPolicy` is nil in rclone's config (`dropbox.go:560-564`), so the SDK's own retry layer
  (which does read `Retry-After` headers) is **not** active. [SDK `sdk.go` lines 97-99, 185-193; code]
- `batch_mode=sync` with default `--transfers` (4) keeps the number of in-flight batch members bounded, which is the documented
  mitigation for `too_many_write_operations` [rclone doc, "Batch mode uploads"].

---

## Q5. Case, path form, forbidden names

### 5a. `path_lower` vs `path_display` [SDK doc, `files/types.go` `Metadata`]

- `PathLower`: "The lowercased full path in the user's Dropbox. This always starts with a slash."
- `PathDisplay`: "The cased path to be used for display purposes only. In rare instances the casing will not correctly match the user's
  filesystem, but this behavior will match the path provided in the Core API v1, and at least the last path component will have the
  correct casing. Changes to only the casing of paths won't be returned by `listFolderContinue`."
- "Changes to only the casing ... won't be returned by `listFolderContinue`" is the load-bearing line: **a case-only rename on the
  server is invisible to change tracking.** Only a full re-list or a `get_metadata` on the new path will show it.
- Forum staff [forum, `files-get-metadata-has-incorrect-case-for-path-display`]: Dropbox is "case-insensitive but makes efforts to be case-preserving";
  only the last path component is guaranteed to have the correct case.

### 5b. rclone's handling [code/doc]

- `CaseInsensitive: true` (`dropbox.go:611`). rclone docs: "Dropbox is case insensitive so you can't have a file called 'Hello.doc' and one called 'hello.doc'" [rclone doc, Limitations].
- Listing uses `PathDisplay` leaf (`:1156-1158`). Operations send the caller's path with the encoder applied (`FromStandardPath`).
- **Consequence for a journal keyed by local path** [inferred]: on Linux, `A.txt` and `a.txt` are distinct local files. Both map to the
  same Dropbox path. Upload uses overwrite mode (`:2239`), so the second upload replaces the first on the server. The journal would hold
  two entries that point at one remote object. Key choice (local path vs lowercased path) must be decided explicitly.
- For ChangeNotify, path notifications are case-preserved but a case-only change produces nothing (see 5a).

### 5c. Forbidden characters and names

- **rclone default encoding** [code, `dropbox.go:313-317`; doc]: `Slash`, `BackSlash` (`\`), `Del` (0x7F), `RightSpace` (trailing space is
  replaced), `InvalidUtf8`, `Dot` (`.`/`..`). The NUL byte is encoded by `Base` (`lib/encoder/standard.go`: `EncodeZero`). Replacements are
  the fullwidth/Unicode look-alikes listed in `docs/content/dropbox.md` "Restricted filename characters".
- **Name length**: `checkPathLength` rejects any path component longer than 255 runes (`:2210-2223`, `maxFileNameLength` at `:93`).
- **Dropbox's own list** [doc, Dropbox help article "Files not syncing"]: names that do not sync include `desktop.ini`, `thumbs.db`,
  `.ds_store`, `icon\r`, `.dropbox`, `.dropbox.attr`; temporary names starting with `~$` or `.~`; "Certain special characters, like
  `*` (asterisk) or `/` (forward slash), aren't compatible with all devices." **The help article gives no full character list and does not
  mention trailing spaces or a maximum length.**

---

## Q6. OAuth for a user-registered Dropbox app

### 6a. App creation and access type [doc, rclone `docs/content/dropbox.md` "Get your own Dropbox App ID"; doc, Dropbox OAuth guide]

1. Create the app in the App console; choose "Dropbox API"; choose access type `Full Dropbox` or `App Folder`.
   rclone docs: "If you want to use Team Folders, `Full Dropbox` is required."
2. Enable permissions on the Permissions tab: rclone lists `account_info.read`, `files.metadata.write`, `files.content.write`,
   `files.content.read`, `sharing.write`; "The `files.metadata.read` and `sharing.read` checkboxes will be marked too."
3. Settings tab: add redirect URI `http://localhost:53682/` (rclone's loopback port). Copy App key (`client_id`) and App secret (`client_secret`).
   [rclone doc]
4. **Access type is fixed**: a forum post says an existing API app's access type cannot be switched; an unreleased app must be deleted
   and recreated [forum, "Restricting API token to a given root folder" / "Changing access type" thread, 2016-2017; treat as dated].

### 6b. App folder confinement

- [doc, Dropbox developer guide, `docs.dropboxapi.com/dropbox-api/docs/developer-resources/developer-guide`]:
  - "A dedicated folder named after your app is created within the Apps folder of a user's Dropbox."
  - "Your app gets read and write access to this folder only."
  - "Selecting App folder will grant you scoped access only to your app's dedicated folder."
  - Full Dropbox: "You get full access to all the files and folders in a user's Dropbox."
- **Answer to the "confine to `/Apps/<app>/`" question:** the official guide says read and write access is to "this folder only". It does not
  say, in so many words, that a token cannot address paths outside it. The forum evidence says an app-folder token treats `/Apps/<app>` as its
  implied root, and that the `Dropbox-API-Path-Root` header is rejected with "Path root is not supported for sandbox app"
  [forum, `400-error---path-root-is-not-supported-for-sandbox-app`]. **Not confirmed against a live app-folder token.**
- Some endpoints reject app-folder tokens: "`list_folder_members`" and "App folders also don't mix with shared folders, team folders, or
  team spaces" [forum, dated 2016-2017; treat as unverified].
- Consequence for the design: with App folder, `remote:` root = the app folder, so no namespace header is expected to be needed
  [inferred from forum]. For a full sync of the user's Dropbox the app must be "Full Dropbox".

### 6c. Scopes [doc, rclone `dropbox.go:102-111`; doc, dropbox.tech migration article]

- rclone's default request scopes: `files.metadata.write`, `files.content.write`, `files.content.read`, `sharing.write`, `account_info.read`
  (`dropbox.go:102-111`). Team scopes are added only with impersonation (`team_data.member`, `members.read`, `:152-168`).
- Scope names quoted in the dropbox.tech article: `account_info.read`, `files.metadata.read`, `files.content.read`, `files.content.write`,
  `sharing.read`, `sharing.write`, plus Team scopes. The article: "API calls to an endpoint without the appropriate scopes will throw an error."
- **Which scope each endpoint needs is not stated in any source I read** (the OAuth guide lists only categories: "Account Info", "Files and
  Folders", "Collaboration (sharing)", ...). For a list/read/write/delete/move/account set, the practical set is rclone's default list plus
  `files.metadata.read` (auto-added). This is inferred from rclone's behaviour, not from a Dropbox table.

### 6d. Tokens [doc: Dropbox OAuth guide, dropbox.tech offline-access article]

- Authorization request: `token_access_type=offline` for a refresh token; `response_type=code`. "The use of response_type=code is necessary for
  offline access." `redirect_uri` is optional; if unset, "the authorization code is displayed on dropbox.com for the user to copy and paste to
  your app." [doc, OAuth guide]
- Code exchange (`/oauth2/token`, `grant_type=authorization_code`): `code`, `client_id`, `client_secret` (confidential apps) or `code_verifier`
  (PKCE, `S256`). Returns `access_token`, `expires_in`, `token_type`, `scope`, `refresh_token`, `account_id`, `uid` [doc, offline-access article].
- Refresh (`grant_type=refresh_token`): `refresh_token`, `client_id`, and `client_secret` in the article's example. "Setting redirect_uri is not expected"
  [doc]. **The refresh response contains no new `refresh_token`** [doc].
- "The refresh token can be repeatedly re-used and doesn't expire automatically (though it can be revoked on demand)." [doc, offline-access article]
- "Dropbox access tokens are short lived, and will expire after a short period of time." The exact expiry is returned by `/oauth2/token`; no
  fixed number is given in the doc [doc, OAuth guide]. Forum/third-party examples show `expires_in` around 14399 s (about 4 h); not from Dropbox [forum/third-party].
- "Newly created scoped apps will default to short-lived tokens" and "Dropbox will retire the creation of long-lived access tokens" [doc, migration article].
- PKCE: "Using `S256` is the recommended approach." Auth URL includes `code_challenge` and `code_challenge_method` [doc, OAuth guide].
  **Whether a `client_secret` is needed for PKCE is not stated in the fetched doc**; the SDK's PKCE exchange sends the verifier with `client_id` only,
  and its refresh test asserts `client_secret` is empty [SDK, `oauth/oauth.go:553-560, :669-679`; `oauth/oauth_test.go:797-834`].

### 6e. SDK helpers homedrive could use [SDK, `dropbox/oauth/oauth.go`]

- `NewPKCEFlow(appKey, ...)`: PKCE, S256, `token_access_type=offline` by default (`newPKCEOptions`, `:553-560`).
- `NewOAuth2FlowNoRedirect(appKey, WithAppSecret|WithPKCE, ...)`: no-redirect flow. `Start()` returns the URL; `Finish(ctx, code)` exchanges the
  pasted code (`:222-237`, `:405-420`). **With `WithAppSecret`, `token_access_type` is omitted by default** (`oauth_test.go:270-284`), so
  `WithTokenAccessType(TokenAccessTypeOffline)` must be set explicitly to get a refresh token.
- `Refresh(ctx, appKey, token, ...)` and `TokenSource(ctx, appKey, token, ...)`: refresh with the app key; `client_secret` is sent only if
  `WithAppSecret` is set (`:501-534`, test `:834`).
- `oauthConfig` uses `AuthStyleInParams` (`:669-679`): client credentials are sent in the form body.

### 6f. How rclone does it [code/doc]

- Config wizard: `rclone config` -> dropbox -> `client_id`/`client_secret` (optional; rclone's built-in app used if blank) -> browser flow.
  rclone's Dropbox redirect is `http://localhost:53682/` (`lib/oauthutil/oauthutil.go:50,61-62`). Headless: run `rclone authorize` on a machine with a browser
  [rclone doc "remote_setup"; the Drive note `oauth-troubleshooting.md` describes the same flow].
- Config keys: `token` (JSON: `{"access_token":..., "token_type":"bearer", "expiry":..., "refresh_token":...}`), `client_id`, `client_secret`,
  `auth_url`, `token_url`, `client_credentials`, plus Team options [rclone doc; `dropbox.go:521-537` for legacy token conversion].
- Legacy string tokens are converted to the JSON form on NewFs (`dropbox.go:526-537`).
- The `token_access_type=offline` parameter is set by rclone's `OAuth2Opts` in the Config callback (`dropbox.go:177-184`); `NoOffline: true` stops
  oauthutil from adding Google's `access_type` (`lib/oauthutil/oauthutil.go:612`, `:899`).
- The rclone docs' wizard transcript names the prompts `app_key`/`app_secret` while the option names are `client_id`/`client_secret`; the doc says
  "The `App key` setting corresponds to `client_id`" [rclone doc]. Use the option names.

### 6g. Refresh failures

- **Dropbox documents no refresh error codes** [doc, OAuth guide: "The page doesn't document refresh failure error codes"; offline-access article:
  "The article lists no specific error codes, such as `invalid_grant`"].
- Forum reports [forum, `error-invalid-grant-error-description-refresh-token-is-malformed/712728`, `refresh-token-is-malformed/669304`]: `invalid_grant`
  with `error_description` "refresh token is malformed" in cases such as: the authorization code used as a refresh token; a revoked token; wrong
  app key/secret; a non-PKCE token refreshed without the app secret; `code_verifier` sent with `grant_type=refresh_token`.
- Expired access token on an API call: HTTP 401 with tag `expired_access_token` [forum, one report on `files/list_folder`]. Fix: refresh and retry.
  The SDK maps 401 to `AuthAPIError` (`auth/sdk.go:61-67`).
- rclone's wrapper (`lib/oauthutil/oauthutil.go:285-307`) decodes the JSON `error` field for HTTP 400/401 from the token endpoint and returns
  `"<error>: <suggestion>"`:
  - `invalid_client`, `unauthorized_client`, `unsupported_grant_type`, `invalid_scope` -> "if you're using your own client id/secret, make sure they're properly set up following the docs".
  - `invalid_grant` (and default) -> "maybe token expired? - try refreshing with \"rclone config reconnect <remote>:\"".
- `golang.org/x/oauth2` `*RetrieveError` carries `Response` and `Body` (rclone reads both at `oauthutil.go:287-292`). A startup validator can use that
  shape, as the Drive validator does [inferred].

---

## Q7. Binary size and dependency impact

**Method.** `go list -deps` on two scratch modules outside the repo, both with homedrive's `go.mod`/`go.sum` copied in (repo untouched):
(a) `import _ "github.com/rclone/rclone/backend/drive"`; (b) the same plus `_ ".../backend/dropbox"`. Both built for `linux/arm64`,
`-trimpath -ldflags="-s -w"`. Also built the real `homedrive/cmd/homedrive` into scratch for a baseline.

**Packages added by `backend/dropbox`** (20 total; diffed with `LC_ALL=C`; none removed):

- `github.com/dropbox/dropbox-sdk-go-unofficial/v6` and its subpackages: `dropbox`, `account`, `async`, `auth`, `common`, `contenthash`,
  `file_properties`, `files`, `retry`, `secondary_emails`, `seen_state`, `sharing`, `team`, `team_common`, `team_policies`, `users`, `users_common`.
- `github.com/rclone/rclone/backend/dropbox`, `.../backend/dropbox/dbhash`, `.../lib/batcher`.
- **No other third-party module is added.** The SDK itself requires `golang.org/x/oauth2 v0.30.0` (`go.mod`), which homedrive already has.

**Sizes** (arm64, stripped):

| Build | Size |
|---|---|
| minimal, `backend/drive` only | 20,578,464 bytes (~19.6 MiB) |
| minimal, `backend/drive` + `backend/dropbox` | 21,758,112 bytes (~20.7 MiB) |
| delta | 1,179,648 bytes (~1.13 MiB) |
| current `homedrive/cmd/homedrive` (real, baseline) | 22,151,328 bytes (~21.1 MiB) |
| **estimate** for real binary with Dropbox (baseline + delta) | ~23.3 MB (estimate, not measured) |

The CI gate is "< 25 MB" [`AGENTS.md`, `PLAN.md` §10]. The estimate is under it, but the margin is about 1.7 MB.

**Policy conflict (not a size issue).** `AGENTS.md` allows `backend/drive` as the only explicitly imported rclone backend and requires that
`grep` for `rclone/backend/` outside `drive` print nothing. Adding Dropbox fails that invariant and the allow-list. Whether to amend the
allow-list is a decision for the project owner, not something this note resolves.

---

## Q8. Maestral-specific facts

**Status.** The GitHub page shows "This repository was archived by the owner on Jun 28, 2026. It is now read-only." The README says
"As of 2026-07-28, this project is archived" and "Maestral will still remain usable in the medium term, but will no longer be actively
maintained or receive updates." The two dates conflict; I report both. A Daring Fireball link (2026-07-06) and MacRumors also report
retirement [secondary]. Source: `https://github.com/samschott/maestral`.

**Special names** [Maestral source, `src/maestral/constants.py`, `raw.githubusercontent.com/samschott/maestral/main/...`]:

- `OLD_REV_FILE = ".maestral"`, `FILE_CACHE = ".maestral.cache"`, `MIGNORE_FILE = ".mignore"`.
- `EXCLUDED_DIR_NAMES = frozenset([".dropbox.cache", FILE_CACHE])`.
- Excluded file names include `.dropbox`, `.dropbox.attr`, `.dropbox.cache`, `.DS_Store`, `.Spotlight-V100`, `.Trashes`, `.fseventd`,
  `.localized`, `.TemporaryItems`, `.com.apple.timemachine.supported`.
- The source does not say where these are created. The names are the exclusions a replacement must not sync.

**`.mignore`.** The name is confirmed (`MIGNORE_FILE`, and the README mentions a `.mignore` file for excluding local files from sync).
**Its syntax (gitignore-like or not) is not verified.** No Maestral doc I could fetch describes it.

**Selective sync.** The README names the CLI command `maestral excluded add|remove|list` ("Command group to manage excluded folders"). The semantics
(whether children of an excluded folder are also excluded, whether this is server-side or local-only) were not found.

**Config and state location.** The README gives the macOS path `~/Library/Application Support/maestral/maestral.ini`. The Linux location was not
verified. The constant `DEFAULT_CONFIG_NAME = "maestral"` is the only Linux-relevant name found.

**Dropbox ignore attribute** [doc, Dropbox help "How to set a file or folder to be ignored", `help.dropbox.com/en-us/sync/ignored-files`]:
`com.dropbox.ignored` with value `1`, set with `xattr -w` (macOS) or `attr -s com.dropbox.ignored -V 1` (Linux), removed with `attr -r`.
Effect: the item "stays in your Dropbox folder on your computer's hard drive" but is "removed from the Dropbox server and your other devices".
"Moving a file or folder into an ignored folder makes it ignored too." **Whether Maestral or a homedrive backend should honour this xattr is not
specified in any source**, and I did not check whether Maestral reads it.

---

## Local usage and discrepancies (homedrive repo)

- **Current rclone pin**: `homedrive/go.mod` pins `github.com/rclone/rclone v1.75.2`. The existing note `docs/oauth-troubleshooting.md` says it reads
  "`github.com/rclone/rclone v1.75.0`" and cites v1.75.0 source paths. **That note is stale against the current pin.** Its `lib/oauthutil` line numbers
  may differ at v1.75.2 (I re-checked the cited function at v1.75.2: `maybeWrapOAuthError` at `lib/oauthutil/oauthutil.go:285-307`).
- **Drive journal fields** (`store/journal.go:32-41`): `LocalMtime`, `RemoteMtime`, `RemoteMD5`, `RemoteID`, `LastSyncedAt`, `LastOrigin`, keyed by path.
  Each Dropbox field (`content_hash`, `client_modified` or `server_modified`, `rev`, `id`) differs from its Drive counterpart (see Q3, Q5).
- **Drive hash path**: `rcloneclient/remotefs.go:19-20` documents `MD5`; `rcloneclient/rclonefs.go:437` says MD5 extraction is omitted "until the
  Drive Changes API needs it" (this comment reads as if MD5 is unused for Drive today; the Drive Changes path fills `MD5` from `md5Checksum` in
  `driveapi.go:322`).
- **Typed backend assertion**: `rcloneclient/rclonefs.go:158` asserts `fsObj.(*drive.Fs)`. A Dropbox backend would need its own typed path or
  a backend-agnostic `RemoteFS` mapping; the assertion as written would fail for Dropbox.
- **OAuth refresh at startup**: `rcloneclient/oauthvalidate.go` already forces a token refresh before `buildFs` (`rclonefs.go:145`). A Dropbox
  validator can reuse the `*RetrieveError` shape; the existing Drive error mapping is in rclone's `maybeWrapOAuthError`.

---

## What I could not establish

1. **Does `include_deleted=false` (rclone's `ChangeNotify` cursor) suppress deletions from `continue`?** Dropbox staff say it is "effectively
   meaningless" for `continue` [forum]; another forum report says deletions still appear. No primary text settles it. Test against a live
   account before relying on it.
2. **Cursor lifetime** and the exact conditions that produce `reset` (SDK spec has no text for `reset`; forum-only causes).
3. **Ordering within a page** and whether a rename is always "deleted then added" in the same or adjacent pages (forum only).
4. **The exact longpoll timeout bounds** Dropbox enforces. The SDK doc gives "plus up to 90 seconds of jitter"; the 30-480 range is rclone's comment.
5. **Which OAuth scope each endpoint needs.** No per-endpoint table found.
6. **Refresh error codes** Dropbox returns (no official list; forum examples only).
7. **Whether PKCE refresh works without `client_secret` on the live API** (SDK behaviour and tests say yes; the fetched Dropbox page does not state it).
8. **Whether a token for an App-folder app is confined to `/Apps/<app>/` at the API level** (developer-guide text says "this folder only"; the path-root
   behaviour is forum-only and possibly dated).
9. **Rate-limit numbers** (none published), and whether `Retry-After` is always present on `too_many_write_operations` (Dropbox says yes; forum says no).
10. **Local hash throughput on the Pi** (Go uses SHA2 instructions on arm64 when present; the Pi's CPU feature set and speed were not measured).
11. **Real-binary size with Dropbox added** (only estimated: baseline 22.15 MB + 1.18 MB minimal-binary delta).
12. **Maestral's `.mignore` syntax**, its Linux config/state path, and whether it honours `com.dropbox.ignored`. Maestral's archive date is contradictory (Jun 28 vs Jul 28, 2026).
13. **Whether rclone's Dropbox backend has test coverage for `ChangeNotify`'s error path** (I did not examine `dropbox_internal_test.go` in depth).
14. **The `rclone authorize dropbox` headless command output** at v1.75.2 (the Drive flow is documented in `oauth-troubleshooting.md`; I did not run it).
15. **Whether `shared_folders`/team behaviour matches Maestral's** (not compared).

---

## When I looked

- **Date**: 2026-11-14.
- **rclone**: `v1.75.2` (module cache; `VERSION` file = `v1.75.2`; homedrive `go.mod` = `v1.75.2`).
- **Dropbox SDK**: `github.com/dropbox/dropbox-sdk-go-unofficial/v6 v6.4.0` (downloaded into the module cache with `go mod download`; SDK `sdkVersion = "6.4.0"`, spec `b5f7ddd`).
- **Go toolchain**: `go1.27.4 linux/amd64` (used for builds and to read the arm64 SHA-256 source).
- **Dropbox web docs**: fetched 2026-11-14. Several old URLs redirect (`dropbox.com/developers/...` and `developers.dropbox.com/...`) to
  `docs.dropboxapi.com/dropbox-api/docs/...`; the fetched pages are the redirect targets. The `dbx-rate-limits` URL returned 404; the
  performance guide covers the same material.
- **Forum posts** are dated 2016-2026; the age of each is noted where it matters.
- **Repo state**: read-only. `git status` was clean before and after the scratch builds. The `go mod download` call did not change `go.mod` or `go.sum`.

---

## Sources

Dropbox documentation (primary, fetched 2026-11-14):

- Content hash: https://docs.dropboxapi.com/dropbox-api/docs/technical-reference/content-hash (old: https://www.dropbox.com/developers/reference/content-hash)
- Performance guide (rate limits, 429, batching, upload sizes): https://docs.dropboxapi.com/dropbox-api/docs/performance (old: https://developers.dropbox.com/dbx-performance-guide)
- OAuth guide (App folder vs Full Dropbox, no-redirect code, PKCE, refresh): https://docs.dropboxapi.com/dropbox-api/docs/oauth (old: https://developers.dropbox.com/oauth-guide)
- Developer guide (app folder "this folder only"): https://docs.dropboxapi.com/dropbox-api/docs/developer-resources/developer-guide
- Using OAuth 2.0 with offline access (refresh tokens, response types, refresh request/response): https://dropbox.tech/developers/using-oauth-2-0-with-offline-access
- Migrating app permissions and access tokens (scope names, short-lived tokens): https://dropbox.tech/developers/migrating-app-permissions-and-access-tokens
- Ignored files: https://help.dropbox.com/en-us/sync/ignored-files
- Files not syncing: https://help.dropbox.com/installs-integrations/sync-uploads/files-not-syncing
- 
Dropbox SDK (official, `github.com/dropbox/dropbox-sdk-go-unofficial/v6@v6.4.0`; doc comments generated from Dropbox's spec):

- `dropbox/files/types.go`: `ListFolderArg`, `ListFolderLongpollArg`, `ListFolderLongpollResult`, `ListFolderContinueErrorReset`, `FileMetadata`, `Metadata`, `DeletedMetadata`.
- `dropbox/files/client.go`: `ListFolderLongpollContext` (`:2492-2501`, host `notify`, `Auth: "noauth"`), `UploadSessionFinishBatchV2Context` doc (`:851-866`).
- `dropbox/sdk.go`: `Config` (TokenSource, Client, PathRoot, RetryPolicy), `WithRoot`/`WithNamespaceID`, `newHTTPRequest`.
- `dropbox/auth/sdk.go`, `dropbox/auth/types.go`: 429 mapping to `RateLimitAPIError`; `RateLimitError`.
- `dropbox/oauth/oauth.go`, `dropbox/oauth/oauth_test.go`: PKCE, no-redirect, refresh, defaults.

rclone v1.75.2 (`github.com/rclone/rclone@v1.75.2`):

- `backend/dropbox/dropbox.go` (options `:99-381`; Fs/Object `:384-426`; `shouldRetry` `:449-492`; `NewFs` `:514-703`; `ListP` `:1081-1204`;
  Put/Update `:2230-2272`; Move/Copy/DirMove `:1339-1575`; Hash `:1790-1881`; ChangeNotify `:1613-1787`; upload `:2061-2198`; `checkPathLength` `:2210-2223`).
- `backend/dropbox/batcher.go`, `backend/dropbox/dbhash/dbhash.go`.
- `docs/content/dropbox.md` (options, batch modes, limitations, "Get your own Dropbox App ID"). Public copy: https://rclone.org/dropbox/
- `lib/batcher/batcher.go`, `lib/batcher/options.go`.
- `lib/oauthutil/oauthutil.go` (`bindPort` `:50`, redirect `:56-62`, `NoOffline` `:612`/`:899`, `maybeWrapOAuthError` `:285-307`).
- `lib/encoder/standard.go` (`Base`).
- `fs/features.go` (`Fill`, `ChangeNotifier`).

Go toolchain: `crypto/internal/fips140/sha256/sha256block_arm64.go` (`useSHA2 = cpu.ARM64HasSHA2`).

Maestral (secondary status; source for names):

- Repository and status: https://github.com/samschott/maestral
- Constants (special names): https://raw.githubusercontent.com/samschott/maestral/main/src/maestral/constants.py
- Coverage of retirement (secondary): https://daringfireball.net/linked/2026/07/06/maestral-retired ; https://www.macrumors.com/2026/07/07/free-dropbox-client-maestral-retired/

Forum posts (community evidence; not contracts):

- Cursors and deleted entries: https://www.dropboxforum.com/t5/Dropbox-API-Support-Feedback/list-folder-continue-and-get-latest-cursor-are-returning-deleted/m-p/701928
- Renames and move handling: https://www.dropboxforum.com/t5/Dropbox-API-Support-Feedback/Handling-File-Rename-Move-in-the-API-filesListFolderContinue/m-p/427701
- Cursor reset and recovery: https://www.dropboxforum.com/discussions/101000042/help-understanding-how-fileslist-foldercontinue/457381
- Lock contention and Retry-After: https://www.dropboxforum.com/discussions/101000014/uploads-via-api-returning-many-too-many-write-operatios-error/819547
- Case in path_display: https://www.dropboxforum.com/t5/Dropbox-API-Support-Feedback/files-get-metadata-has-incorrect-case-for-path-display/m-p/186436
- App folder path root rejection: https://www.dropboxforum.com/discussions/101000014/400-error---path-root-is-not-supported-for-sandbox-app/769690
- Refresh token malformed: https://www.dropboxforum.com/discussions/101000042/error-invalid-grant-error-description-refresh-token-is-malformed/712728
- Access type not switchable: https://www.dropboxforum.com/discussions/101000014/restricting-api-token-to-a-given-root-folder/644192 (dated 2016-2017)

homedrive repo (read-only): `homedrive/go.mod`, `homedrive/PLAN.md` §5.3, §7, §10, §11, `homedrive/AGENTS.md` (rclone allow-list, CI invariants),
`homedrive/internal/rcloneclient/{remotefs.go,rclonefs.go,driveapi.go,oauthvalidate.go}`, `homedrive/internal/store/journal.go`,
`homedrive/docs/oauth-troubleshooting.md`.
