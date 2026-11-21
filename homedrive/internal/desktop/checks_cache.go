package desktop

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Cache-dir marker failures (analogue of NAS mode's root guard, #170).
var (
	ErrCacheFilesystemChanged = errors.New("desktop: cache directory is on a different filesystem than when it was created")
	ErrCacheUnmarked          = errors.New("desktop: cache directory is not empty and has no homedrive marker")
)

// cacheMarkerName is the marker file kept at the top of the cache directory.
// rclone's vfs only writes under vfs/ and vfsMeta/, never at the top.
const cacheMarkerName = ".homedrive-cache.json"

type cacheMarker struct {
	FilesystemID string    `json:"filesystem_id"`
	CreatedAt    time.Time `json:"created_at"`
	Dir          string    `json:"dir"`
}

// fsIDFunc returns an opaque identifier of the filesystem holding path.
type fsIDFunc func(path string) (string, error)

// CheckCacheDir creates the cache directory (mode 0700) if needed and
// verifies the marker recording the filesystem it was created on. A directory
// that has moved to another filesystem (disk unmounted, swapped) is refused:
// otherwise it would silently refill a fresh cache on the wrong disk, or
// re-upload a stale dirty cache. fsID may be nil (the default is
// FilesystemID).
func CheckCacheDir(dir string, fsID fsIDFunc, now func() time.Time) error {
	if fsID == nil {
		fsID = FilesystemID
	}
	if now == nil {
		now = time.Now
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("desktop: create cache dir %q: %w", dir, err)
	}
	id, err := fsID(dir)
	if err != nil {
		return fmt.Errorf("desktop: identify filesystem of cache dir %q: %w", dir, err)
	}
	markerPath := filepath.Join(dir, cacheMarkerName)
	data, err := os.ReadFile(markerPath) //nolint:gosec // path derives from the operator-configured cache dir
	switch {
	case errors.Is(err, os.ErrNotExist):
		return writeCacheMarker(dir, markerPath, id, now())
	case err != nil:
		return fmt.Errorf("desktop: read cache marker %q: %w", markerPath, err)
	}
	var m cacheMarker
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("desktop: parse cache marker %q: %w", markerPath, err)
	}
	if id != "" && m.FilesystemID != "" && m.FilesystemID != id {
		return fmt.Errorf("%w: %q was created on filesystem %q, now on %q (disk unmounted or swapped?); "+
			"fix the mount or remove %q to adopt the new location",
			ErrCacheFilesystemChanged, dir, m.FilesystemID, id, markerPath)
	}
	return nil
}

func writeCacheMarker(dir, markerPath, id string, now time.Time) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("desktop: list cache dir %q: %w", dir, err)
	}
	if len(entries) > 0 {
		return fmt.Errorf("%w: %q holds %d entries", ErrCacheUnmarked, dir, len(entries))
	}
	data, err := json.Marshal(cacheMarker{FilesystemID: id, CreatedAt: now.UTC(), Dir: dir})
	if err != nil {
		return fmt.Errorf("desktop: encode cache marker: %w", err)
	}
	if err := os.WriteFile(markerPath, data, 0o600); err != nil {
		return fmt.Errorf("desktop: write cache marker %q: %w", markerPath, err)
	}
	return nil
}
