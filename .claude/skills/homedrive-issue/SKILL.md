---
name: homedrive-issue
description: Standard format for creating GitHub issues for homedrive phases and features via gh CLI, with labels, milestones, and PLAN.md cross-references. Apply only when the user explicitly asks to create an issue.
---

# Creating issues for homedrive

Use the `gh` CLI to create issues with consistent format.

## Triggering

Use this skill **only** when the user explicitly asks to create an issue
("create an issue for X", "open a ticket for phase 2", etc.). Don't
proactively create issues — the user is in control.

## Standard command

```bash
gh issue create \
  --repo asnowfix/home-drive \
  --title "[homedrive] <Title from PLAN.md phase or feature>" \
  --label "homedrive,<functional-label>" \
  --body "$BODY"
```

## Functional labels

Match the parent home-automation repo conventions:

| Label | Use for |
|---|---|
| `core-architecture` | Watcher, syncer, store internals |
| `integrations` | rclone wrapper, Drive API, MQTT, HA Discovery |
| `monitoring` | HTTP endpoint, metrics, audit log |
| `packaging` | systemd, sysctl, logrotate, postinst |
| `enhancement` | New features (default if no other fits) |
| `bug` | Defects |
| `documentation` | Docs-only changes |
| `tests` | Test infrastructure |
| `ci` | GitHub Actions, dependabot |

Always include `homedrive` plus exactly one functional label.

## Body template for phase issues

```markdown
## Phase

Phase N from `PLAN.md` §14: <title>.

## Acceptance criteria

(Copy from PLAN.md §14 phase description.)

- [ ] Item 1
- [ ] Item 2
...

## Tests

(Copy from PLAN.md §16.3 if relevant.)

- [ ] Test 1
- [ ] Test 2
...

## References

- `PLAN.md` §<section number>
- Skill: `.claude/skills/homedrive-<skill>`

## Definition of done

- All acceptance criteria checked.
- Tests pass on `linux/amd64` and `linux/arm64` (CI green).
- Coverage > 70% for the package.
- Binary < 25 MB stripped.
- Exactly 1 rclone backend in the binary.
- PLAN.md updated to mark phase complete.
```

## Body template for feature issues

```markdown
## Context

Why this is needed, what it enables.

## Proposed change

What changes in the code.

## Affected packages

- `internal/<pkg>/`
- ...

## Tests

What tests this requires.

## References

- Related PLAN.md section: §<N>
- Related skill: `.claude/skills/homedrive-<skill>`
```

## Linking to PLAN.md

Always link to the relevant PLAN.md section:

```
PLAN.md §14 phase 1 (https://github.com/asnowfix/home-drive/blob/main/PLAN.md#phase-1)
```

## Milestones

Create milestones matching releases:
- `v0.1.0` — phases 0–13.
- `v0.2.0` — peer sync (future).

Assign each phase issue to its milestone.

## Project board (optional)

If a GitHub project is set up, add issues to it via:
```bash
gh issue create ... | xargs gh project item-add <project-number> --owner asnowfix --url
```

## What NOT to do

- Don't create issues without checking PLAN.md for an existing phase
  description.
- Don't use generic labels like `priority/high` — they don't exist in
  this repo.
- Don't create a single "do all of homedrive" mega-issue. One per phase.
