package syncer

import "time"

// DeleteGuard is the expected-deletion set (issue #154). Skeleton: the
// behaviour lands in the next commit.
type DeleteGuard struct{ inner LocalRemover }

// NewDeleteGuard wraps inner so deletions made through it are remembered.
func NewDeleteGuard(root string, inner LocalRemover, ttl time.Duration, now func() time.Time) *DeleteGuard {
	return &DeleteGuard{inner: inner}
}

// Remove deletes through the wrapped remover.
func (g *DeleteGuard) Remove(path string) error { return removeLocalPath(g.inner, path) }

// Expected reports whether rel was just deleted by homedrive itself.
func (g *DeleteGuard) Expected(rel string) bool { return false }

// Forget drops any mark for rel.
func (g *DeleteGuard) Forget(rel string) {}

// WithDeleteGuard makes the push syncer drop remove events for deletions
// recorded in g.
func WithDeleteGuard(g *DeleteGuard) Option { return func(s *Syncer) {} }
