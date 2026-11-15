// status_v2.go builds the status v2 document (homedrive/docs/design/
// status-v2.md §5.3) from the stats tracker and runs the periodic
// flush + publish loop. The same tracker backs GET /status.
package main

import (
	"context"
	"sync"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/mqtt"
	"github.com/asnowfix/home-drive/homedrive/internal/stats"
	"github.com/asnowfix/home-drive/homedrive/pkg/homedrive"
)

// quotaRefresh bounds how often the cached quota is refreshed for the
// status document: never a live Drive call per publish tick.
const quotaRefresh = 5 * time.Minute

// quotaTimeout bounds the Drive quota call so a hung About request cannot
// delay the flush and publish that follow it (the liveness signal).
const quotaTimeout = 15 * time.Second

// quotaCache holds the last quota percentage seen by the status loop.
type quotaCache struct {
	mu  sync.Mutex
	at  time.Time
	pct *float64
}

func (q *quotaCache) get() *float64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.pct
}

func (q *quotaCache) due(now time.Time) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return now.Sub(q.at) >= quotaRefresh
}

func (q *quotaCache) set(now time.Time, pct *float64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.at = now
	if pct != nil {
		q.pct = pct
	}
}

// refreshQuota updates the cached quota when stale. Errors keep the
// previous value (best-effort display data).
func (a *Agent) refreshQuota(ctx context.Context, now time.Time) {
	if a.rfs == nil || !a.quota.due(now) {
		return
	}
	qctx, cancel := context.WithTimeout(ctx, quotaTimeout)
	defer cancel()
	q, err := a.rfs.Quota(qctx)
	if err != nil {
		a.log.Warn("quota refresh failed", "op", "quota_refresh", "error", err)
		a.quota.set(now, nil)
		return
	}
	if q.Total <= 0 {
		a.quota.set(now, nil)
		return
	}
	pct := float64(q.Used) / float64(q.Total) * 100
	a.quota.set(now, &pct)
}

// stateName is the document "state" for the live process.
func (a *Agent) stateName() string {
	if a.paused.Load() {
		return homedrive.StatePaused
	}
	return homedrive.StateRunning
}

// buildStatusV2 assembles the status document as of now.
func (a *Agent) buildStatusV2(now time.Time) homedrive.StatusV2 {
	snap := a.stats.Snapshot(now)
	started := a.startTime
	if started.IsZero() {
		started = now
	}
	uptime := int64(now.Sub(started).Seconds())
	if uptime < 0 {
		uptime = 0
	}
	doc := homedrive.StatusV2{
		TS:           homedrive.Timestamp{Time: now},
		Version:      a.version,
		State:        a.stateName(),
		StartedAt:    homedrive.Timestamp{Time: started},
		UptimeS:      uptime,
		DryRun:       a.reloadedDryRun.Load(),
		PendingUp:    len(a.pushEvents) + len(a.pushRenames),
		LastPush:     homedrive.TS(snap.LastPush),
		LastPull:     homedrive.TS(snap.LastPull),
		LastActivity: homedrive.TS(snap.LastActivity),
		LastPoll:     homedrive.TS(snap.LastPoll),
		Counts:       snap.Counts,
		LastError:    snap.LastError,
		Bisync:       snap.Bisync,
		Local:        snap.Local,
		QuotaUsedPct: a.quota.get(),
	}
	return doc
}

// statusInterval is the publish (and stats flush) interval.
func (a *Agent) statusInterval() time.Duration {
	if d := a.cfg.MQTT.PublishInterval.Duration; d > 0 {
		return d
	}
	return mqtt.DefaultPublishInterval
}

// finalPublish flushes and publishes one last time on clean shutdown so the
// last retained document reflects the final counters.
func (a *Agent) finalPublish() {
	a.flushStats()
	if a.statePub != nil {
		a.statePub.PublishOnce()
	}
}

// flushStats persists the tracker, logging failures (kept in memory and
// retried next tick).
func (a *Agent) flushStats() {
	if err := a.stats.Flush(); err != nil {
		a.log.Error("stats flush failed", "op", "stats_flush", "error", err)
	}
}

// runStatusLoop flushes the tracker and (when MQTT is enabled) publishes
// the status document at start, then every statusInterval, until ctx is
// cancelled. Flushing runs even without MQTT, so /status counters survive
// restarts either way.
func (a *Agent) runStatusLoop(ctx context.Context) {
	tick := func() {
		a.refreshQuota(ctx, time.Now())
		a.flushStats()
		if a.statePub != nil {
			a.statePub.PublishOnce()
		}
	}
	tick()

	ticker := time.NewTicker(a.statusInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tick()
		}
	}
}

// buildStats loads the persisted tracker and wires the status publisher.
// Load failures are logged, not fatal: stats are observability data.
func (a *Agent) buildStats() {
	a.stats = stats.New(stats.Config{Store: a.journal, Log: a.log})
	if err := a.stats.Load(); err != nil {
		a.log.Error("stats load failed", "op", "stats_load", "error", err)
	}
	a.stats.RecordStart()

	if a.mqttReal != nil {
		a.statePub = mqtt.NewStatePublisher(a.mqttReal,
			func() homedrive.StatusV2 { return a.buildStatusV2(time.Now()) },
			a.statusInterval(), a.log)
	}
}
