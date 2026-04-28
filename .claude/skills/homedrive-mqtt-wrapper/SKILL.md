---
name: homedrive-mqtt-wrapper
description: MQTT publishing rules for homedrive — paho wrapper API, LWT pattern, Home Assistant Discovery, reserved future namespaces, and test conventions. Apply whenever publishing MQTT, designing topics, or extending the mqtt package.
---

# homedrive MQTT wrapper

## Library

Use **only** `github.com/eclipse/paho.mqtt.golang`, wrapped in
`internal/mqtt/`. Never import paho elsewhere in the codebase.

## v0.1 API contract

```go
package mqtt

type Config struct {
    Broker             string         // tcp://host:1883
    ClientIDPrefix     string         // homedrive
    BaseTopic          string         // homedrive
    HADiscoveryPrefix  string         // homeassistant
    QoS                byte           // 0|1|2 (default 1)
    KeepAlive          time.Duration  // default 30s
    ReconnectMax       time.Duration  // default 5m
    Username, Password string         // optional
}

type Publisher interface {
    Publish(topic string, qos byte, retain bool, payload any) error
    PublishJSON(topic string, payload any) error
    Topic(parts ...string) string  // <base>/<host>/<user>/<parts...>
    Close(ctx context.Context) error
}

func New(cfg Config, host, user string, log *slog.Logger) (*Client, error)
```

## Mandatory connection lifecycle

1. On `Connect()`: set LWT on `<base>/<host>/<user>/online` payload
   `offline`, retain=true.
2. After successful connect: publish `online` payload (retain=true) to
   the same topic.
3. On disconnect: publish `offline` (retain=true) before closing.
4. Auto-reconnect with exponential backoff via paho's `OnConnectionLost`.
5. All publishes are non-blocking; errors logged + counted in metrics.

## No subscriptions in v0.1

The daemon never subscribes. All control happens via the HTTP endpoint on
`127.0.0.1:6090`. Adding subscriptions later requires user approval and a
PLAN.md update.

## Home Assistant Discovery

Publish discovery configs at startup and on `/reload`, with retain=true:

```
Topic: homeassistant/<component>/homedrive_<host>_<user>_<entity>/config
Payload: see PLAN.md §9.1 for the entity table
```

All entities share a `device` block:
```json
"device": {
  "identifiers": ["homedrive_<host>_<user>"],
  "name": "homedrive (<user>@<host>)",
  "sw_version": "<semver>",
  "model": "homedrive",
  "manufacturer": "asnowfix/home-automation"
}
```

## Event payloads

Topic: `homedrive/<host>/<user>/events/<type>` (QoS 1, retain false).

Allowed types: `push.success`, `push.failure`, `pull.success`,
`pull.failure`, `conflict.detected`, `conflict.resolved`, `dir_rename`,
`quota.warning`, `quota.exhausted`, `oauth.refresh_failed`.

JSON payload format includes always: `ts` (ISO8601 UTC), `type`. Other
fields are type-specific. See PLAN.md §9.2.

## Reserved future namespaces

**Do not use** these prefixes in v0.1 publishes:

| Prefix | Reserved for |
|---|---|
| `homedrive/peers/<host>` | retained presence beacon |
| `homedrive/locks/<key>` | distributed mutex |
| `homedrive/sync/proposals/<id>` | conflict resolution voting |
| `homedrive/sync/decisions/<id>` | resolution outcome |

## Future-extension interfaces

The package contains commented-out interfaces for the v0.2+ peer-sync
features. Do not implement them in v0.1; their presence is a design
contract:

```go
// type Subscriber interface { ... }
// type PeerCoordinator interface { ... }
```

## Tests

- Use `github.com/mochi-mqtt/server/v2` as an embedded broker in tests.
- **Never** test against a real production broker.
- Cover: connect, publish JSON, retained discovery message, LWT delivery
  on broker kill, reconnect after broker restart.
- Test file must skip if the embedded broker port is busy (use
  `:0` for ephemeral allocation when possible).

## What this wrapper is NOT

- Not a replacement for `mymqtt` from the parent repo (which provides
  bidirectional plumbing tailored to Shelly devices).
- Not a generic library — it is shaped for `homedrive`'s specific topic
  structure (`<base>/<host>/<user>/...`).
- Not for arbitrary cross-package use — keep MQTT calls inside
  `internal/mqtt/` and the syncer.
