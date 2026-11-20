package syncer

import "log/slog"

// WithRootGate makes the push syncer ask gate before every event and
// directory rename (issue #170). While it refuses, the work is dropped with
// no remote call and no journal change; bisync reconciles local changes once
// local_root is back.
func WithRootGate(gate RootGate) Option {
	return func(s *Syncer) { s.rootGate = gate }
}

// rootBlocked reports whether the root gate refuses push work now. The guard
// itself logs, audits and publishes the transition.
func (s *Syncer) rootBlocked(logger *slog.Logger) bool {
	if s.rootGate == nil {
		return false
	}
	if err := s.rootGate.Check(); err != nil {
		logger.Debug("push skipped: local_root is not the synced tree", "origin", "local")
		return true
	}
	return false
}
