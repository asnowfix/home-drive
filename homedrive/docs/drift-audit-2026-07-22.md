# Drift audit — 2026-07-22

Part of #48 (migration umbrella #47). Read-only reconciliation between the
production NAS's `/home/fix/gdrive` and the `gdrive:` remote, run with
`homedrive/scripts/audit-drift.sh` (`rclone check --combined`, no mutating
command). Full raw report (unredacted paths) is kept on the NAS at
`/home/fix/homedrive-audits/homedrive-audit-2026-07-22.txt` — not committed
here since paths include personal file/folder names and this repo is public.

## Result

- **203 files identical** on both sides. Zero files exist on both sides with
  differing content (no `rclone check` "differs" markers at all).
- **22 files across 4 paths were renamed locally** (typographic dash/quote —
  em-dash "—", en-dash "–", curly apostrophe "'" — replaced with plain ASCII
  "-"/"'") at some point after the 2026-06-02 initial one-shot pull. Since
  the push watcher was never wired up (see #49), these renames were never
  propagated to Drive; Drive still holds the pre-rename names. Verified for
  every affected path that the file count matches exactly on both sides —
  these are clean renames, not data loss. No action needed from this audit;
  once push is wired (#49) and a local rename replays as a rename op (see
  `homedrive/PLAN.md` §6), these should reconcile automatically.
- **3 items exist only on Drive**, created/edited after the pull stopped
  working correctly:
  - One markdown file has **three duplicate copies** in Drive (same name,
    same folder), all dated 2026-07-12, with three different byte sizes —
    real content edits made directly in Drive after the pull broke. **Needs
    a human decision** on which copy (if any) is authoritative before pull
    is resumed against this path, since Drive permits duplicate names in one
    folder but the local filesystem and homedrive's path-keyed store do not.
  - One Google Doc has **two duplicate copies**, dated 2026-07-17. Same
    duplicate-name situation as above, lower priority (holiday-planning
    content).
  - One old **dangling Google Drive shortcut** (0 bytes, dated 2022-05-26,
    points to a deleted/inaccessible target) — pre-existing cruft unrelated
    to the sync gap, not new drift. Recommend excluding shortcuts from pull
    going forward rather than treating this as content to sync.

## Follow-ups for sibling issues

- #49/#50: the sync engine's remote-listing path should tolerate (or at
  least not crash on) duplicate file names within one Drive folder — this
  audit found a real occurrence of it, not just a theoretical edge case.
- Before #49/#50 land and pull/push go live, the NAS owner should manually
  resolve the two Drive-side duplicate-name cases above (keep one copy,
  remove the others) so the first live pull doesn't have to guess.

## Commands run

Only read-only listing/check commands were executed against the NAS:
`rclone check ... --combined`, `rclone lsf`, `rclone lsl`. No `sync`,
`copy`, `bisync`, or `move` was run.
