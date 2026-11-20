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

// IsTrash reports whether relPath (forward-slash, relative to the sync root)
// is, or lies under, a freedesktop.org topdir trash directory directly in the
// sync root: ".Trash" or ".Trash-<uid>". When the root is itself a mount
// point, homedrive's local deletions land in <root>/.Trash-<uid> (issue
// #155), which must never be synced, whatever watcher.exclude says.
func IsTrash(relPath string) bool {
	relPath = strings.TrimPrefix(relPath, "/")
	first, _, _ := strings.Cut(relPath, "/")
	if first == ".Trash" {
		return true
	}
	uid, ok := strings.CutPrefix(first, ".Trash-")
	if !ok || uid == "" {
		return false
	}
	for _, c := range uid {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

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
// Independently of patterns, a freedesktop.org Trash directory directly under
// the sync root (see IsTrash) is always excluded (issue #155).
func Excluded(patterns []string, relPath string) bool {
	relPath = strings.TrimPrefix(relPath, "/")
	if IsTrash(relPath) {
		return true
	}

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
