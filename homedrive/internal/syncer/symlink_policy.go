// symlink_policy.go implements the opt-in that lets homedrive copy the
// content of some symlinks (config symlinks.copy_resolved_links, issue
// #191). The default policy is empty: every link is skipped as before. A
// link is dereferenced only when it sits under a folder matched by a
// pattern, resolves on the same filesystem to a regular file or a directory
// tree inside the root, and that target also lies under a matched folder.
// The link is read only through that resolve step; nothing is ever written
// through it (see localPathFor).
package syncer

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// Extra skip reasons, reported when a link under a matched folder fails an
// opt-in check. Stable codes, never paths.
const (
	// LinkCrossDevice: the target is on another filesystem than the link
	// (or the device cannot be determined).
	LinkCrossDevice = "cross_device"
	// LinkTargetNotMatched: the target is inside the root but not under a
	// folder matched by copy_resolved_links.
	LinkTargetNotMatched = "target_not_matched"
	// LinkNotRegular: the target is neither a regular file nor a directory
	// (socket, FIFO, device).
	LinkNotRegular = "not_regular"
)

// LinkPolicy is the machine-local copy_resolved_links list. A nil or empty
// policy copies nothing.
type LinkPolicy struct {
	patterns []string
}

// NewLinkPolicy returns a policy over patterns (already validated by
// config.Load). The slice is copied.
func NewLinkPolicy(patterns []string) *LinkPolicy {
	return &LinkPolicy{patterns: append([]string(nil), patterns...)}
}

// Patterns returns a copy of the effective pattern list.
func (p *LinkPolicy) Patterns() []string {
	if p == nil {
		return nil
	}
	return append([]string(nil), p.patterns...)
}

// Enabled reports whether any pattern is configured.
func (p *LinkPolicy) Enabled() bool { return p != nil && len(p.patterns) > 0 }

// matches reports whether the root-relative slash path dir is matched by a
// pattern (doublestar: "x/**" matches the folder x itself too).
func (p *LinkPolicy) matches(dir string) bool {
	for _, pat := range p.patterns {
		if ok, _ := doublestar.Match(pat, dir); ok {
			return true
		}
	}
	return false
}

// covers reports whether dir is a matched folder or lies under one.
func (p *LinkPolicy) covers(dir string) bool {
	if !p.Enabled() {
		return false
	}
	for d := dir; ; d = path.Dir(d) {
		if d != "." && d != "" && p.matches(d) {
			return true
		}
		if d == "." || d == "" || d == "/" {
			return false
		}
	}
}

// deviceCheck is sameDevice; tests replace it to simulate a mount point
// inside the root, which a unit test cannot create.
var deviceCheck = sameDevice

// linkDecision is the outcome of examining one symlink.
type linkDecision struct {
	isLink bool
	// copy is true when the link is dereferenced: not skipped, not reported.
	copy bool
	// dir is true when a copied link's target is a directory tree.
	dir bool
	// target is the fully resolved absolute target (only when copy).
	target string
	// reason and raw describe a skipped link (see skippedLink).
	reason, raw string
}

// decideLink examines path (absolute, root-relative slash path rel) with
// Lstat/Readlink and, when the policy applies, one resolve step. Without a
// matching policy the result is exactly classifyLink's.
func decideLink(root, linkPath, rel string, pol *LinkPolicy) linkDecision {
	isLink, reason, raw := classifyLink(root, linkPath)
	if !isLink {
		return linkDecision{}
	}
	skip := linkDecision{isLink: true, reason: reason, raw: raw}
	// Only a link that resolves inside the root can qualify; unresolved and
	// outside-root links keep their reason whatever the policy says.
	if reason != LinkInRoot || !pol.covers(path.Dir(rel)) {
		return skip
	}
	target, err := filepath.EvalSymlinks(linkPath)
	if err != nil {
		return skip
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		realRoot = root
	}
	tfi, err := os.Lstat(target)
	if err != nil {
		skip.reason = LinkUnresolved
		return skip
	}
	if !tfi.IsDir() && !tfi.Mode().IsRegular() {
		skip.reason = LinkNotRegular
		return skip
	}
	lfi, err := os.Lstat(linkPath)
	if err != nil || !deviceCheck(lfi, tfi) {
		skip.reason = LinkCrossDevice
		return skip
	}
	trel, err := filepath.Rel(realRoot, target)
	if err != nil {
		return skip
	}
	trel = filepath.ToSlash(trel)
	covered := pol.covers(path.Dir(trel))
	if tfi.IsDir() {
		covered = pol.covers(trel)
	}
	if !covered {
		skip.reason = LinkTargetNotMatched
		return skip
	}
	return linkDecision{isLink: true, copy: true, dir: tfi.IsDir(), target: target, raw: raw}
}

// UnmatchedPatterns returns the patterns that match no folder under root.
// It walks directories once (not files' content), so call it at start-up
// only, never per pass. It is empty for a disabled policy.
func (p *LinkPolicy) UnmatchedPatterns(ctx context.Context, root string) []string {
	if !p.Enabled() {
		return nil
	}
	seen := make(map[string]bool, len(p.patterns))
	_ = filepath.WalkDir(root, func(fp string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil || !d.IsDir() || fp == root {
			return nil
		}
		rel, rerr := filepath.Rel(root, fp)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		for _, pat := range p.patterns {
			if seen[pat] {
				continue
			}
			if ok, _ := doublestar.Match(pat, rel); ok {
				seen[pat] = true
			}
		}
		if len(seen) == len(p.uniquePatterns()) {
			return fs.SkipAll
		}
		return nil
	})
	var out []string
	for _, pat := range p.patterns {
		if !seen[pat] {
			out = append(out, pat)
		}
	}
	return out
}

func (p *LinkPolicy) uniquePatterns() map[string]struct{} {
	m := make(map[string]struct{}, len(p.patterns))
	for _, pat := range p.patterns {
		m[pat] = struct{}{}
	}
	return m
}

// WarnUnmatched logs one WARN per pattern that matches no folder.
func (p *LinkPolicy) WarnUnmatched(ctx context.Context, root string, log *slog.Logger) {
	for _, pat := range p.UnmatchedPatterns(ctx, root) {
		log.Warn("symlinks.copy_resolved_links pattern matches no folder",
			"op", "symlink_policy", "pattern", pat)
	}
}

// hasLinkPrefix reports whether rel, or any ancestor folder of it, is in set.
func hasLinkPrefix(set map[string]struct{}, rel string) bool {
	if len(set) == 0 {
		return false
	}
	for p := rel; p != "." && p != "" && p != "/"; p = path.Dir(p) {
		if _, ok := set[p]; ok {
			return true
		}
	}
	return false
}

// joinRel appends a slash-separated rel to base.
func joinRel(base, rel string) string {
	if rel == "." || rel == "" {
		return base
	}
	return strings.TrimSuffix(base, "/") + "/" + rel
}
