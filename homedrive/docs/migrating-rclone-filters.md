# Migrating rclone filters to `watcher.exclude`

This is a reference for anyone coming from a hand-rolled `rclone sync` /
`rclone bisync` setup that used `--filter`, `--include`, `--exclude`,
`--filter-from`, or size/age filters, and wants to know how (or whether)
those rules translate to `homedrive`'s `watcher.exclude` config field.

See [PLAN.md §5](../PLAN.md#5-watcher-design-notes-no-spike) (push-side
watcher design) and [§7](../PLAN.md#7-pull-strategy-hybrid-changes-api--bisync)
(pull-side Changes API + bisync) for where exclusion is enforced in each
direction, and issue [#54](https://github.com/asnowfix/home-drive/issues/54)
for the audit that produced this doc.

## The model, in one paragraph

`homedrive` has exactly one filtering knob: `watcher.exclude`, a flat list
of [doublestar](https://github.com/bmatcuk/doublestar) glob patterns in
config. A path matching any pattern is excluded from **both** directions of
sync -- it is never pushed to Drive by the watcher, and never pulled from
Drive by the Changes-API poller or the full walk. There is no ordering, no
include rules, no size/age/date filters, and no per-remote filter files.
The matcher lives in one place, `internal/pathfilter`, and both
`internal/watcher` (push side) and `internal/rcloneclient` (pull side) call
it, so the same pattern list behaves identically regardless of which
direction is evaluating it.

## How a pattern is matched

Given a pattern and a path relative to the sync root (forward-slash
separated), `pathfilter.Excluded` tries three tiers, in order, and returns
true on the first match:

1. **Direct match** -- `doublestar.Match(pattern, relPath)`.
2. **Directory match** -- `doublestar.Match(pattern, relPath+"/")`, so a
   pattern without a trailing `/**` (e.g. `**/node_modules`) still matches
   the directory itself, not just entries inside it.
3. **Bare-directory match** -- for a pattern ending in `/**` (e.g.
   `**/.git/**`), the pattern with `/**` stripped is also matched against
   `relPath`, so the directory itself (`.git`) is excluded, not just its
   contents (`.git/HEAD`).

A leading `/` on the path being tested is stripped before matching, and an
empty pattern list always matches nothing (everything syncs).

## Translating common rclone filter rules

rclone's filter syntax is richer than `watcher.exclude` (ordered
include/exclude rules, `-` / `+` prefixes, filter files, size and age
filters). Most simple exclude rules have a direct doublestar equivalent;
a few common ones:

| rclone rule | homedrive `watcher.exclude` entry | Notes |
|---|---|---|
| `- .git/**` | `"**/.git/**"` | Also excludes `.git` itself (tier 3 above); rclone's rule alone would not. |
| `- /some/path/**` | `"some/path/**"` | rclone's leading `/` anchors to the root of the remote; homedrive paths are already root-relative, so drop the leading `/`. |
| `- *.tmp` | `"**/*.tmp"` | rclone's bare `*.tmp` (no leading `/`) matches at any depth by default; homedrive's `*.tmp` (no `**/` prefix) matches **only** at the sync root -- use `**/*.tmp` to match at any depth. |
| `- node_modules/**` | `"**/node_modules/**"` | Same anchoring caveat as above: add `**/` unless you specifically mean "only at the root." |
| `--exclude-from filter.txt` (flat exclude list, no includes) | Copy each line into `watcher.exclude`, applying the two translations above | Only works if `filter.txt` contains pure excludes with no `+` include rules -- see the callout below. |
| `--min-size 10M` / `--max-age 30d` | **Not supported** | No size or age filtering exists in homedrive. Exclude by path/glob only. |
| `--filter '+ *.pdf' / '- *'` (include-only allowlist) | **Not supported** | See the callout below. |

## No include/allow-list support (read this before migrating)

**homedrive has no include-rule or allow-list filtering.** `watcher.exclude`
is exclude-only: every path under `local_root` syncs by default, and a
pattern match removes it from sync. There is no way to express "only sync
these specific subfolders" or "sync everything except X, except also
re-include Y inside X" -- rclone's ordered `+`/`-` filter chains have no
homedrive equivalent today.

If your legacy rclone setup relied on an allowlist (e.g. `--filter-from`
with `+ /Documents/**`, `+ /Photos/**`, `- *` to sync only two top-level
folders out of a much larger Drive), there is no direct migration path.
Workarounds, none of which are drop-in:

- Point `local_root` at a narrower directory that already contains only
  the folders you want synced (requires restructuring the local tree or
  the Drive remote).
- Run multiple `homedrive` instances against different `local_root` /
  `remote` pairs, one per folder you want included -- unsupported/untested
  configuration, not a documented deployment shape.

This is a known gap, not a design decision to keep permanently closed --
if you need include-based filtering, please open a new issue describing
the use case rather than trying to approximate it with deeply nested
exclude globs.

## homedrive's own default excludes

For reference, the shipped default in
[`linux/config.yaml`](../linux/config.yaml) covers version-control
metadata, OS/editor cruft, and common build artifacts:

```yaml
watcher:
  exclude:
    - "**/.git/**"
    - "**/.svn/**"
    - "**/.hg/**"
    - "**/.DS_Store"
    - "**/Thumbs.db"
    - "**/desktop.ini"
    - "**/*.swp"
    - "**/*.swo"
    - "**/*~"
    - "**/.#*"
    - "**/~$*"
    - "**/.~lock.*"
    - "**/node_modules/**"
    - "**/.venv/**"
    - "**/__pycache__/**"
    - "**/.idea/**"
    - "**/.vscode/**"
    - "**/*.tmp"
    - "**/*.partial"
    - "**/*.crdownload"
```

Add your own patterns to this list; there is no separate "user" vs.
"default" filter layer -- `watcher.exclude` in `config.yaml` is the
complete, authoritative list.

## Verifying push/pull parity

Because `internal/pathfilter.Excluded` is the single matcher called by
both sides, a pattern that excludes a path from the watcher (push) also
excludes it from the Drive Changes API poll and full walk (pull) -- see
`TestFullWalkThenResume_ExcludesPatterns`,
`TestPollChanges_ExcludedPathSkipped`, and
`TestListChanges_RemoteOnlyExcludedFileNotPulled` in
`internal/rcloneclient` for the pull-side tests, and
`TestFilter_Excluded` in `internal/watcher` for the push-side tests.
