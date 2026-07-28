// agent_control.go implements the httpctl.Deps interfaces (Pausable,
// Resyncable, Reloadable, StatusProvider, HealthChecker) so the HTTP
// control endpoint can query and control the running Agent. See
// PLAN.md §12 for the route table these back.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	httpctl "github.com/asnowfix/home-drive/homedrive/internal/http"

	"github.com/asnowfix/home-drive/homedrive/internal/config"
)

// Pause stops the watcher pump from forwarding events to the push worker
// pool. Buffered events are dropped (and logged) rather than blocking the
// watcher, per the fsnotify non-blocking send design in internal/watcher.
// Pull and bisync are unaffected: PLAN.md §12 describes /pause as pausing
// "watcher + workers" (the push side), while pull is read-only and safe
// to keep running.
func (a *Agent) Pause(_ context.Context) error {
	a.paused.Store(true)
	a.log.Info("agent paused", "op", "pause")
	return nil
}

// Resume re-enables forwarding of watcher events to the push worker pool.
func (a *Agent) Resume(_ context.Context) error {
	a.paused.Store(false)
	a.log.Info("agent resumed", "op", "resume")
	return nil
}

// ForceResync triggers an immediate bisync pass, bypassing the hourly
// ticker. Returns syncer.ErrBisyncRunning if one is already in progress.
func (a *Agent) ForceResync(ctx context.Context) error {
	return a.bisync.ForceRun(ctx)
}

// RunChainRepair runs the one-time nested .old.<N> chain repair pass
// (PLAN.md §11.5, issue #65 §3) on demand, independent of the automatic
// first-pass-after-upgrade run. Backs POST /conflict/repair and
// `homedrive ctl conflict repair`.
func (a *Agent) RunChainRepair(ctx context.Context, dryRun bool) (httpctl.RepairReport, error) {
	report, err := a.bisync.RunChainRepair(ctx, dryRun)
	if err != nil {
		return httpctl.RepairReport{}, err
	}

	links := make([]httpctl.RepairedLink, 0, len(report.Links))
	for _, l := range report.Links {
		links = append(links, httpctl.RepairedLink{OldPath: l.OldPath, NewPath: l.NewPath, Side: l.Side})
	}
	return httpctl.RepairReport{
		Scanned:  report.Scanned,
		Repaired: len(report.Links),
		Links:    links,
		DryRun:   dryRun,
	}, nil
}

// Reload re-reads the configuration file. Only the log level and the
// dry_run flag reported by /status are applied live; local_root, remote,
// watcher.exclude, push.workers, retry settings, and other structural
// settings require a full restart because they are baked into already
// -constructed components (watcher, syncer, puller, bisync) by design --
// re-wiring those live is out of scope for this issue (see PLAN.md §12,
// "Hot reload via SIGHUP or POST /reload").
func (a *Agent) Reload(_ context.Context) error {
	newCfg, err := config.Load(a.configPath)
	if err != nil {
		return fmt.Errorf("reload config: %w", err)
	}

	if a.logLevel != nil {
		a.logLevel.Set(parseLogLevel(newCfg.Logging.Level))
	}
	a.reloadedDryRun.Store(newCfg.DryRun)

	a.log.Info("config reloaded",
		"op", "reload",
		"config", a.configPath,
		"log_level", newCfg.Logging.Level,
		"dry_run", newCfg.DryRun,
	)
	a.log.Warn("reload applies log level and status-reported dry_run only; " +
		"local_root, remote, watcher excludes, push workers, and retry " +
		"settings require a full restart to take effect")
	return nil
}

// Status reports the agent's current state for GET /status.
func (a *Agent) Status(ctx context.Context) (httpctl.StatusInfo, error) {
	state := "running"
	if a.paused.Load() {
		state = "paused"
	}

	return httpctl.StatusInfo{
		State:         state,
		Version:       a.version,
		PendingUp:     len(a.pushEvents) + len(a.pushRenames),
		PendingDown:   0, // pull applies each Changes API page synchronously; no queue to report
		LastPush:      unixNanoToRFC3339(a.lastPushUnixNano.Load()),
		LastPull:      unixNanoToRFC3339(a.lastPullUnixNano.Load()),
		QuotaUsedPct:  a.quotaUsedPct(ctx),
		Conflicts24h:  0, // not yet tracked; requires a metrics/quota follow-up
		BytesUp24h:    0, // not yet tracked; requires a metrics/quota follow-up
		BytesDown24h:  0, // not yet tracked; requires a metrics/quota follow-up
		DryRun:        a.reloadedDryRun.Load(),
		UptimeSeconds: int64(time.Since(a.startTime).Seconds()),
	}, nil
}

// quotaUsedPct performs a live quota lookup for /status. Errors are
// swallowed (returning 0) since quota is best-effort display data, not a
// health signal in its own right.
func (a *Agent) quotaUsedPct(ctx context.Context) int {
	q, err := a.rfs.Quota(ctx)
	if err != nil || q.Total <= 0 {
		return 0
	}
	return int(float64(q.Used) / float64(q.Total) * 100)
}

// Healthz aggregates health from the store, rclone remote, and (if
// enabled) MQTT for GET /healthz.
func (a *Agent) Healthz(_ context.Context) (httpctl.HealthResult, error) {
	components := []httpctl.ComponentHealth{a.storeHealth(), a.rcloneHealth()}
	if a.mqttReal != nil {
		components = append(components, a.mqttHealth())
	}

	healthy := true
	for _, c := range components {
		if !c.Healthy {
			healthy = false
		}
	}
	return httpctl.HealthResult{Healthy: healthy, Components: components}, nil
}

func (a *Agent) storeHealth() httpctl.ComponentHealth {
	if _, err := a.journal.Count(); err != nil {
		return httpctl.ComponentHealth{Name: "store", Healthy: false, Message: err.Error()}
	}
	return httpctl.ComponentHealth{Name: "store", Healthy: true}
}

// rcloneHealth reports whether the remote filesystem was initialized. It
// deliberately does not make a live Drive API call on every /healthz poll
// to avoid burning API quota on a monitoring endpoint.
func (a *Agent) rcloneHealth() httpctl.ComponentHealth {
	if a.rfs == nil {
		return httpctl.ComponentHealth{Name: "rclone", Healthy: false, Message: "not initialized"}
	}
	return httpctl.ComponentHealth{
		Name:    "rclone",
		Healthy: true,
		Message: "initialized; no live probe (avoids Drive API quota use on health checks)",
	}
}

func (a *Agent) mqttHealth() httpctl.ComponentHealth {
	if a.mqttReal.Connected() {
		return httpctl.ComponentHealth{Name: "mqtt", Healthy: true}
	}
	return httpctl.ComponentHealth{Name: "mqtt", Healthy: false, Message: "disconnected"}
}

// unixNanoToRFC3339 renders a unix-nano timestamp (0 meaning "never") as
// an RFC3339 string, or "" for 0 so the JSON field is omitted.
func unixNanoToRFC3339(n int64) string {
	if n == 0 {
		return ""
	}
	return time.Unix(0, n).UTC().Format(time.RFC3339)
}

// parseLogLevel maps a config log level string to a slog.Level, defaulting
// to Info for unrecognized values.
func parseLogLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
