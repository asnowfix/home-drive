package rcloneclient

import "log/slog"

// RemoteIDResolver maps Drive file IDs back to the remote-relative paths a
// previous sync journaled for them. It is injected (see
// RcloneFSConfig.IDResolver) so this package does not depend on the store
// package.
//
// RemoteIDPaths returns every journaled ID with the paths recorded for it:
// one path per ID is the normal case, more than one means the journal is
// inconsistent and the ID must not be trusted. Entries without a recorded
// ID (written before issue #157) are absent. The map is a point-in-time
// snapshot the caller may keep for the duration of one poll.
type RemoteIDResolver interface {
	RemoteIDPaths() (map[string][]string, error)
}

// idSnapshot lazily loads one RemoteIDResolver snapshot and shares it
// across all unknown-ID Removed changes of a single poll. The journal is
// only scanned when a poll actually meets an uncached removed ID, and then
// once, not once per change: a mass deletion while the agent was down must
// not cost one full journal scan per deleted file.
type idSnapshot struct {
	res    RemoteIDResolver
	paths  map[string][]string
	loaded bool
}

// lookup returns the single journaled path for id. ok is false when there
// is no resolver, the snapshot cannot be read, the ID is not journaled, or
// it is journaled under several paths (journal inconsistency; Warn and
// drop, bisync stays the backstop).
func (s *idSnapshot) lookup(log *slog.Logger, id string) (string, bool) {
	if s == nil || s.res == nil {
		return "", false
	}
	if !s.loaded {
		s.loaded = true
		m, err := s.res.RemoteIDPaths()
		if err != nil {
			log.Warn("ListChanges: cannot read journal to resolve removed IDs",
				"op", "ListChanges", "error", err)
			return "", false
		}
		s.paths = m
	}
	ps := s.paths[id]
	switch len(ps) {
	case 0:
		return "", false
	case 1:
		return ps[0], true
	default:
		log.Warn("ListChanges: skipping removed file whose ID is journaled under several paths",
			"op", "ListChanges", "file_id", id, "paths", ps)
		return "", false
	}
}
