package syncer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/oldsuffix"
)

// oauthBackoffMax bounds how far the poll interval backs off once Drive
// Changes API polling starts failing on every attempt because this
// remote's rclone.conf has no client_id/client_secret configured (issue
// #67). This condition cannot self-heal by retrying: it clears only once
// an operator configures OAuth credentials and restarts the process (see
// homedrive/README.md's prerequisites). Retrying at the normal 30s
// interval just burns Drive API quota and floods the journal for a
// failure retrying can never fix. 30 minutes still surfaces incremental
// changes reasonably promptly once the fix lands, without hammering a
// broken remote meanwhile -- and the hourly bisync safety net (PLAN.md
// §7.2) keeps carrying all sync traffic, unaffected, the whole time.
const oauthBackoffMax = 30 * time.Minute

// PullerConfig configures the pull loop.
type PullerConfig struct {
	// Interval between polling cycles (default 30s).
	Interval time.Duration

	// LocalRoot is the absolute path to the local sync directory.
	LocalRoot string

	// ConflictPolicy determines how conflicts are resolved.
	ConflictPolicy ConflictPolicy

	// Matcher controls the .old.<N> suffix format used when naming
	// conflict losers. Defaults to the default ".old.%d" format if nil
	// (see oldsuffix.New). Set from config.ConflictCfg.OldSuffixFormat
	// during wiring (cmd/homedrive/agent.go).
	Matcher *oldsuffix.Matcher

	// Retention bounds .old.<N> conflict-loser retention (PLAN.md
	// §11.5), applied inline right after a new loser is recorded.
	Retention RetentionPolicy

	// Auditor, if non-nil, receives "conflict_gc" JSONL lines from the
	// retention GC. Distinct from the AuditLogger passed to NewPuller
	// (which only forwards a subset of AuditEntry fields): this is the
	// same *store.Auditor instance used directly so the Reason field
	// reaches the log.
	Auditor *Auditor

	// DryRun when true logs intended actions without writing.
	DryRun bool
}

// Puller polls the Drive Changes API and downloads remote modifications.
type Puller struct {
	cfg    PullerConfig
	remote RemoteFS
	store  Store
	audit  AuditLogger
	pub    Publisher
	log    *slog.Logger
	clock  func() time.Time

	// oauthMissingStreak counts consecutive poll cycles whose failure was
	// classified as ErrOAuthClientMissing (see fetchChanges). It drives
	// nextPollInterval's backoff and is reset to 0 by any other outcome
	// -- success or a different error (issue #67).
	oauthMissingStreak int
}

// NewPuller creates a Puller. Pass a nil Publisher to disable MQTT events.
func NewPuller(
	cfg PullerConfig,
	remote RemoteFS,
	store Store,
	audit AuditLogger,
	pub Publisher,
	log *slog.Logger,
	clockFn func() time.Time,
) *Puller {
	if cfg.Interval <= 0 {
		cfg.Interval = 30 * time.Second
	}
	if cfg.ConflictPolicy == "" {
		cfg.ConflictPolicy = PolicyNewerWins
	}
	if cfg.Matcher == nil {
		cfg.Matcher, _ = oldsuffix.New("") // never errors for the empty/default format
	}
	if clockFn == nil {
		clockFn = time.Now
	}
	if log == nil {
		log = slog.Default()
	}
	return &Puller{
		cfg:    cfg,
		remote: remote,
		store:  store,
		audit:  audit,
		pub:    pub,
		log:    log,
		clock:  clockFn,
	}
}

// Run starts the pull polling loop. It blocks until ctx is cancelled.
// The first poll happens immediately, then every cfg.Interval.
func (p *Puller) Run(ctx context.Context) error {
	p.log.Info("puller starting",
		"interval", p.cfg.Interval.String(),
		"dry_run", p.cfg.DryRun,
		"local_root", p.cfg.LocalRoot,
	)

	// Run one cycle immediately at startup.
	if err := p.poll(ctx); err != nil && !errors.Is(err, context.Canceled) {
		p.log.Error("poll error", "error", err)
	}

	// A time.Timer (not time.Ticker) because the interval changes: it
	// backs off while poll cycles keep failing to the OAuth
	// "no client_id" class, and is restored the moment that stops (see
	// nextPollInterval).
	timer := time.NewTimer(p.nextPollInterval())
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			p.log.Info("puller stopping", "reason", ctx.Err())
			return ctx.Err()
		case <-timer.C:
			if err := p.poll(ctx); err != nil && !errors.Is(err, context.Canceled) {
				p.log.Error("poll error", "error", err)
			}
			next := p.nextPollInterval()
			if next != p.cfg.Interval {
				p.log.Warn("polling backed off: oauth client_id/client_secret not configured for remote",
					"op", "pull",
					"next_poll_in", next.String(),
				)
			}
			timer.Reset(next)
		}
	}
}

// nextPollInterval computes the delay before the next poll cycle. It
// backs off exponentially, doubling per consecutive ErrOAuthClientMissing
// failure and capped at oauthBackoffMax; any other outcome keeps (or
// restores) the configured cfg.Interval. See oauthBackoffMax's doc
// comment for why this narrow class, specifically, backs off (issue #67).
func (p *Puller) nextPollInterval() time.Duration {
	if p.oauthMissingStreak == 0 {
		return p.cfg.Interval
	}
	interval := p.cfg.Interval
	for i := 1; i < p.oauthMissingStreak && interval < oauthBackoffMax; i++ {
		interval *= 2
	}
	if interval > oauthBackoffMax {
		interval = oauthBackoffMax
	}
	return interval
}

// PollOnce executes a single pull cycle. Exported for testing.
func (p *Puller) PollOnce(ctx context.Context) error {
	return p.poll(ctx)
}

// poll executes one pull cycle: fetch changes, process each one.
func (p *Puller) poll(ctx context.Context) error {
	token, err := p.ensurePageToken(ctx)
	if err != nil {
		p.log.Error("failed to get page token",
			"op", "pull",
			"error", err,
		)
		return fmt.Errorf("getting page token: %w", err)
	}

	changes, err := p.fetchChanges(ctx, token)
	if err != nil {
		return err
	}

	if len(changes.Items) == 0 {
		p.log.Debug("no remote changes",
			"op", "pull",
			"page_token", token,
		)
		// Still persist the next token even if no changes.
		return p.persistToken(ctx, changes.NextPageToken)
	}

	p.log.Info("processing remote changes",
		"op", "pull",
		"count", len(changes.Items),
		"origin", "remote",
	)

	var lastErr error
	for i := range changes.Items {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := p.processChange(ctx, changes.Items[i]); err != nil {
			lastErr = err
			// Continue processing remaining changes; individual failures
			// are logged and emit pull.failure events.
		}
	}

	// Persist the new token only after processing all changes.
	if err := p.persistToken(ctx, changes.NextPageToken); err != nil {
		return err
	}

	return lastErr
}

// ensurePageToken retrieves the persisted page token from the store,
// or obtains a fresh start token if none exists.
func (p *Puller) ensurePageToken(ctx context.Context) (string, error) {
	token, err := p.store.GetPageToken(ctx)
	if err != nil {
		return "", fmt.Errorf("reading page token from store: %w", err)
	}
	if token != "" {
		return token, nil
	}

	// No persisted token: obtain a fresh start token.
	p.log.Info("no persisted page token, obtaining start token",
		"op", "pull",
	)
	startToken, err := p.remote.GetStartPageToken(ctx)
	if err != nil {
		return "", fmt.Errorf("obtaining start page token: %w", err)
	}
	if err := p.store.SetPageToken(ctx, startToken); err != nil {
		return "", fmt.Errorf("persisting start page token: %w", err)
	}
	return startToken, nil
}

// fetchChanges calls ListChanges and handles 410 GONE by resetting.
func (p *Puller) fetchChanges(ctx context.Context, token string) (Changes, error) {
	changes, err := p.remote.ListChanges(ctx, token)
	if err == nil {
		p.oauthMissingStreak = 0
		return changes, nil
	}

	// Reset-worthy if it wraps ErrGone (the normal case) OR ErrTokenRejected
	// alone (defensive: ErrTokenRejected is only ever supposed to be
	// produced together with ErrGone via rcloneclient.NewTokenRejectedErr,
	// but checking it here too means a future call site that gets that
	// composition wrong loses only the distinguishable log line, not the
	// reset itself -- issue #64 PR review item 2).
	if !errors.Is(err, ErrGone) && !errors.Is(err, ErrTokenRejected) {
		p.log.Error("ListChanges failed",
			"op", "pull",
			"error", err,
			"page_token", token,
		)
		p.emitPullFailure("", err)
		// Track consecutive OAuth "no client_id" failures for
		// nextPollInterval's backoff (issue #67); any other error resets
		// the streak, since it is not the permanent condition backoff
		// exists for.
		if errors.Is(err, ErrOAuthClientMissing) {
			p.oauthMissingStreak++
		} else {
			p.oauthMissingStreak = 0
		}
		return Changes{}, fmt.Errorf("listing changes: %w", err)
	}

	// A 410/400 reset is a different, self-healing failure class, not the
	// permanent OAuth condition backoff targets.
	p.oauthMissingStreak = 0

	// Token is unusable, reset it. This covers two distinguishable causes
	// (issue #64): the classic HTTP 410 GONE (Drive once recognized this
	// token and it has since expired), and a wrapped ErrTokenRejected
	// (currently: any HTTP 400 from changes.list -- Drive never recognized
	// this token at all, e.g. a corrupted/stale-shape token surviving an
	// upgrade; see rcloneclient.isBadPageTokenErr for why Drive's actual
	// 400 responses don't distinguish this from any other 400 in a
	// machine-readable or message-text way). Both take the identical
	// reset-and-full-walk path below; only the log line and MQTT event
	// text differ, so operators/logs can tell the two apart.
	resetReason := "page token expired (410 GONE), resetting"
	if errors.Is(err, ErrTokenRejected) {
		resetReason = "page token rejected by Drive (not 410 GONE), resetting"
	}
	p.log.Warn(resetReason,
		"op", "pull",
		"stale_token", token,
	)
	if p.pub != nil {
		_ = p.pub.PublishJSON(p.pub.Topic("events", "pull.failure"), map[string]any{
			"ts":    p.clock().UTC().Format(time.RFC3339),
			"type":  "pull.failure",
			"error": resetReason,
		})
	}

	newToken, err := p.remote.GetStartPageToken(ctx)
	if err != nil {
		return Changes{}, fmt.Errorf("obtaining new start page token after 410: %w", err)
	}
	if err := p.store.SetPageToken(ctx, newToken); err != nil {
		return Changes{}, fmt.Errorf("persisting reset page token: %w", err)
	}

	// Retry with the fresh token.
	changes, err = p.remote.ListChanges(ctx, newToken)
	if err != nil {
		return Changes{}, fmt.Errorf("listing changes after token reset: %w", err)
	}
	return changes, nil
}

// persistToken saves the next page token to the store.
func (p *Puller) persistToken(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	if err := p.store.SetPageToken(ctx, token); err != nil {
		return fmt.Errorf("persisting page token: %w", err)
	}
	return nil
}

// emitPullSuccess publishes a pull.success MQTT event.
func (p *Puller) emitPullSuccess(path string, bytes int64) {
	if p.pub == nil {
		return
	}
	_ = p.pub.PublishJSON(p.pub.Topic("events", "pull.success"), map[string]any{
		"ts":    p.clock().UTC().Format(time.RFC3339),
		"type":  "pull.success",
		"path":  path,
		"bytes": bytes,
	})
}

// emitPullFailure publishes a pull.failure MQTT event.
func (p *Puller) emitPullFailure(path string, err error) {
	if p.pub == nil {
		return
	}
	payload := map[string]any{
		"ts":    p.clock().UTC().Format(time.RFC3339),
		"type":  "pull.failure",
		"error": err.Error(),
	}
	if path != "" {
		payload["path"] = path
	}
	_ = p.pub.PublishJSON(p.pub.Topic("events", "pull.failure"), payload)
}

// logAudit writes an entry to the audit log, ignoring errors (best-effort).
func (p *Puller) logAudit(entry AuditEntry) {
	if p.audit == nil {
		return
	}
	if err := p.audit.Log(entry); err != nil {
		p.log.Error("failed to write audit log",
			"op", entry.Op,
			"path", entry.Path,
			"error", err,
		)
	}
}
