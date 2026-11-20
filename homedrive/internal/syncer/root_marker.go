package syncer

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/asnowfix/home-drive/homedrive/internal/pathfilter"
)

// readRootMarker returns the id in root's marker file, "" when it is absent
// or empty (a truncated marker counts as absent).
func readRootMarker(root string) (string, error) {
	b, err := os.ReadFile(filepath.Join(root, pathfilter.RootMarker))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return strings.TrimSpace(string(b)), err
}

// writeRootMarker durably writes id as root's marker: temp file, fsync,
// rename over the marker, fsync of the directory. A crash leaves either no
// marker or a complete one, never a truncated one. The caller records the id
// in the state DB only afterwards, so the worst a crash leaves is a marker
// with no stored id, which the guard adopts.
func writeRootMarker(root, id string) error {
	final := filepath.Join(root, pathfilter.RootMarker)
	tmp := final + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(id + "\n"); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if d, err := os.Open(root); err == nil { // best effort: not every filesystem syncs directories
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

func newRootID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
