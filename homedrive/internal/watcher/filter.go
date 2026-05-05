package watcher

import (
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// filter evaluates doublestar exclusion patterns against paths relative to
// a root directory. Patterns are checked at watch-add time and at event
// emission time (defense in depth).
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

	for _, pattern := range f.patterns {
		// Match against the relative path.
		if matched, _ := doublestar.Match(pattern, rel); matched {
			return true
		}
		// Also try matching with a trailing slash for directory patterns.
		if matched, _ := doublestar.Match(pattern, rel+"/"); matched {
			return true
		}
		// For patterns like "**/.git/**", check if the path is a prefix
		// that would match (e.g. ".git" itself matches "**/.git/**").
		if strings.HasSuffix(pattern, "/**") {
			dirPattern := strings.TrimSuffix(pattern, "/**")
			if matched, _ := doublestar.Match(dirPattern, rel); matched {
				return true
			}
		}
	}
	return false
}
