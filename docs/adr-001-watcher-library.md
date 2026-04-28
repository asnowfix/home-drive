# ADR-001: Watcher library — fsnotify

**Status**: Accepted
**Date**: 2026-04-28
**Decision-makers**: project owner

## Context

`homedrive` needs a filesystem watcher to detect local changes and
trigger uploads to Drive. On Linux ARM64, the only viable option is a
library that wraps inotify.

Two candidates were considered:

1. `github.com/fsnotify/fsnotify`
2. `github.com/rjeczalik/notify`

## Decision

Use **`fsnotify/fsnotify`** with manual recursive walk on startup and
dynamic `Watcher.Add` on directory `Create` events.

## Rationale

1. On Linux, `notify`'s "native recursive" advantage is illusory — it
   still uses inotify under the hood and walks subdirectories
   internally. Same kernel constraints (`fs.inotify.max_user_watches`),
   no real benefit for our target.
2. `fsnotify` is actively maintained and used by the Kubernetes, Helm,
   and Hugo ecosystems. Bug fixes flow from a much larger user base.
3. The lower-level API gives explicit control over the rename-of-folder
   edge case, which we must handle anyway via cookie pairing
   (see PLAN.md §6 and the `homedrive-watcher-rename` skill).
4. Lower dependency surface — `notify` adds a layer of abstraction for a
   problem we don't have on Linux.

## Consequences

- The watcher must implement its own recursive walk on startup (handled
  in Phase 1).
- Newly-created directories must be added to the watch set before any
  child events fire (handled by AddWatch on `Create` of a directory).
- Cross-platform support (macOS, Windows) would require additional
  build-tagged code, but is out of scope for v0.1.

## Validation

Phase 1 includes tests for:
- 50k file burst creation → no missed events.
- `mv dir_5k other_dir` → exactly 1 paired `DirRename` event.
- `mv` across mount points → graceful fallback to per-file handling.

If any of these fail, this ADR will be revisited.

## References

- PLAN.md §5 (Watcher design notes)
- PLAN.md §6 (Directory rename handling)
- `.claude/skills/homedrive-watcher-rename/SKILL.md`
