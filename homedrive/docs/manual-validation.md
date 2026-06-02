# Manual validation — installing homedrive on gruissan

Validated on: 2026-05-31  
Target: Debian 12 (bookworm) aarch64 — Raspberry Pi (`gruissan.local`, `192.168.1.2`)  
User instance: `homedrive@fix.service`

---

## Prerequisites

On the **development Mac**:

- Go toolchain installed
- `goreleaser` installed (`brew install goreleaser`)
- SSH key at `~/.ssh/fix.kowalski@gmail.com_rsa` (user `fix`) and admin-capable key for user `admin`

On the **NAS** (`gruissan`):

- `rclone` installed (`/usr/bin/rclone v1.74.2`)
- External drive mounted at `/media/fideco-sda1` (3.6 TB)
- User `admin` has passwordless `sudo`

---

## 1. Build the .deb (on the Mac)

```bash
goreleaser release --snapshot --skip=publish --clean
# produces: dist/homedrive_<version>_linux_arm64.deb
```

## 2. Copy the .deb to the NAS

```bash
scp -i ~/.ssh/fix.kowalski@gmail.com_rsa \
  dist/homedrive_*_linux_arm64.deb \
  fix@192.168.1.2:/tmp/
```

> Note: use the IP `192.168.1.2` rather than `gruissan.local` — the mDNS name
> resolves with a trailing dot in `known_hosts` which causes host-key
> verification to fail with `scp`.

## 3. Install the package (on the NAS, as admin)

```bash
sudo dpkg -i /tmp/homedrive_*_linux_arm64.deb
```

This installs:

| Path | Content |
|---|---|
| `/usr/bin/homedrive` | binary |
| `/lib/systemd/system/homedrive@.service` | templated service unit |
| `/etc/default/homedrive` | systemd env file (HOMEDRIVE_CONFIG, log level) |
| `/etc/homedrive/config.yaml` | rich YAML config (config\|noreplace — not overwritten on upgrade) |
| `/etc/sysctl.d/99-homedrive-inotify.conf` | inotify tuning (applied at install) |
| `/etc/logrotate.d/homedrive` | weekly log rotation, keep 12 |

## 4. Prepare the local sync root (on the NAS)

Create the directory that will mirror Google Drive and give ownership to the
service user:

```bash
mkdir -p /media/fideco-sda1/gdrive
sudo chown fix:fix /media/fideco-sda1/gdrive
```

## 5. Configure rclone (on the Mac, then copy to the NAS)

The service reads rclone config from `/home/fix/.config/rclone/rclone.conf`.
Configure the `gdrive` remote on the Mac (where a browser is available for
the OAuth flow), then copy it to the NAS:

```bash
# On the Mac — create/configure the remote named "gdrive":
rclone config

# Copy the resulting config to the NAS:
scp -i ~/.ssh/fix.kowalski@gmail.com_rsa \
  ~/.config/rclone/rclone.conf \
  fix@192.168.1.2:~/.config/rclone/rclone.conf
```

## 6. Edit `/etc/homedrive/config.yaml` (on the NAS)

Update at minimum:

```yaml
local_root: /media/fideco-sda1/gdrive   # actual mount path
remote: gdrive:                          # must match the rclone remote name
rclone_config: /home/fix/.config/rclone/rclone.conf
```

State and log paths use systemd-managed per-user directories
(`StateDirectory=homedrive/%i` / `LogsDirectory=homedrive/%i`):

```yaml
state:
  path: /var/lib/homedrive/fix/state.db
  audit_log: /var/log/homedrive/fix/audit.jsonl
```

## 7. Enable and start the service (on the NAS, as admin)

```bash
sudo systemctl enable --now homedrive@fix.service
sudo systemctl status homedrive@fix.service
```

Expected output (stub — sync not yet wired):

```
● homedrive@fix.service - homedrive sync agent for fix
     Loaded: loaded (/lib/systemd/system/homedrive@.service; enabled; preset: enabled)
     Active: active (running) since Sun 2026-05-31 21:48:19 CEST
   Main PID: 395402 (homedrive)
…
May 31 21:48:19 gruissan homedrive[395402]: {"level":"INFO","msg":"starting homedrive agent","version":"0.0.0-SNAPSHOT-06a62c4","config":"/etc/homedrive/config.yaml","dry_run":false}
```

## 8. Verify logs

```bash
sudo journalctl -u homedrive@fix.service -f
```

---

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `unknown flag: --config` | old binary missing the flag | rebuild and reinstall the .deb |
| `failed` with exit-code immediately | `Type=notify` without sd_notify | service now uses `Type=simple`; rebuild if still on old unit |
| `Host key verification failed` on scp | mDNS trailing-dot in known_hosts | use IP `192.168.1.2` instead of `gruissan.local` |
| state/log dirs missing | first start before systemd creates them | systemd creates them automatically on `systemctl start` |

## Uninstalling

```bash
sudo dpkg -r homedrive
# config files (/etc/homedrive/config.yaml, /etc/default/homedrive) are kept
# by dpkg (config|noreplace). Remove manually if desired:
sudo rm -rf /etc/homedrive /etc/default/homedrive
```
