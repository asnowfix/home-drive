// symlink_walk.go is the bisync side of the copy_resolved_links opt-in
// (issue #191): it lists the content behind a dereferenced symlink and keeps
// every path at or under such a link push-only, so bisync never writes
// through a link.
package syncer

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/asnowfix/home-drive/homedrive/internal/pathfilter"
)

// linkWalk accumulates what one walkLocal pass learns about symlinks.
type linkWalk struct {
	files []LocalFileInfo        // content listed through dereferenced links
	found map[string]skippedLink // skipped links, reported by the tracker
	deref map[string]struct{}    // dereferenced link paths (push-only)
}

// walkLink handles the symlink at path (root-relative rel). handled is false
// when it is not a link any more; true when it was skipped (recorded in
// w.found) or dereferenced (content added to w.files).
func (b *Bisync) walkLink(ctx context.Context, w *linkWalk, path, rel string) (handled bool, err error) {
	dec := decideLink(b.cfg.LocalRoot, path, rel, b.links.policy)
	switch {
	case !dec.isLink:
		return false, nil
	case !dec.copy:
		w.found[rel] = skippedLink{reason: dec.reason, target: dec.raw}
		return true, nil
	case !dec.dir:
		info, serr := os.Stat(path) // the target's size and mtime
		if serr != nil {
			w.found[rel] = skippedLink{reason: LinkUnresolved, target: dec.raw}
			return true, nil
		}
		w.files = append(w.files, LocalFileInfo{Path: rel, Size: info.Size(), ModTime: info.ModTime()})
		w.deref[rel] = struct{}{}
		return true, nil
	}
	w.deref[rel] = struct{}{}
	return true, b.walkLinkedDir(ctx, w, dec.target, rel)
}

// walkLinkedDir lists the regular files of the directory tree at target as
// if it were at linkRel. A symlink inside it is never dereferenced (it is
// skipped and reported by the usual rule), and the walk does not descend
// through one, so it is finite.
func (b *Bisync) walkLinkedDir(ctx context.Context, w *linkWalk, target, linkRel string) error {
	root := b.cfg.LocalRoot
	return filepath.WalkDir(target, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("walk linked dir %s: %w", linkRel, err)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		sub, rerr := filepath.Rel(target, p)
		if rerr != nil {
			return fmt.Errorf("rel path %s: %w", p, rerr)
		}
		if sub == "." {
			return nil
		}
		rel := joinRel(linkRel, filepath.ToSlash(sub))
		if d.IsDir() {
			if pathfilter.Excluded(b.cfg.Exclude, rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if pathfilter.Excluded(b.cfg.Exclude, rel) {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			if isLink, reason, raw := classifyLink(root, p); isLink {
				w.found[rel] = skippedLink{reason: reason, target: raw}
				return nil
			}
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return fmt.Errorf("stat %s: %w", p, ierr)
		}
		w.files = append(w.files, LocalFileInfo{Path: rel, Size: info.Size(), ModTime: info.ModTime()})
		return nil
	})
}

// pushOnlyUnderLinks drops the diffs that would write through a
// dereferenced link: pulls, remote-deleted local removals, and conflicts the
// remote side would win. Pushes (local-only, local-changed, local-deleted
// and conflicts local wins) stay.
func (b *Bisync) pushOnlyUnderLinks(diffs []FileDiff) []FileDiff {
	if len(b.derefPaths) == 0 {
		return diffs
	}
	kept := diffs[:0:0]
	for _, d := range diffs {
		if hasLinkPrefix(b.derefPaths, d.Path) && !pushesOnly(d) {
			b.log.Warn("bisync leaves a Drive-side change alone: path is behind a copied symlink and is push-only",
				"op", "symlink_copy", "origin", "remote", "path", d.Path)
			continue
		}
		kept = append(kept, d)
	}
	return kept
}

// pushesOnly reports whether d only ever changes Drive.
func pushesOnly(d FileDiff) bool {
	switch d.Kind {
	case DiffLocalOnly, DiffLocalChanged, DiffLocalDeleted:
		return true
	case DiffConflict:
		// Local wins when its mtime is not older (equal mtimes included),
		// the same rule resolveConflict applies.
		return d.LocalInfo != nil && d.RemoteInfo != nil &&
			!d.RemoteInfo.ModTime.After(d.LocalInfo.ModTime)
	}
	return false
}
