package watcher

import (
	"path/filepath"

	"github.com/asnowfix/home-drive/homedrive/internal/pathfilter"
)

// filter evaluates doublestar exclusion patterns against paths relative to
// a root directory. Patterns are checked at watch-add time and at event
// emission time (defense in depth). The actual glob matching is delegated
// to internal/pathfilter so push-side (here) and pull-side
// (internal/rcloneclient) exclusion behave identically for the same
// watcher.exclude patterns -- see homedrive/docs/migrating-rclone-filters.md.
type filter struct {
	root     string
	patterns []string
}

// newFilter creates a filter with the given root and exclusion patterns.
func newFilter(root string, patterns []string) *filter {
	return &filter{
		root:     filepath.Clean(root),
		patterns: patterns,
	}
}

// excluded returns true if the absolute path matches any exclusion pattern.
func (f *filter) excluded(absPath string) bool {
	if len(f.patterns) == 0 {
		return false
	}

	rel, err := filepath.Rel(f.root, absPath)
	if err != nil {
		return false
	}
	// Normalize to forward slashes for doublestar matching.
	rel = filepath.ToSlash(rel)

	return pathfilter.Excluded(f.patterns, rel)
}
