// symlinks.go holds the symlinks: config block (issue #191): the opt-in that
// lets homedrive copy the content of some symlinks instead of skipping them.
package config

import (
	"fmt"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// SymlinksConfig configures symlink handling. By default (an empty list)
// homedrive never syncs a symlink: every one is skipped and reported.
type SymlinksConfig struct {
	// CopyResolvedLinks is a list of doublestar glob patterns, relative to
	// local_root and slash-separated (for example "Brain/.claude/skills/**").
	// A symlink under a folder matched by a pattern is copied to Drive as
	// content (dereferenced) instead of being skipped, but only when it
	// resolves on the same filesystem to a regular file or a directory tree
	// inside local_root that itself lies under a matched folder. Anything
	// else stays skipped and reported. Machine-local only: it is read from
	// this file, never from the synced tree.
	CopyResolvedLinks []string `yaml:"copy_resolved_links"`
}

// validate rejects patterns that could address anything outside local_root.
func (s SymlinksConfig) validate() error {
	for i, p := range s.CopyResolvedLinks {
		if err := ValidateLinkPattern(p); err != nil {
			return fmt.Errorf("config: symlinks.copy_resolved_links[%d] %q: %w", i, p, err)
		}
	}
	return nil
}

// ValidateLinkPattern reports why p cannot be a copy_resolved_links
// pattern: empty, absolute (leading "/", backslash or drive letter), with a
// ".." component (it could escape the root), or not valid glob syntax.
func ValidateLinkPattern(p string) error {
	switch {
	case strings.TrimSpace(p) == "":
		return fmt.Errorf("pattern is empty")
	case strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) || (len(p) >= 2 && p[1] == ':'):
		return fmt.Errorf("pattern must be relative to local_root, not absolute")
	case strings.Contains(p, `\`):
		return fmt.Errorf("pattern must use '/' separators")
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return fmt.Errorf("pattern must not contain '..' (it would escape local_root)")
		}
	}
	if !doublestar.ValidatePattern(p) {
		return fmt.Errorf("invalid glob syntax")
	}
	return nil
}
