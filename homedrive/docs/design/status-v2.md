# Design: homedrive status v2 (MQTT wire contract for observability and alerting)

Status: PR 1 of 2 implemented (#141); auth observer and startup reorder are #142. Tracks issue #87 (the agent restart-looped for about 2 months and nobody noticed).
Consumer: myhome (github.com/asnowfix/home-automation). Its design doc cites this file as the
**normative wire contract**. homedrive stays **publish-only**: no MQTT subscriptions (v0.1 rule,
`homedrive-mqtt-wrapper` skill).

Architecture reference: `homedrive/PLAN.md` §3, §7–§9, §16 and `homedrive/docs/architecture.md`.
Both are **stale on MQTT state publishing** (see §2). Code facts below cite `file:line` on
`main` at a8bc95c. Lines marked *(inferred)* are reasoning, not verified in code.
Live facts are marked *(observed 2026-11-15)*: one read-only sample on the NAS, build
`homedrive/v0.1.2-issue65-66-ga8bc95c`.

## 1. The ask, goals, non-goals

**Ask.** Publish one versioned, periodic status document that myhome can show on the kiosk panel
and alert on. Fix the bugs that make today's numbers wrong.

Goals:
- A retained JSON status on a single topic, published every `mqtt.publish_interval`. A consumer can
  tell from it, without any other topic, whether the agent is alive, restart-looping, failing auth,
  or failing to sync.
- Windowed counters (1h/24h/7d) derived from **persistent** data, so they survive restarts.
- Fix confirmed bugs (a) and (c), plus the unwired publisher found while checking them (§2).

Non-goals:
- HA Discovery wiring. `discovery.go` exists but is unwired. Its per-field state topics would
  need `value_template`s over the v2 JSON. That goes in a follow-up issue.
- Wiring the quota manager (`internal/quota`, never imported by `cmd/`). As a result,
  `quota.warning` / `quota.exhausted` and the `quota_blocked` state stay unpublished.
- A v2 document over HTTP. `GET /status` keeps its v1 shape. Its zeroed fields get real values.
- Per-path history, Prometheus histograms, and any MQTT subscription (for example remote pause).

## 2. Today's MQTT surface (verified in code, corrected)

| Topic (`homedrive/<host>/<user>/…`) | Published? | Source |
|---|---|---|
| `status`, `last_push`, `last_pull`, `queue/*`, `conflicts_24h`, `bytes_*_24h`, `quota_used_pct` | **No.** `mqtt.NewStatePublisher` (state.go:53) is only called from tests. `MQTTConfig.PublishInterval` (config.go:128) is read nowhere. | never wired |
| `homeassistant/…/config` (HA discovery) | **No.** `PublishDiscovery` (discovery.go:197) is only called from tests. | never wired |
| `online` (retained, LWT `offline`) | Yes, on every (re)connect | mqtt.go:98, 224-234 |
| `events/push.success`, `events/push.failure` | Yes. Dir renames also use these topics, with `"type":"dir_rename"`. There is no `events/dir_rename` topic. | syncer.go:301, 324, 357, 388 |
| `events/pull.success`, `events/pull.failure` | Yes | puller.go:341, 380, 401 |
| `events/conflict.detected`, `events/conflict.resolved` | Yes | conflict.go:121, 227 |
| `events/conflict.pruned` | Yes. Not in the skill's allowed list. | prune.go:287 |
| `events/bisync/started`, `events/bisync/completed` | Yes, under **slash-separated** topics because the type is split on `.` | bisync.go:204-276, 365-366 |
| `events/quota.warning`, `events/quota.exhausted` | **No.** The quota manager is unwired. | quota.go:248-254 |
| `events/oauth.refresh_failed` | **No.** `EventPublisher.EmitOAuthFailure` (events.go:160) has no caller. | never wired |

The live sample agrees *(observed 2026-11-15)*. In 70s of `homedrive/#` the only topic was
`events/pull.success`: no status, no discovery, and no `online`. The missing status and discovery
are fully explained by the code. **The missing `online` is not**: `onConnect` republishes it
retained after every reconnect. The code alone cannot distinguish two explanations: the embedded
broker not keeping or serving retained messages, or the agent not having reconnected since the
broker's last restart. The QoS 0 seen is most likely the subscriber's requested QoS capping
delivery *(inferred)*. The publisher uses `cfg.QoS`, which defaults to 1.

PLAN.md Phase 9 and RELEASE_NOTES (v0.1.0) both say state and discovery publishing shipped. This
is the same class of gap as issue #49. **This is the root reason #87 was invisible on MQTT.**

## 3. Suspected bugs: verdicts

**(a) `bytes_down_24h` stays 0: CONFIRMED (code and live).** `Agent.Status` hard-codes
`Conflicts24h`, `BytesUp24h` and `BytesDown24h` to `0` with the comment "not yet tracked"
(agent_control.go:111-113). Live, the field stayed 0 while 4 × 20971520 B downloads emitted
`pull.success` with `bytes=20971520` *(observed 2026-11-15)*. The audit log cannot rebuild the
number either. `auditLoggerAdapter` (adapters.go:32-40) copies only `ts, op, path, dry_run, error`
into `store.AuditEntry` (audit.go:13-26) and drops `Bytes`, `Origin` and `DurationMs`.
Fix: counters from §5.

**(b) `last_pull` is local time labelled `Z`: REFUTED in code. Also refuted live for this build.**
`unixNanoToRFC3339` does `time.Unix(0,n).UTC()` (agent_control.go:213), and the value stored is
`time.Now().UnixNano()` (agent_run.go:204), which does not depend on the time zone. Every other
string timestamp also calls `.UTC()` (state.go:121, events.go:83, puller.go:342/381/394,
conflict.go:122, bisync.go:205/234/270). Live, `last_pull` was 11:33:13Z against a measured
11:33:32Z: an age of 19s, with no offset *(observed 2026-11-15)*. **The earlier "future" value
cannot be explained from the code alone.** Candidates that the code cannot tell apart: NAS clock
skew (a Pi has no RTC, *inferred*), a consumer-side misparse, or a different binary.

Two real defects sit next to this and are fixed here:
- `push.success` and `push.failure` put a raw `time.Time` in `ts` (syncer.go:302, 325, 358, 389).
  It marshals as RFC3339Nano with the **local offset**, for example `…T12:33:13.1+01:00`. A
  consumer that truncates the offset would read local time as UTC, which shows up as "in the
  future". Fix: `s.nowFn().UTC().Format(time.RFC3339)`.
- `last_push` is set when an event is **enqueued**, not when a push succeeds
  (agent_run.go:241, 250). It moves even when every push then fails. Fix: set it from the
  tracker on success.

**(c) 24h counters are in memory and reset on restart: CONFIRMED, in a stronger form.**
No 24h counters exist at all; they are constants (agent_control.go:111-113). The only state
that does exist, `lastPushUnixNano` and `lastPullUnixNano` (agent.go:75-76), lives only in memory
and resets on every restart. Fix: the Bolt-backed tracker (§5).

**(d) New: startup is fatal on a broker outage.** `mqtt.New` waits 10s and returns an error
(mqtt.go:108-114), and `newAgent` aborts on it (agent.go:136-141). The broker is embedded in
myhome and restarts on NAS reboot and every 2d *(observed 2026-11-15)*. So a homedrive start
during a broker outage becomes a systemd restart, and the agent's own monitoring channel causes
restarts. Fix in §6.

## 4. Quality drivers

1. **Detectability.** When the agent dies or loops, myhome alerts within 15 min (the alert
   threshold), whatever the broker's restart cadence.
2. **Restart-survival.** After a restart, the 24h and 7d counters keep their values from before
   the restart, minus at most one publish interval.
3. **Pi cost.** One publish (about 1.5 KB) and at most one small Bolt write transaction per
   30s. No filesystem walk and no Drive API call on the publish path.
4. **Contract stability.** A myhome built against 2.0 keeps working against any 2.x.

## 5. Proposed design

**Fit:** mostly as-is. It adds one small internal package and one optional hook interface in
`syncer`, and it reorders startup. No architectural increment is needed beyond the topic contract
itself.

### 5.1 Components

- **`internal/stats` (new): `Tracker`.** Holds an in-memory ring of 5-minute buckets covering
  8 days (at most 2304). Each bucket holds `files_up, files_down, bytes_up, bytes_down, errors,
  conflicts, starts`. The tracker also holds `last_push, last_pull, last_poll, last_error, auth,
  bisync, local` and `started_at`. It implements `syncer.Recorder`. `Snapshot(now)` sums buckets
  that start inside `[now-W, now]`; future-dated buckets are ignored. It uses an injectable
  `now func() time.Time`.
- **Persistence in Bolt (`state.db`), new bucket `stats`.** Key: `YYYYMMDDHHMM` (UTC, bucket
  start). Value: compact JSON. `last_*`, `last_error` and `bisync` live under `meta` keys
  `stats.last.*`.
  - `Flush()` runs on each publish tick and on shutdown. It writes only dirty buckets plus `meta`,
    and deletes keys older than 8d, all in **one** `db.Update`.
  - `Load()` runs at startup, then `starts++` is recorded in the current bucket.
  - Size: at most 2304 × about 90 B, roughly 200 KiB of keys. Bolt reuses freed pages, so the file
    stops growing once 8 days are filled. Expected file size is about 1 MiB, up from 512 KiB
    *(inferred)*.
  - Retention and compaction: the 8-day prune is the only compaction. The 7d window is always
    fully covered after 7 days of uptime **history**, which does not need to be continuous.
  - Crash loss: at most 30s of counts. Accepted.
- **`syncer.Recorder`**, a consumer-defined interface. It is optional, and nil means no-op:
  ```go
  type Outcome struct {
      Op    string    // push|push_delete|dir_rename|pull|pull_delete|conflict|bisync_push|bisync_pull|poll|bisync
      Dir   string    // "up" | "down" | ""   (only content transfers count as files_up/down)
      Bytes int64
      Err   error     // final failure after retries; nil = success
      At    time.Time
  }
  type Recorder interface { Record(Outcome) }
  ```
  - Wiring: `syncer.WithRecorder` (Syncer, the same option style as `WithBisyncMutex`),
    `PullerConfig.Recorder`, `BisyncOpts.Recorder`.
  - Call sites are next to the existing event emits: syncer.go `recordResult` and
    `handleDirRename`; pull_process.go `recordDownload`, the download failure path,
    `processDelete` and `handleConflict`; puller.go `fetchChanges` failure (`Op:"poll"`) and
    success; bisync_ops.go `syncLocalToRemote` and `syncRemoteToLocal`; bisync.go `execute` end
    (bisync result plus duration).
  - Bisync also calls `RecordLocalTree(files, bytes, at)` from the `walkLocal` result it already
    has.
  - **Not counted as errors:** `context.Canceled`, `ErrDanglingShortcut` skips, and dry-run.
- **Auth tracking (rcloneclient).**
  - Startup: `validateOAuthCredential` reports its outcome through an `AuthObserver` callback
    passed in `RcloneFSConfig`. Outcomes: ok, `client_misconfigured`, `token_invalid`, or
    `unknown` (skipped or unreachable).
  - Runtime: `oauthHTTPClient` wraps its `TokenSource` in a notifying source. It records
    `last_refresh_ok` when a new access token arrives and `last_refresh_error` on
    `*oauth2.RetrieveError`.
  - `ErrOAuthClientMissing` from `fetchChanges` maps to `client_missing`.
  - Scope: this observes the **Changes API client only**. rclone's backend refreshes its own
    token internally and invisibly *(inferred; not observable without importing rclone
    `lib/oauthutil`, which is forbidden)*. Because the Changes API polls every 30s, its token is
    refreshed about hourly, which is enough to detect a dead credential.
- **`internal/mqtt/state.go` (rewritten).** `StatePublisher` takes a
  `func() homedrive.StatusV2` snapshot function and publishes **one** retained JSON document per
  tick to `…/status`. The per-field subtopic publishing is removed; it was never wired, so there
  are no consumers to break.
- **`pkg/homedrive/status.go` (new): the `StatusV2` struct.** It holds types only, per
  architecture.md's intended use of `pkg/`. See open question Q1.

### 5.2 Startup order (fixes the invisible restart loop)

`newAgent` becomes:

1. `config.Load`
2. Open the journal and `stats.Load` (`starts++`)
3. MQTT client with `SetConnectRetry(true)`. This is **non-fatal**: the client keeps retrying in
   the background, and paho queues publishes until connected (*inferred* from the paho docs, to
   be checked in tests).
4. Publish status `state:"starting"`.
5. `NewRcloneFS` (OAuth validation).

If step 5 or anything after it fails, the agent publishes `state:"error"` with
`last_error{op:"startup"}` and `auth`, flushes stats, waits up to 3s for the publish token, and
exits non-zero as it does today. Each crash-loop attempt, one per 60s with `RestartSec=60`, then
leaves a visible trace: a new `started_at`, `counts.*.starts` rising, and `last_error`. Today an
OAuth rejection exits before MQTT even connects.

### 5.3 Topic and payload: status schema 2.0

Topic `homedrive/<host>/<user>/status`, QoS 1, **retain=true**, every `mqtt.publish_interval`
(default 30s). It is also published immediately on start, on clean shutdown (after the final stats flush), and on `/reload`; the error-exit publish is #142.
`<host>` is `os.Hostname()` and `<user>` is the OS user of the unit (agent.go:237-247). Neither
can be configured *(observed 2026-11-15)*.

All timestamps are RFC3339 **UTC with a `Z` suffix and second precision**. Absent values are
JSON `null`, never `""` or the zero time. Tags: **P** = required for panel, **A** = required for
alerting, **N** = nice to have and safe to cut.

| Field | Type / unit | Semantics | Tag |
|---|---|---|---|
| `schema` | string `"2.0"` | `major.minor` of this contract | A |
| `ts` | timestamp | When this document was built | A |
| `publish_interval_s` | int, s | **Always present and > 0** (a zero or unset `mqtt.publish_interval` publishes as 30). So consumers derive staleness instead of hard-coding it. | A |
| `version` | string | Binary version (ldflags) | P |
| `state` | enum string | `starting`, `running`, `paused`, `error`. Future values (for example `quota_blocked`) may appear; see §7. | A |
| `started_at` | timestamp | Start of this process | A |
| `uptime_s` | int, s | `ts - started_at`. Redundant, kept for display. | N |
| `dry_run` | bool | Live value after `/reload` | P |
| `pending_up` | int | Push queue depth (`len(pushEvents)+len(pushRenames)`) | P |
| `last_push` | timestamp/null | Last **successful** push-side remote mutation (upload, delete, dir rename). Persisted. | P |
| `last_pull` | timestamp/null | Last **successful** pull-side local mutation (download, delete). Persisted. | P |
| `last_activity` | timestamp/null | `max(last_push, last_pull)` | P |
| `last_poll` | timestamp/null | Last successful Changes API poll, even with 0 changes. This is today's HTTP `last_pull`. | A |
| `counts` | object | Keys `"1h"`, `"24h"`, `"7d"`. Each holds `files_up, files_down, bytes_up, bytes_down, errors, errors_up, errors_down, conflicts, starts` (ints; bytes in B). `errors` is the total of final failures; `errors_up` (push side: upload, delete, dir rename) and `errors_down` (pull side: download, delete, Changes API poll) split it by direction, so steady push failures are not masked by interleaved pull successes. A failure with no direction (a failed bisync pass, conflict resolution) counts in `errors` only. All three windows are always present. Conflict handling (pull or bisync) counts in `conflicts` only: a file transferred while resolving one is not added to `files_*` / `bytes_*`. Windows are aligned to 5-min buckets, so they are accurate to 5 min. | P (files, bytes), A (errors, starts) |
| `last_error` | object/null | `{ts, op, message}`: the last final failure, persisted. `op` is one of the `Outcome.Op` values or `startup`. `message` is truncated to 256 bytes. | A |
| `auth` | object | `{state, last_refresh_ok, last_refresh_error:{ts,message}\|null}`. `state` is one of `ok`, `unknown`, `client_missing`, `client_misconfigured`, `token_invalid`, `refresh_failing`. | A |
| `bisync` | object/null | `{at, result:"ok"\|"error", duration_ms, files_changed, error}` for the last completed pass. `error` is the one exception to "absent is null": the key is omitted when `result` is `ok`. Persisted. | P; A (safety net) |
| `local` | object/null | `{files, bytes, scanned_at}` from the last bisync `walkLocal`. Excludes apply. Not real-time. | N |
| `quota_used_pct` | number/null | Drive usage, cached and refreshed at most every 5 min. **Never** a live call per tick. Today's HTTP path makes a live call (agent_control.go:122-128). | P |

**Deliberately not added:**
- `pending_down`: always 0, because pull is synchronous (agent_control.go:107).
- Flat `conflicts_24h` / `bytes_*_24h`: superseded by `counts`.
- Host and user in the payload: already in the topic.
- MQTT publish error count.
- Journal entry count.

Size: about 1.3–1.6 KB of JSON, so about 4.5 MB/day on the LAN. Build cost: summing 2016
buckets × 7 ints, at most a few microseconds.

**HTTP `/status`** keeps its v1 field names. It fills `conflicts_24h`, `bytes_up_24h` and
`bytes_down_24h` from `counts["24h"]`, and `last_push` / `last_pull` from the tracker, which fixes
(a) and (c) for `homedrive ctl status`. **Behaviour change:** `last_pull` now means last pull-side
mutation; the old meaning moves to the new `last_poll` field.

**Events** are unchanged except for the `ts` normalisation in §3(b). They remain **advisory**:
non-retained, and missed whenever the consumer is down.

## 6. Alternatives considered

- **Derive the windows from the audit log** (scan JSONL at startup, then count incrementally).
  - Strongest form: the audit log is already an append-only, durable record, and it adds no new
    persistence.
  - Why it loses:
    - It is optional (`state.audit_log: ""` disables it, agent.go:189).
    - Weekly rotation with `compress`/`delaycompress` puts a 7d window across `audit.jsonl`,
      `.1`, and sometimes `.2.gz`.
    - The adapter drops bytes (adapters.go:32-40).
    - The live log shows only `ts, op, path, error` *(observed 2026-11-15)*. The observer saw
      failures only, although the code writes successes too (syncer.go:382, pull_process.go:316).
      The code cannot explain that difference.
    - Fixing all of this costs more than the Bolt bucket and still needs a startup scan on a Pi.
- **Tap the event stream** (wrap `syncer.Publisher` in the wiring layer and count by topic and
  payload).
  - Strongest form: zero changes to `syncer`, and it works with MQTT disabled.
  - Why it loses:
    - It counts from stringly-typed `map[string]any` payloads, whose shapes are already
      inconsistent.
    - `push.success` carries no bytes.
    - Bisync per-file transfers emit no events, so a large part of real traffic would be
      invisible.
- **Per-field retained subtopics** (the existing `state.go` design, HA-friendly).
  - Why it loses:
    - About 15 publishes per tick, with no atomic snapshot.
    - No place for a schema version or nested objects.
    - The consumer cannot tell a field that is stale from a field that is valid.
- **A RemoteFS decorator for counting.** It sees every transfer, bisync included, but counts
  *attempts*: `retryFunc` makes up to 5 attempts, which would inflate `errors` up to 5×. It also
  sees `Stat` returning `ErrNotFound` as an "error" in normal flow. Outcome-level hooks are more
  precise.

## 7. Consumer guidance (normative for myhome)

1. **Liveness comes from the age of `status`, never from retained or LWT state.** Retained
   messages, `online` included, do not survive a broker restart. The broker restarts on NAS
   reboot and, as of 2026-11-15, every 2d, and the cadence may change. The rules must not depend
   on it.
   - `age = now - ts`.
   - **stale** (show it greyed out) when `age > 2 × publish_interval_s`.
   - **offline** (alert) when no document with `age < 15 min` has been seen for 15 min. Count
     from consumer start if nothing has arrived since.
   - A retained document delivered on subscribe is judged by its `ts`, never by the time it was
     received.
2. **Clock skew.** If `ts > now + 120s`, flag `clock_skew` and use receipt time for liveness
   until it clears. This would also catch the unexplained "future" value.
3. **`online` and LWT are a hint only.** `offline` may hasten the stale display, but it never
   raises or clears an alert on its own. A missing `online` means nothing.
4. **Restart loop.** Alert when any of these holds:
   - `counts["1h"].starts >= 3`;
   - `started_at` changed at least 3 times within 30 min as seen by the consumer;
   - `state == "error"`.

   `counts` is the robust signal because it is persisted and does not depend on the consumer
   having seen each attempt.
5. **Auth.** Alert immediately when `auth.state ∉ {ok, unknown}`. Warn when `unknown` lasts
   more than 1h while `state == running`.
6. **Sync failing.** Alert when either holds:
   - `counts["1h"].errors >= 5` and `last_error.ts > last_activity` (failing with no success
     since);
   - `state == running` and `now - last_poll > 15 min` (the pull loop is stuck or backed off).

   During an outage `errors_down` counts every failed poll (about 120 per hour at the default
   30s interval), so thresholds on it must allow for that.

   Warn when `bisync.result == "error"` or `now - bisync.at > 3h`, given the default 1h
   interval.
7. **Events.** Use them only for toasts and notifications, never as alert state.
   `push.failure` and `pull.failure` can raise an immediate notification. The `counts` in the
   next status are the source of truth.
8. **Compatibility.**
   - Ignore unknown fields.
   - Treat an unknown `state` value as "degraded: show it raw", not as healthy.
   - The same major with a higher minor is compatible.
   - On an **unknown major**, show "unsupported homedrive status schema", raise **no** alert from
     its contents, and still apply the liveness rule using `ts` if present.

**Compatibility rule (producer side).**
- Adding a field, a `counts` window, or an enum value bumps the minor.
- Removing or renaming a field, changing a type or unit, or changing the meaning of an existing
  field bumps the major.

## 8. Cross-cutting concerns

- **Security:** not affected. Still publish-only, and no new listener.
- **Privacy:** `last_error.message` may contain a file path. Event payloads already publish
  paths, so this is not new exposure. It is truncated to 256 B, and token or secret material
  never appears in it: oauth2 `RetrieveError` text carries the error code and body only
  *(inferred)*. Never log or publish file contents.
- **Observability:** `slog` lines for flush failures (`op=stats_flush`) and for the startup-error
  publish.
- **Failure modes:**
  - Bolt flush error: logged, counts kept in memory, retried next tick.
  - Broker down: paho retries and publishes resume. The consumer's staleness rule covers the gap.
- **Rollout:**
  - The first start after upgrade creates the `stats` bucket. All windows start at 0 and fill up
    over 7d; there is no backfill (Q3).
  - Rollback is safe, because older binaries ignore the unknown bucket and meta keys.
  - Ship as an interim tag `homedrive/vX.Y.Z-issue87` and verify on the NAS with
    `mosquitto_sub -v -t 'homedrive/+/+/status'`.
- **Conventions:**
  - It extends the mqtt skill with a `status` topic. The skill only lists events, so the skill
    and PLAN.md §9.1 must be updated.
  - It changes the meaning of HTTP `last_pull`.
  - `bisync.go` is already **532 lines, over the 500 limit**. The PR must first split it by
    responsibility (for example the retention-GC section into `bisync_gc.go`) before adding
    hooks.
  - `syncer.go` is at 458 lines: move `recordResult`, `handleDirRename` and the dry-run helpers
    to `push_record.go`.

## 9. Implementation plan: one atomic PR

Branch `issue-141-status-v2` (tracked by #141; PR 2 of the split is #142). It fits one PR: it is one feature with one contract, and the
bug fixes are the same lines.

Files:
- **New:** `internal/stats/{tracker.go,ring.go,persist.go}`, `pkg/homedrive/status.go`,
  `internal/syncer/{recorder.go,push_record.go,bisync_gc.go}` (splits).
- **Changed:**
  - `internal/syncer/{syncer.go,puller.go,pull_process.go,bisync.go,bisync_ops.go}` (hooks; push
    `ts` UTC).
  - `internal/store/journal.go` (`stats` bucket; `LoadStats` / `SaveStats` in a new
    `stats_store.go`).
  - `internal/rcloneclient/{driveapi.go,oauthvalidate.go,rclonefs.go}` (`AuthObserver`,
    notifying `TokenSource`).
  - `internal/mqtt/{state.go,mqtt.go}` (JSON status; `SetConnectRetry`).
  - `cmd/homedrive/{agent.go,agent_run.go,agent_control.go}` (order, wiring, publisher
    goroutine, quota cache, `last_push` from the tracker).
  - Docs: PLAN.md §9 and §14 (new phase), the mqtt skill, `docs/home-assistant.md` note.
- **Split option, only if review needs it:** PR1 = tracker, status topic and bugs (a)–(c);
  PR2 = auth tracking and the startup reorder. PR1 alone already makes #87 detectable through
  staleness.

Tests (table-driven `TestXxx_Case`, one `_test.go` per file, injectable clock):
- `stats`: window sums at bucket edges (1h, 24h and 7d boundaries); future buckets ignored; prune
  past 8d; Flush→Load round-trip on a temp Bolt file (restart survival, the core assertion for
  (c)); `starts` increments; `last_error` truncation.
- `syncer`: a fake Recorder against MemFS/FlakyFS asserts one `Outcome` per final result.
  - FlakyFS with 2 failures then success gives **0** errors and 1 transfer with the right bytes.
  - A download records `Dir:"down"` and `Bytes==size`; this is the regression test for (a).
  - A dangling shortcut is not an error; dry run records nothing.
  - Push event `ts` matches `^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$`.
- `rcloneclient`: the notifying TokenSource against an httptest token endpoint (ok and
  `invalid_grant`).
- `mqtt`: embedded mochi-mqtt on `127.0.0.1:0`. Assert one retained `…/status` JSON with
  `schema:"2.0"` per tick and all timestamps ending in `Z`. Assert startup succeeds with the
  broker down and status arrives once it comes up.
- `cmd/homedrive`: with fake remote and journal, `Status()` 24h fields equal the tracker counts;
  a startup OAuth failure publishes `state:"error"` before returning the error.

Invariants to check before the PR:
- `make test`, then `orb run -m dev -- go test -race ./homedrive/...`.
- arm64 stripped binary < 25 MB.
- The rclone-backend grep prints nothing, and no new rclone packages are imported.
- `slog` JSON only.
- Files < 500 lines and functions < 80 lines, including the bisync and syncer splits.
- Coverage > 70%.

## 10. Confirmation

- A reviewer runs `grep -rn 'NewStatePublisher\|SetConnectRetry' homedrive/cmd homedrive/internal/mqtt`;
  it must show production wiring.
- On the NAS, after deploy: `status` arrives every 30s. `counts["24h"].bytes_down` grows by the
  size of a test download and survives `systemctl restart`.
- Breaking `rclone.conf` on a **separate test instance** (see `docs/live-testing.md`) yields
  `state:"error"` and `auth.state:"client_misconfigured"`.

## 11. Open questions (recommended default in bold)

- **Q1** Where does `StatusV2` live? **`pkg/homedrive`.** myhome may import it, or copy the
  struct to avoid pulling in the module graph.
- **Q2** Bucket size: **5 min.** The alternative, 1 min for the last hour plus hourly buckets
  beyond, is more precise but more code.
- **Q3** Backfill counters from the existing audit log on first start? **No.** It lacks bytes
  and successes, as observed.
- **Q4** Wire HA Discovery against v2 in this PR? **No,** a follow-up issue. Whoever wires it must
  also update discovery's per-field state topics.
- **Q5** Stop failing startup on a definitive OAuth rejection, and run degraded with
  `auth.state` set instead? **Keep exiting** (issues #86/#88 chose fail-loud). With §5.2 the exit
  is now visible.
- **Q6** Should `state:"error"` also be used at runtime, for example when `auth.state` is bad
  while the process lives? **No.** Keep `state` about the process; consumers combine it with
  `auth`.
- **Q7** Rename the existing `events/bisync/*` topics to `events/bisync.*` for consistency?
  **No.** It is a breaking change with no consumer benefit; document them as they are.

## 12. Implementation notes and example documents (#141)

Delivered by PR 1 (`issue-141-status-v2`): the stats tracker, the status topic, the
`counts` windows including `errors_up` / `errors_down`, `last_*` timestamps, `last_error`,
`bisync`, `local`, `quota_used_pct` (cached, refreshed at most every 5 min), the push event `ts`
UTC fix, and `last_push` / `last_pull` moving only on success.

**Not in PR 1 (deferred to #142):** the `auth` object, the `starting` state, publishing
`state:"error"` before a failed start exits, and the non-fatal MQTT connect at startup. Until
#142 lands, documents carry no `auth` key. Consumers must treat an absent `auth` as `unknown`
(§7 item 8: ignore unknown or missing optional fields); adding it is a minor-compatible change
within 2.x.

**MQTT reconnect cap.** paho's `MaxReconnectInterval` defaults to **10 minutes** (verified in
`paho.mqtt.golang@v1.5.1/options.go`), which would put a post-restart reconnect gap close to the
consumer's 15 min offline threshold. The wrapper (`internal/mqtt`) now defaults
`ReconnectMax` to, and clamps it at, **2 minutes** (`mqtt.MaxReconnectCap`).

The example documents below are also shipped as fixtures in
`homedrive/pkg/homedrive/testdata/status-*.json`, which myhome can pin a test against.

### 12.1 Running

```json
{
  "schema": "2.0",
  "ts": "2026-11-15T12:33:13Z",
  "publish_interval_s": 30,
  "version": "v0.1.3",
  "state": "running",
  "started_at": "2026-11-15T08:00:02Z",
  "uptime_s": 16391,
  "dry_run": false,
  "pending_up": 0,
  "last_push": "2026-11-15T12:20:41Z",
  "last_pull": "2026-11-15T11:58:09Z",
  "last_activity": "2026-11-15T12:20:41Z",
  "last_poll": "2026-11-15T12:33:03Z",
  "counts": {
    "1h": {
      "files_up": 3,
      "files_down": 0,
      "bytes_up": 1048576,
      "bytes_down": 0,
      "errors": 0,
      "errors_up": 0,
      "errors_down": 0,
      "conflicts": 0,
      "starts": 1
    },
    "24h": {
      "files_up": 41,
      "files_down": 6,
      "bytes_up": 73400320,
      "bytes_down": 125829120,
      "errors": 0,
      "errors_up": 0,
      "errors_down": 0,
      "conflicts": 1,
      "starts": 1
    },
    "7d": {
      "files_up": 290,
      "files_down": 52,
      "bytes_up": 512000000,
      "bytes_down": 900000000,
      "errors": 4,
      "errors_up": 3,
      "errors_down": 1,
      "conflicts": 2,
      "starts": 3
    }
  },
  "last_error": {
    "ts": "2026-11-13T03:11:50Z",
    "op": "push",
    "message": "copy file \"docs/a.txt\": googleapi: Error 503: backend error"
  },
  "bisync": {
    "at": "2026-11-15T12:00:07Z",
    "result": "ok",
    "duration_ms": 8123,
    "files_changed": 0
  },
  "local": {
    "files": 18342,
    "bytes": 52428800000,
    "scanned_at": "2026-11-15T12:00:05Z"
  },
  "quota_used_pct": 61.4
}
```

### 12.2 `state:"error"` (a failed start; published by #142, shape fixed here)

```json
{
  "schema": "2.0",
  "ts": "2026-11-15T12:40:00Z",
  "publish_interval_s": 30,
  "version": "v0.1.3",
  "state": "error",
  "started_at": "2026-11-15T12:39:58Z",
  "uptime_s": 2,
  "dry_run": false,
  "pending_up": 0,
  "last_push": null,
  "last_pull": null,
  "last_activity": null,
  "last_poll": null,
  "counts": {
    "1h": {
      "files_up": 0,
      "files_down": 0,
      "bytes_up": 0,
      "bytes_down": 0,
      "errors": 3,
      "errors_up": 0,
      "errors_down": 0,
      "conflicts": 0,
      "starts": 3
    },
    "24h": {
      "files_up": 0,
      "files_down": 0,
      "bytes_up": 0,
      "bytes_down": 0,
      "errors": 40,
      "errors_up": 0,
      "errors_down": 0,
      "conflicts": 0,
      "starts": 40
    },
    "7d": {
      "files_up": 0,
      "files_down": 0,
      "bytes_up": 0,
      "bytes_down": 0,
      "errors": 40,
      "errors_up": 0,
      "errors_down": 0,
      "conflicts": 0,
      "starts": 40
    }
  },
  "last_error": {
    "ts": "2026-11-15T12:40:00Z",
    "op": "startup",
    "message": "init rclone remote: oauth: invalid_grant"
  },
  "bisync": null,
  "local": null,
  "quota_used_pct": null
}
```

### 12.3 Auth failure (`auth` is produced by #142; shape fixed here)

```json
{
  "schema": "2.0",
  "ts": "2026-11-15T12:45:00Z",
  "publish_interval_s": 30,
  "version": "v0.1.3",
  "state": "running",
  "started_at": "2026-11-15T08:00:02Z",
  "uptime_s": 16391,
  "dry_run": false,
  "pending_up": 0,
  "last_push": "2026-11-15T12:20:41Z",
  "last_pull": "2026-11-15T11:58:09Z",
  "last_activity": "2026-11-15T12:20:41Z",
  "last_poll": "2026-11-15T12:10:03Z",
  "counts": {
    "1h": {
      "files_up": 0,
      "files_down": 0,
      "bytes_up": 0,
      "bytes_down": 0,
      "errors": 40,
      "errors_up": 0,
      "errors_down": 40,
      "conflicts": 0,
      "starts": 1
    },
    "24h": {
      "files_up": 41,
      "files_down": 6,
      "bytes_up": 73400320,
      "bytes_down": 125829120,
      "errors": 0,
      "errors_up": 0,
      "errors_down": 0,
      "conflicts": 1,
      "starts": 1
    },
    "7d": {
      "files_up": 290,
      "files_down": 52,
      "bytes_up": 512000000,
      "bytes_down": 900000000,
      "errors": 4,
      "errors_up": 3,
      "errors_down": 1,
      "conflicts": 2,
      "starts": 3
    }
  },
  "last_error": {
    "ts": "2026-11-15T12:44:33Z",
    "op": "poll",
    "message": "oauth2: cannot fetch token: invalid_grant"
  },
  "bisync": {
    "at": "2026-11-15T12:00:07Z",
    "result": "ok",
    "duration_ms": 8123,
    "files_changed": 0
  },
  "local": {
    "files": 18342,
    "bytes": 52428800000,
    "scanned_at": "2026-11-15T12:00:05Z"
  },
  "quota_used_pct": 61.4,
  "auth": {
    "state": "token_invalid",
    "last_refresh_ok": "2026-11-15T11:10:02Z",
    "last_refresh_error": {
      "ts": "2026-11-15T12:10:04Z",
      "message": "oauth2: cannot fetch token: 400 Bad Request: invalid_grant"
    }
  }
}
```
