// agent_run.go implements Agent.Run: starting every component, pumping
// watcher events into the push syncer, and orchestrating graceful shutdown.
package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/syncer"
	"github.com/asnowfix/home-drive/homedrive/internal/watcher"
)

// Run starts every component of the sync engine and blocks until ctx is
// cancelled (SIGTERM/SIGINT), then drains in-flight work and shuts down
// cleanly. See PLAN.md §3.2 for the runtime topology.
func (a *Agent) Run(ctx context.Context) error {
	a.startTime = time.Now()
	a.log.Info("starting homedrive agent",
		"version", a.version,
		"config", a.configPath,
		"local_root", a.cfg.LocalRoot,
		"remote", a.cfg.Remote,
		"dry_run", a.cfg.DryRun,
	)

	var wg sync.WaitGroup
	a.startComponents(ctx, &wg)

	httpErrCh := make(chan error, 1)
	go func() { httpErrCh <- a.httpSrv.ListenAndServe() }()

	a.watchSIGHUP(ctx, &wg)

	<-ctx.Done()
	a.log.Info("shutdown signal received, draining in-flight work")

	a.shutdownHTTP(httpErrCh)
	wg.Wait() // watcher, pump, push syncer, pull loop, bisync all drained
	a.shutdownMQTT()
	a.shutdownStore()

	a.log.Info("homedrive agent stopped")
	return nil
}

// startComponents launches the watcher, pump, push syncer, pull loop, and
// bisync ticker as tracked goroutines.
func (a *Agent) startComponents(ctx context.Context, wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := a.watch.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
			a.log.Error("watcher stopped with error", "error", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		a.pumpWatcherEvents(ctx)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		a.pushSyncer.Run(ctx, a.pushEvents, a.pushRenames)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		a.runPullLoop(ctx)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := a.bisync.Run(ctx); err != nil && !errors.Is(err, syncer.ErrBisyncCanceled) {
			a.log.Error("bisync stopped with error", "error", err)
		}
	}()
}

// watchSIGHUP starts a goroutine that reloads configuration on SIGHUP,
// distinct from the SIGTERM/SIGINT shutdown context so a reload never
// triggers a restart (systemd ExecReload=/bin/kill -HUP $MAINPID).
func (a *Agent) watchSIGHUP(ctx context.Context, wg *sync.WaitGroup) {
	sighup := make(chan os.Signal, 1)
	signal.Notify(sighup, syscall.SIGHUP)

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer signal.Stop(sighup)
		for {
			select {
			case <-ctx.Done():
				return
			case <-sighup:
				if err := a.Reload(ctx); err != nil {
					a.log.Error("config reload failed", "error", err)
				}
			}
		}
	}()
}

// shutdownHTTP gracefully stops the HTTP control endpoint with a bounded
// timeout, then waits for ListenAndServe to actually return.
func (a *Agent) shutdownHTTP(httpErrCh <-chan error) {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.httpSrv.Shutdown(shutdownCtx); err != nil {
		a.log.Error("http server shutdown error", "error", err)
	}
	if err := <-httpErrCh; err != nil {
		a.log.Error("http server exited with error", "error", err)
	}
}

// shutdownMQTT publishes the offline LWT payload and disconnects, if MQTT
// is enabled.
func (a *Agent) shutdownMQTT() {
	if a.mqttReal == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.mqttReal.Close(ctx); err != nil {
		a.log.Error("mqtt close error", "error", err)
	}
}

// shutdownStore closes the BoltDB journal and audit log file. Must be
// called only after every goroutine that might still write to them has
// been drained (via wg.Wait in Run).
func (a *Agent) shutdownStore() {
	if err := a.journal.Close(); err != nil {
		a.log.Error("journal close error", "error", err)
	}
	closeAuditFile(a.auditFile, a.log)
}

// runPullLoop polls the Drive Changes API on cfg.Pull.ChangesAPIInterval,
// taking bisyncMu.RLock around each cycle so it never overlaps a bisync
// run (PLAN.md §7.2). syncer.Puller has no built-in bisync coordination,
// so this loop owns its own timer rather than calling puller.Run.
//
// It uses a *time.Timer, re-armed after every cycle with
// a.puller.NextPollInterval(), not a fixed *time.Ticker -- deliberately
// mirroring Puller.Run's own mechanics (same doubling-capped OAuth
// "no client_id" backoff, issue #67). A plain Ticker at the static
// interval was the bug an issue #67 PR review caught: the backoff logic
// lived entirely in Puller.Run, which this loop never calls, so
// production kept polling Drive's token endpoint at full 30s cadence
// through a sustained outage while a test that only drove Run() directly
// stayed green. See NextPollInterval's doc comment for why the two
// call sites are kept in sync by sharing that one method instead of each
// computing the backoff themselves.
func (a *Agent) runPullLoop(ctx context.Context) {
	interval := a.cfg.Pull.ChangesAPIInterval.Duration
	if interval <= 0 {
		interval = 30 * time.Second
	}

	a.pollOnceGuarded(ctx)

	timer := time.NewTimer(a.puller.NextPollInterval())
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			a.pollOnceGuarded(ctx)
			next := a.puller.NextPollInterval()
			if next != interval {
				a.log.Warn("polling backed off: oauth client_id/client_secret not configured for remote",
					"op", "pull",
					"next_poll_in", next.String(),
				)
			}
			timer.Reset(next)
		}
	}
}

// pollOnceGuarded runs a single Changes API poll cycle under bisyncMu.RLock.
func (a *Agent) pollOnceGuarded(ctx context.Context) {
	a.bisyncMu.RLock()
	defer a.bisyncMu.RUnlock()

	err := a.puller.PollOnce(ctx)
	if err != nil && !errors.Is(err, context.Canceled) {
		a.log.Error("poll error", "error", err)
		return
	}
	a.lastPullUnixNano.Store(time.Now().UnixNano())
}

// pumpWatcherEvents converts watcher.WatchEvent values into the syncer
// Event/DirRename types consumed by the push worker pool, dropping events
// while paused (see Pause/Resume in agent_control.go).
func (a *Agent) pumpWatcherEvents(ctx context.Context) {
	in := a.watch.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case we, ok := <-in:
			if !ok {
				return
			}
			a.dispatchWatchEvent(ctx, we)
		}
	}
}

// dispatchWatchEvent forwards a single watch event to the appropriate
// push-syncer channel, or drops it (with a debug log) while paused.
func (a *Agent) dispatchWatchEvent(ctx context.Context, we watcher.WatchEvent) {
	if a.paused.Load() {
		a.log.Debug("push paused, dropping watcher event")
		return
	}

	if we.DirRename != nil {
		dr := syncer.DirRename{From: we.DirRename.From, To: we.DirRename.To, At: we.DirRename.At}
		select {
		case a.pushRenames <- dr:
			a.lastPushUnixNano.Store(time.Now().UnixNano())
		case <-ctx.Done():
		}
		return
	}
	if we.Event != nil {
		ev := syncer.Event{Path: we.Event.Path, Op: toSyncerOp(we.Event.Op), At: we.Event.At}
		select {
		case a.pushEvents <- ev:
			a.lastPushUnixNano.Store(time.Now().UnixNano())
		case <-ctx.Done():
		}
	}
}

// toSyncerOp maps watcher.Op to syncer.Op. watcher.OpDirRename is never
// set on a watcher.Event (paired renames arrive via the separate DirRename
// field), so it is not represented here.
func toSyncerOp(op watcher.Op) syncer.Op {
	switch op {
	case watcher.OpCreate:
		return syncer.OpCreate
	case watcher.OpWrite:
		return syncer.OpWrite
	case watcher.OpRemove:
		return syncer.OpRemove
	case watcher.OpRename:
		return syncer.OpRename
	default:
		return syncer.OpWrite
	}
}
