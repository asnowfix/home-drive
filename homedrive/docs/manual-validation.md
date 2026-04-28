# Manual validation checklist

This document provides an end-to-end test checklist for manual
validation of homedrive on a production Raspberry Pi. Run through these
steps after deploying a new build to verify correct behavior before
tagging a release.

## Prerequisites

- homedrive deployed and running on the Pi (`systemctl status homedrive@fix.service`)
- `rclone.conf` configured with a working Google Drive remote
- A test directory under `local_root` (e.g., `/mnt/external/gdrive/test-validation/`)
- MQTT broker running and accessible
- Home Assistant available with MQTT integration (for HA-specific checks)
- A web browser open to Google Drive for visual verification

## 1. Basic connectivity

- [ ] `homedrive ctl status` returns JSON with `"state": "running"`
- [ ] `curl http://127.0.0.1:6090/healthz` returns HTTP 200
- [ ] `curl http://127.0.0.1:6090/status` returns JSON with version,
      queue sizes, and last sync timestamps
- [ ] MQTT online sensor shows `online` in Home Assistant
- [ ] `journalctl -u homedrive@fix.service --no-pager -n 20` shows
      structured JSON logs at the configured level

## 2. File create and push

- [ ] Create a new file:
      `echo "test content" > /mnt/external/gdrive/test-validation/test-create.txt`
- [ ] Wait 5 seconds (debounce 2s + sync time)
- [ ] Verify the file appears in Google Drive (web UI or
      `rclone ls gdrive:test-validation/`)
- [ ] `homedrive ctl status` shows updated `last_push` timestamp
- [ ] MQTT `push.success` event received (check HA or `mosquitto_sub`)
- [ ] Audit log contains the push entry:
      `tail -1 /var/log/homedrive/audit.jsonl`

## 3. File modify and push

- [ ] Modify the file:
      `echo "modified content" >> /mnt/external/gdrive/test-validation/test-create.txt`
- [ ] Wait 5 seconds
- [ ] Verify the updated content in Google Drive:
      `rclone cat gdrive:test-validation/test-create.txt`
- [ ] MQTT `push.success` event received

## 4. File delete and push

- [ ] Delete the file:
      `rm /mnt/external/gdrive/test-validation/test-create.txt`
- [ ] Wait 5 seconds
- [ ] Verify the file is removed from Google Drive
- [ ] MQTT `push.success` event received
- [ ] Audit log contains the delete entry

## 5. Remote create and pull

- [ ] Create a file on Drive:
      `rclone copyto --no-check-dest /tmp/test-remote.txt gdrive:test-validation/test-remote.txt`
- [ ] Wait 35 seconds (pull interval 30s + processing)
- [ ] Verify the file appears locally:
      `ls -la /mnt/external/gdrive/test-validation/test-remote.txt`
- [ ] `homedrive ctl status` shows updated `last_pull` timestamp
- [ ] MQTT `pull.success` event received

## 6. Remote modify and pull

- [ ] Modify the file on Drive:
      `echo "remote edit" | rclone rcat gdrive:test-validation/test-remote.txt`
- [ ] Wait 35 seconds
- [ ] Verify the local file has the updated content:
      `cat /mnt/external/gdrive/test-validation/test-remote.txt`
- [ ] MQTT `pull.success` event received

## 7. Remote delete and pull

- [ ] Delete the file on Drive:
      `rclone deletefile gdrive:test-validation/test-remote.txt`
- [ ] Wait 35 seconds
- [ ] Verify the local file is removed
- [ ] MQTT `pull.success` event received

## 8. Directory rename

- [ ] Create a directory with several files:
      ```
      mkdir -p /mnt/external/gdrive/test-validation/rename-source
      for i in $(seq 1 20); do
        echo "file $i" > /mnt/external/gdrive/test-validation/rename-source/file-$i.txt
      done
      ```
- [ ] Wait for all files to sync (check `homedrive ctl status` queue
      counts reach 0)
- [ ] Rename the directory:
      `mv /mnt/external/gdrive/test-validation/rename-source /mnt/external/gdrive/test-validation/rename-dest`
- [ ] Wait 5 seconds
- [ ] Verify on Drive that the folder is renamed (not deleted and
      recreated):
      - The Drive file IDs of the contained files should be unchanged
      - `rclone ls gdrive:test-validation/rename-dest/` shows all 20 files
      - `rclone ls gdrive:test-validation/rename-source/` shows nothing
        or errors
- [ ] MQTT `dir_rename` event received (exactly 1, not 20 push events)
- [ ] Audit log contains a single `dir_rename` entry with
      `files_count: 20`
- [ ] Clean up:
      `rm -rf /mnt/external/gdrive/test-validation/rename-dest`

## 9. Conflict resolution

- [ ] Create a conflict scenario:
      1. Pause the agent: `homedrive ctl pause`
      2. Create a file locally:
         `echo "local version" > /mnt/external/gdrive/test-validation/conflict.txt`
      3. Create the same file on Drive with different content:
         `echo "remote version" | rclone rcat gdrive:test-validation/conflict.txt`
      4. Resume the agent: `homedrive ctl resume`
- [ ] Wait 35 seconds for the pull + push cycle
- [ ] Check which version won (depends on mtime ordering):
      `cat /mnt/external/gdrive/test-validation/conflict.txt`
- [ ] Verify a `.old.1` backup exists for the loser:
      `ls /mnt/external/gdrive/test-validation/conflict.txt.old.*`
      or check Drive for a `.old.1` file
- [ ] MQTT `conflict.detected` and `conflict.resolved` events received
- [ ] Audit log contains the conflict entry
- [ ] Clean up:
      `rm /mnt/external/gdrive/test-validation/conflict.txt*`
      `rclone delete gdrive:test-validation/conflict.txt`

## 10. Exclusion filters

- [ ] Create excluded files:
      ```
      echo "temp" > /mnt/external/gdrive/test-validation/file.swp
      echo "temp" > /mnt/external/gdrive/test-validation/file.tmp
      mkdir -p /mnt/external/gdrive/test-validation/.git
      echo "ref" > /mnt/external/gdrive/test-validation/.git/HEAD
      ```
- [ ] Wait 10 seconds
- [ ] Verify none of these appear on Drive:
      `rclone ls gdrive:test-validation/` should not list `.swp`,
      `.tmp`, or `.git/`
- [ ] Clean up:
      `rm -rf /mnt/external/gdrive/test-validation/file.swp /mnt/external/gdrive/test-validation/file.tmp /mnt/external/gdrive/test-validation/.git`

## 11. Pause and resume

- [ ] Pause: `homedrive ctl pause`
- [ ] `homedrive ctl status` shows `"state": "paused"`
- [ ] Create a file:
      `echo "while paused" > /mnt/external/gdrive/test-validation/paused-file.txt`
- [ ] Wait 10 seconds
- [ ] Verify the file is NOT on Drive yet
- [ ] Resume: `homedrive ctl resume`
- [ ] Wait 5 seconds
- [ ] Verify the file now appears on Drive
- [ ] Clean up

## 12. Forced resync

- [ ] `homedrive ctl resync`
- [ ] Watch logs for the bisync pass:
      `journalctl -u homedrive@fix.service -f` -- look for bisync start
      and completion messages
- [ ] Verify `homedrive ctl status` shows updated bisync timestamp

## 13. Dry-run mode

- [ ] Stop the service: `sudo systemctl stop homedrive@fix.service`
- [ ] Start in dry-run mode:
      `homedrive run --dry-run --config /etc/homedrive/config.yaml`
- [ ] Create a file:
      `echo "dry run" > /mnt/external/gdrive/test-validation/dry-test.txt`
- [ ] Wait 5 seconds
- [ ] Check logs -- should show "would upload" or "dry_run=true" messages
- [ ] Verify the file does NOT appear on Drive
- [ ] Stop the dry-run instance (Ctrl+C)
- [ ] Restart the service:
      `sudo systemctl start homedrive@fix.service`
- [ ] Clean up

## 14. Quota behavior

Quota testing requires a nearly-full Drive account. If not practical,
verify the MQTT events are correctly structured by inspecting the code
and logs.

- [ ] If Drive is above `warn_pct`: verify MQTT `quota.warning` event
      is published periodically
- [ ] If Drive is above `stop_push_pct`: verify status shows
      `quota_blocked`, pushes are paused, pulls continue
- [ ] If not testable with real quota: confirm quota polling logs appear
      every 5 minutes in the journal

## 15. Crash recovery

- [ ] `sudo kill -9 $(pidof homedrive)` (simulate crash)
- [ ] Wait for systemd to restart the service (10s RestartSec)
- [ ] `homedrive ctl status` shows `running`
- [ ] MQTT online sensor recovers to `online`
- [ ] Create a file and verify it syncs normally (push path works)
- [ ] Wait for a pull cycle (verify pull path works)
- [ ] No duplicate files or corruption visible

## 16. Log and metrics

- [ ] Logs are structured JSON:
      `journalctl -u homedrive@fix.service -n 5 -o cat | python3 -m json.tool`
- [ ] Audit log exists and has entries:
      `wc -l /var/log/homedrive/audit.jsonl`
- [ ] Prometheus metrics endpoint responds:
      `curl http://127.0.0.1:6090/metrics`
- [ ] Health endpoint reflects current state:
      `curl http://127.0.0.1:6090/healthz`

## 17. Cleanup

After completing all checks:

```bash
rm -rf /mnt/external/gdrive/test-validation/
rclone purge gdrive:test-validation/
```

## Result recording

Record the results with:

- Date and time
- homedrive version (`homedrive --version`)
- Pi model and OS version (`uname -a`, `cat /etc/os-release`)
- Any failures or unexpected behavior
- Screenshots of HA dashboard if relevant
