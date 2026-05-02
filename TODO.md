# TODO — Post-implementation manual steps

## Merge PRs

Merge in dependency order:

1. [ ] PR #5 — Phase 0: Bootstrap module
2. [ ] PR #8 — Phase 1: Watcher + rename pairer
3. [ ] PR #7 — Phase 2: Rclone wrapper
4. [ ] PR #6 — Phase 3: Store + conflict resolution
5. [ ] PR #9 — Phase 4: MQTT wrapper
6. [ ] PR #13 — Phase 5: Push syncer
7. [ ] PR #15 — Phase 6: Pull Changes API
8. [ ] PR #14 — Phase 7: Bisync safety net
9. [ ] PR #11 — Phase 8: HTTP control
10. [ ] PR #19 — Phase 9: HA Discovery
11. [ ] PR #18 — Phase 10: Quota handling
12. [ ] PR #16 — Phase 11: Systemd packaging
13. [ ] PR #17 — Phase 12: CI GitHub Actions
14. [ ] PR #21 — Phase 13: Docs + release

## Interface alignment

- [ ] Wire local interface copies in each package to canonical definitions during merge (each phase defined its own `RemoteFS`, `Publisher`, `Store` interfaces locally)
- [ ] Add `List(ctx context.Context, dir string) ([]RemoteObject, error)` method to canonical `RemoteFS` interface in `internal/rcloneclient/` (required by bisync, Phase 7)

## CI fixes

- [ ] Adjust rclone backend CI check — `crypt` is a transitive dependency of `drive`, so the `go tool nm` count will be >1; check should verify only `drive` is explicitly imported (noted in Phase 2, PR #7)

## Release

- [ ] Tag `homedrive/v0.1.0` after all PRs are merged
- [ ] Update root `RELEASE_NOTES.md`
