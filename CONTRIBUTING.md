# Contributing to homedrive

## Workflow

`homedrive` ships in atomic phases — one PR per phase from `PLAN.md` §14.
The Claude Code agent (`.claude/agents/homedrive-implementer.md`) is
configured to follow this discipline; please mirror it for hand-written
contributions too.

### Per-phase checklist

1. Open an issue for the phase using the `homedrive-issue` skill or the
   template in `.claude/skills/homedrive-issue/SKILL.md`.
2. Branch: `phase-N-<short-title>`.
3. Read the relevant skill(s) in `.claude/skills/`.
4. Implement, with tests.
5. Run locally inside OrbStack ([orbstack.dev](https://orbstack.dev)) —
   the project requires real Linux inotify semantics not available on macOS:
   ```bash
   orb run -m dev -- go test -race ./homedrive/...
   ```
6. Verify invariants:
   ```bash
   GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" \
     -o homedrive-bin ./homedrive/cmd/homedrive
   du -h homedrive-bin                                # < 25 MB
   go tool nm homedrive-bin | grep -c rclone/backend  # = 1
   ```
7. Update `PLAN.md` §14 to tick the phase complete.
8. Open the PR, link the issue, paste test output.

## Code conventions

See `.claude/skills/homedrive-conventions/SKILL.md`. Highlights:

- `slog` only — no `fmt.Println`.
- Files < 500 lines, functions < 80 lines.
- Table-driven tests, naming `TestXxx_Case`.
- No `panic` outside `main`.
- rclone imports per the allow-list.

## Testing

See `.claude/skills/homedrive-test-mocks/SKILL.md`.

- Use `MemFS` and `FlakyFS`; never the real Drive API.
- Embedded `mochi-mqtt/server` for MQTT tests.
- Coverage gate: 70%.

## Reviewing PRs

Reject PRs that:
- Combine multiple phases.
- Skip tests for any scenario listed in `PLAN.md` §16.3.
- Push the binary over 25 MB.
- Add rclone backends beyond `drive`.
- Use `fmt.Println` or unstructured logging.
- Add MQTT subscriptions in v0.1 (publish-only).

## Releases

Tags: `homedrive/v0.1.0`, `homedrive/v0.2.0`, etc.

Each release ships:
- A goreleaser-built binary for `linux/amd64` and `linux/arm64`.
- nfpm `.deb` and `.rpm` packages with the systemd unit, sysctl, and
  logrotate config installed.

## Questions

Open a GitHub Discussion or comment on the relevant phase issue.
