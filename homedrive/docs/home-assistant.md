# Home Assistant integration

homedrive publishes MQTT messages compatible with Home Assistant's
auto-discovery protocol. When MQTT is enabled and a broker is configured,
Home Assistant automatically creates entities for sync status, queue
depth, quota usage, and events.

## Prerequisites

- An MQTT broker accessible from both the Pi and the Home Assistant
  instance (e.g., Mosquitto).
- The MQTT integration enabled in Home Assistant with auto-discovery
  active (default prefix: `homeassistant/`).
- homedrive configured with MQTT enabled:

```yaml
mqtt:
  enabled: true
  broker: tcp://192.168.1.2:1883
  base_topic: homedrive
  ha_discovery_prefix: homeassistant
  publish_interval: 30s
  qos: 1
```

## Topic structure

All topics follow the pattern:

```
<base_topic>/<hostname>/<user>/<entity>
```

Example: `homedrive/nas/fix/status`

Discovery configs are published to:

```
<ha_discovery_prefix>/<component>/homedrive_<host>_<user>_<entity>/config
```

Example: `homeassistant/sensor/homedrive_nas_fix_status/config`

## Entity list

### Sensors

| Entity | Topic suffix | HA component | Device class | Unit | Description |
|---|---|---|---|---|---|
| Status | `status` | sensor | -- | -- | `running`, `paused`, `error`, `quota_blocked` |
| Last push | `last_push` | sensor | timestamp | -- | ISO8601 timestamp of last successful push |
| Last pull | `last_pull` | sensor | timestamp | -- | ISO8601 timestamp of last successful pull |
| Pending uploads | `queue/pending_up` | sensor | -- | files | Number of files waiting to be pushed |
| Pending downloads | `queue/pending_down` | sensor | -- | files | Number of files waiting to be pulled |
| Conflicts (24h) | `conflicts_24h` | sensor | -- | conflicts | Rolling 24-hour conflict count |
| Bytes uploaded (24h) | `bytes_up_24h` | sensor | data_size | B | Rolling 24-hour upload volume |
| Bytes downloaded (24h) | `bytes_down_24h` | sensor | data_size | B | Rolling 24-hour download volume |
| Drive quota used | `quota_used_pct` | sensor | -- | % | Google Drive storage usage percentage |

### Binary sensors

| Entity | Topic suffix | HA component | Device class | Description |
|---|---|---|---|---|
| Online | `online` | binary_sensor | connectivity | LWT: `online` or `offline` |

### Device block

All entities share a `device` block so they appear grouped in the Home
Assistant UI:

```json
{
  "device": {
    "identifiers": ["homedrive_nas_fix"],
    "name": "homedrive (fix@nas)",
    "sw_version": "0.1.0",
    "model": "homedrive",
    "manufacturer": "asnowfix/home-automation"
  }
}
```

## Events

Events are published to `<base>/<host>/<user>/events/<type>` with
QoS 1 and retain=false.

| Event type | When published |
|---|---|
| `push.success` | File successfully uploaded to Drive |
| `push.failure` | File upload failed after all retries |
| `pull.success` | File successfully downloaded from Drive |
| `pull.failure` | File download failed |
| `conflict.detected` | Conflict identified before resolution |
| `conflict.resolved` | Conflict resolved per policy |
| `dir_rename` | Directory rename completed (single API call) |
| `quota.warning` | Drive quota above `warn_pct` threshold |
| `quota.exhausted` | Drive quota above `stop_push_pct`, pushes paused |
| `oauth.refresh_failed` | OAuth token refresh failed |

### Event payload format

All events share a common structure:

```json
{
  "ts": "2026-04-28T14:32:11Z",
  "type": "push.success",
  "path": "Documents/report.pdf",
  "bytes": 1048576,
  "duration_ms": 2340
}
```

Conflict events include additional fields:

```json
{
  "ts": "2026-04-28T14:32:11Z",
  "type": "conflict.detected",
  "path": "Documents/notes.md",
  "local_mtime": "2026-04-28T14:32:00Z",
  "remote_mtime": "2026-04-28T14:31:45Z",
  "resolution": "newer_wins:local",
  "kept_old_as": "Documents/notes.md.old.3"
}
```

Quota events include usage information:

```json
{
  "ts": "2026-04-28T15:00:00Z",
  "type": "quota.warning",
  "used_pct": 91.5,
  "used_bytes": 14413619814,
  "total_bytes": 16106127360
}
```

## LWT (Last Will and Testament)

The MQTT client sets a Last Will and Testament on connect:

- **Topic**: `<base>/<host>/<user>/online`
- **Payload**: `offline`
- **Retain**: true
- **QoS**: 1

After a successful connection, the client publishes `online` to the same
topic with retain=true. If the daemon crashes or loses connectivity, the
broker delivers the LWT (`offline`) to subscribers.

Home Assistant uses this as a binary sensor with `device_class:
connectivity`.

## Example automations

### Notify on conflict

Send a mobile notification when a file conflict is detected:

```yaml
automation:
  - alias: "Notify on homedrive conflict"
    trigger:
      - platform: mqtt
        topic: "homedrive/nas/fix/events/conflict.detected"
    action:
      - service: notify.mobile_app_phone
        data:
          title: "homedrive conflict"
          message: >-
            Conflict on {{ trigger.payload_json.path }}.
            Local: {{ trigger.payload_json.local_mtime }},
            Remote: {{ trigger.payload_json.remote_mtime }}.
            Resolution: {{ trigger.payload_json.resolution }}.
```

### Alert on quota warning

Flash a light or send a notification when Drive storage is running low:

```yaml
automation:
  - alias: "Alert on Drive quota warning"
    trigger:
      - platform: mqtt
        topic: "homedrive/nas/fix/events/quota.warning"
    action:
      - service: notify.mobile_app_phone
        data:
          title: "Drive storage warning"
          message: >-
            Google Drive is {{ trigger.payload_json.used_pct }}% full.
            Consider freeing space.
```

### Alert on quota exhausted (pushes paused)

A more urgent notification when pushes are blocked:

```yaml
automation:
  - alias: "Alert on Drive quota exhausted"
    trigger:
      - platform: mqtt
        topic: "homedrive/nas/fix/events/quota.exhausted"
    action:
      - service: notify.mobile_app_phone
        data:
          title: "Drive storage FULL"
          message: >-
            Google Drive is {{ trigger.payload_json.used_pct }}% full.
            Push sync is PAUSED. Downloads continue.
          data:
            priority: high
```

### Notify on OAuth failure

Get alerted when the OAuth refresh token fails (requires re-authentication):

```yaml
automation:
  - alias: "Alert on OAuth refresh failure"
    trigger:
      - platform: mqtt
        topic: "homedrive/nas/fix/events/oauth.refresh_failed"
    action:
      - service: notify.mobile_app_phone
        data:
          title: "homedrive auth failed"
          message: >-
            OAuth token refresh failed. Re-authenticate with
            rclone config to restore sync.
          data:
            priority: high
```

### Dashboard card

A simple Lovelace entities card showing sync status:

```yaml
type: entities
title: "homedrive (fix@nas)"
entities:
  - entity: binary_sensor.homedrive_nas_fix_online
    name: "Online"
  - entity: sensor.homedrive_nas_fix_status
    name: "Status"
  - entity: sensor.homedrive_nas_fix_last_push
    name: "Last push"
  - entity: sensor.homedrive_nas_fix_last_pull
    name: "Last pull"
  - entity: sensor.homedrive_nas_fix_queue_pending_up
    name: "Pending uploads"
  - entity: sensor.homedrive_nas_fix_queue_pending_down
    name: "Pending downloads"
  - entity: sensor.homedrive_nas_fix_conflicts_24h
    name: "Conflicts (24h)"
  - entity: sensor.homedrive_nas_fix_quota_used_pct
    name: "Drive quota"
```

## Reserved future topics

The following topic namespaces are reserved for future cross-device sync
features. Do not use them in v0.1 automations as their format may
change:

| Topic pattern | Future purpose |
|---|---|
| `homedrive/peers/<host>` | Retained presence beacon for peer discovery |
| `homedrive/locks/<key>` | Distributed mutex for multi-device file locking |
| `homedrive/sync/proposals/<id>` | Conflict resolution voting between peers |
| `homedrive/sync/decisions/<id>` | Resolution outcome broadcast |
