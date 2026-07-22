package rcloneclient

import (
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// matchesExclude evaluates doublestar glob patterns against a remote-relative
// path (forward slashes, no leading slash). It mirrors the semantics of
// watcher.filter.excluded so push-side and pull-side exclusion behave the
// same way for the same watcher.exclude patterns (PLAN.md §7, §10).
//
// Kept as a single, clearly-named function so issue #54 (filter parity
// between push and pull) can extend or share matching logic without
// duplicating it.
func matchesExclude(patterns []string, relPath string) bool {
	if len(patterns) == 0 {
		return false
	}

	relPath = strings.TrimPrefix(relPath, "/")

	for _, pattern := range patterns {
		if matched, _ := doublestar.Match(pattern, relPath); matched {
			return true
		}
		// Also try matching with a trailing slash for directory patterns.
		if matched, _ := doublestar.Match(pattern, relPath+"/"); matched {
			return true
		}
		// For patterns like "**/.git/**", a path equal to ".git" itself
		// should also be treated as excluded.
		if strings.HasSuffix(pattern, "/**") {
			dirPattern := strings.TrimSuffix(pattern, "/**")
			if matched, _ := doublestar.Match(dirPattern, relPath); matched {
				return true
			}
		}
	}
	return false
}
