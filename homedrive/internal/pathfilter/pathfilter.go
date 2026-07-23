// Package pathfilter provides the single doublestar exclude-pattern matcher
// shared by both sync directions: internal/watcher (push side, evaluated at
// watch-add time and event-emission time against absolute local paths) and
// internal/rcloneclient (pull side, evaluated against remote-relative paths
// returned by the Drive full walk and Changes API).
//
// Before this package existed, the two packages each carried their own
// independent copy of the same three-tier doublestar matching logic. That
// duplication risked the exact kind of push/pull drift described in
// homedrive/docs/migrating-rclone-filters.md: a pattern tweak applied to one
// copy but not the other. Centralizing the match here means watcher.exclude
// patterns behave identically regardless of which side evaluates them.
package pathfilter

import (
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// Excluded reports whether relPath matches any of the given doublestar glob
// patterns. relPath must be forward-slash separated and relative to the
// sync root; a leading slash is tolerated and stripped.
//
// Each pattern is tried against relPath in three tiers, in order:
//  1. Direct match: doublestar.Match(pattern, relPath).
//  2. Directory match: doublestar.Match(pattern, relPath+"/"), so a
//     pattern like "**/node_modules" (no trailing "/**") still matches the
//     directory itself when relPath names a directory.
//  3. Bare-directory match: for a pattern ending in "/**" (e.g.
//     "**/.git/**"), also match relPath against the pattern with the
//     "/**" suffix stripped, so the directory itself (".git") is excluded,
//     not just its contents (".git/HEAD").
//
// An empty patterns slice always returns false.
func Excluded(patterns []string, relPath string) bool {
	if len(patterns) == 0 {
		return false
	}
	relPath = strings.TrimPrefix(relPath, "/")

	for _, pattern := range patterns {
		if matched, _ := doublestar.Match(pattern, relPath); matched {
			return true
		}
		if matched, _ := doublestar.Match(pattern, relPath+"/"); matched {
			return true
		}
		if strings.HasSuffix(pattern, "/**") {
			dirPattern := strings.TrimSuffix(pattern, "/**")
			if matched, _ := doublestar.Match(dirPattern, relPath); matched {
				return true
			}
		}
	}
	return false
}
