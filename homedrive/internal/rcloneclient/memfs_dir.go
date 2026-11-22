package rcloneclient

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// SeedDir adds an explicit (possibly empty) folder at remotePath, like a
// Drive folder that outlives the files it held (issue #174).
func (m *MemFS) SeedDir(remotePath string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dirs == nil {
		m.dirs = make(map[string]struct{})
	}
	m.dirs[strings.Trim(remotePath, "/")] = struct{}{}
}

// Dirs returns the sorted explicit folders currently in the MemFS.
func (m *MemFS) Dirs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.dirs))
	for d := range m.dirs {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// isDirLocked reports whether p is a folder: seeded with SeedDir, or the
// parent of a file or seeded folder. m.mu must be held.
func (m *MemFS) isDirLocked(p string) bool {
	if _, ok := m.dirs[p]; ok {
		return true
	}
	return m.hasChildLocked(p)
}

// hasChildLocked reports whether any file or seeded folder lives under p.
// m.mu must be held.
func (m *MemFS) hasChildLocked(p string) bool {
	prefix := p + "/"
	for f := range m.files {
		if strings.HasPrefix(f, prefix) {
			return true
		}
	}
	for d := range m.dirs {
		if strings.HasPrefix(d, prefix) {
			return true
		}
	}
	return false
}

// RemoveEmptyDir removes the folder at remotePath only if nothing lives
// under it, mirroring RcloneFS.RemoveEmptyDir: the root is refused, a
// missing folder is ErrNotFound and a folder with any child ErrDirNotEmpty.
func (m *MemFS) RemoveEmptyDir(_ context.Context, remotePath string) error {
	p := strings.Trim(remotePath, "/")
	if p == "" || p == "." {
		return fmt.Errorf("remove dir: refusing the remote root")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.hasChildLocked(p) {
		return fmt.Errorf("remove dir %q: %w", p, ErrDirNotEmpty)
	}
	if _, ok := m.dirs[p]; !ok {
		return fmt.Errorf("remove dir %q: %w", p, ErrNotFound)
	}
	delete(m.dirs, p)
	return nil
}
