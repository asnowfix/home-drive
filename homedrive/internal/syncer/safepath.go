package syncer

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/asnowfix/home-drive/homedrive/internal/rcloneclient"
)

// ErrUnsafeRemotePath is returned when a remote path cannot be mapped to a
// location strictly inside the local root (issue #161).
var ErrUnsafeRemotePath = errors.New("unsafe remote path")

// localPathFor maps a remote-relative path to its absolute local path under
// root. It is the choke point for every place a Drive-supplied path becomes
// a local write: the path must be a valid remote path (no "..", empty or
// "." component, no backslash or NUL) and the joined result must still lie
// strictly under root, so even a caller that forgot to validate cannot
// escape the sync root.
func localPathFor(root, rel string) (string, error) {
	if !rcloneclient.ValidRemotePath(rel) {
		return "", fmt.Errorf("%w: %q", ErrUnsafeRemotePath, rel)
	}
	clean := filepath.Clean(root)
	full := filepath.Join(clean, filepath.FromSlash(rel))
	r, err := filepath.Rel(clean, full)
	if err != nil || r == "." || r == ".." || filepath.IsAbs(r) ||
		strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q", ErrUnsafeRemotePath, rel)
	}
	return full, nil
}
