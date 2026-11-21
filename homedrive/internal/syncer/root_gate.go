package syncer

import (
	"context"
	"errors"
	"log/slog"
)

// WithRootGate makes the push syncer ask gate before every remote attempt
// (issue #170). While it refuses, the work is dropped with no remote call and
// no journal change; bisync reconciles local changes once local_root is back.
func WithRootGate(gate RootGate) Option {
	return func(s *Syncer) { s.rootGate = gate }
}

// retryGated runs fn under retryFunc, asking the root gate before every
// attempt. It is called with the bisync read lock held, so the check cannot
// predate a whole bisync pass. blocked is true when the gate refused: nothing
// was attempted and the caller must record nothing.
func (s *Syncer) retryGated(
	ctx context.Context, logger *slog.Logger, desc string, fn func(context.Context) error,
) (blocked bool, err error) {
	err = retryFunc(ctx, s.cfg.Retry, logger, desc, s.sleepFn, func(ctx context.Context) error {
		if s.rootGate != nil {
			if gateErr := s.rootGate.Check(); gateErr != nil {
				logger.Debug("push skipped: local_root is not the synced tree", "origin", "local")
				return gateErr
			}
		}
		return fn(ctx)
	})
	if errors.Is(err, ErrRootBlocked) {
		return true, nil
	}
	return false, err
}

// gateErr returns nil when the root gate (if any) lets bisync act, or an
// error wrapping ErrRootBlocked.
func (b *Bisync) gateErr() error {
	if b.gate == nil {
		return nil
	}
	return b.gate.Check()
}

// gateRefuses reports whether the root gate vetoes a pass now (issue #170).
func (b *Bisync) gateRefuses() bool {
	if err := b.gateErr(); err != nil {
		b.log.Debug("bisync skipped: local_root is not the synced tree", "error", err)
		return true
	}
	return false
}
