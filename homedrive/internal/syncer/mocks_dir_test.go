package syncer

import (
	"context"
	"fmt"
	"strings"

	"github.com/asnowfix/home-drive/homedrive/internal/rcloneclient"
)

// RemoveEmptyDir models RcloneFS.RemoveEmptyDir: any file or folder under
// path refuses with ErrDirNotEmpty, a missing folder is ErrRemoteNotFound.
func (m *mockRemoteFS) RemoveEmptyDir(_ context.Context, path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removedDirs = append(m.removedDirs, path)
	if m.rmDirErr != nil {
		if err := m.rmDirErr(path); err != nil {
			return err
		}
	}
	for p := range m.files {
		if strings.HasPrefix(p, path+"/") {
			return fmt.Errorf("mock rmdir %s: %w", path, rcloneclient.ErrDirNotEmpty)
		}
	}
	for d := range m.dirs {
		if strings.HasPrefix(d, path+"/") {
			return fmt.Errorf("mock rmdir %s: %w", path, rcloneclient.ErrDirNotEmpty)
		}
	}
	if !m.dirs[path] {
		return fmt.Errorf("mock rmdir %s: %w", path, ErrRemoteNotFound)
	}
	delete(m.dirs, path)
	return nil
}

// seedDir adds an explicit remote folder.
func (m *mockRemoteFS) seedDir(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dirs == nil {
		m.dirs = make(map[string]bool)
	}
	m.dirs[path] = true
}

// hasDir reports whether the explicit remote folder path still exists.
func (m *mockRemoteFS) hasDir(path string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dirs[path]
}

// getRemovedDirs returns the RemoveEmptyDir calls made so far.
func (m *mockRemoteFS) getRemovedDirs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.removedDirs...)
}
