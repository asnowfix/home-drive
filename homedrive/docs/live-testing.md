# Live testing against real Drive

A dev host may run a live agent (system unit or `systemctl --user` unit)
against its real `local_root`, for example `~/gdrive` syncing `gdrive:Brain`.
Two agents on one `local_root` race each other's journal (loop prevention,
PLAN.md section 7.3; `.old.<N>` numbering, section 11), so never install,
enable or start a second one there.

## Exercising a build with an isolated instance

1. Build the branch under test into a scratch directory (`go build -o
   <scratch>/homedrive ./homedrive/cmd/homedrive`).
2. Copy the live `config.yaml` to a scratch file and change:
   `local_root` (scratch directory), `remote` (a Drive sub-folder such as
   `gdrive:Brain/<test-dir>`), `state.path`, `state.audit_log`, and
   `http.listen` (a free port, e.g. `127.0.0.1:6091`). A shorter
   `pull.changes_api_interval` (e.g. `10s`) speeds the test up.
3. Create fixtures on Drive with `rclone` (e.g. a dangling shortcut:
   `rclone backend shortcut gdrive: <src> <dst>` then delete the target with
   `--drive-use-trash=false`).
4. Run `homedrive run --config <scratch>/cfg.yaml` and read its JSON log and
   `/status`.

## Pitfalls

- **The live agent syncs the parent folder.** A test sub-folder under
  `gdrive:Brain` is also pulled into the live `local_root` and the live
  agent pushes what it pulls. Expect it, and clean up on both sides.
- **Clean up.** Stop the instance, `rclone purge` the test folder (use
  `--drive-use-trash=false` for your own artifacts), remove the empty
  directory that the live agent leaves locally, and confirm Drive and
  `local_root` hold only the original files.
- **Drive listings lag.** A file deleted moments earlier can still be
  listed by the first full walk.
- **Do not `pkill -f` a pattern that also matches your own command line**
  (it kills the shell running it). Kill by PID.
- Tests in CI never use real rclone or Google; this procedure is for manual
  verification only.
