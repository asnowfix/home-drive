package desktop

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Refusals caused by the NAS-mode configuration on the same host.
var (
	ErrOverlapsNAS      = errors.New("desktop: mount point overlaps the NAS-mode local_root")
	ErrTokenIsNASConfig = errors.New("desktop: token file is the NAS-mode rclone_config")
)

// NASInfo is the part of the NAS-mode config.yaml desktop mode cares about.
type NASInfo struct {
	LocalRoot    string `yaml:"local_root"`
	RcloneConfig string `yaml:"rclone_config"`
}

// LoadNASInfo reads the NAS-mode config at path. A missing file is not an
// error: it returns (nil, nil) and the NAS checks are skipped. An unreadable
// or unparsable file is an error, because the safety checks cannot be done.
func LoadNASInfo(path string) (*NASInfo, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil //nolint:nilnil // documented: no NAS config, nothing to check
	}
	if err != nil {
		return nil, fmt.Errorf("desktop: read NAS config %q: %w", path, err)
	}
	var n NASInfo
	if err := yaml.Unmarshal(data, &n); err != nil {
		return nil, fmt.Errorf("desktop: parse NAS config %q: %w", path, err)
	}
	return &n, nil
}

// CheckNASOverlap refuses a mount point that is, contains, or sits inside the
// NAS-mode local_root (two writers on one tree would sync it in circles).
func CheckNASOverlap(mountPoint string, nas *NASInfo) error {
	if nas == nil || strings.TrimSpace(nas.LocalRoot) == "" {
		return nil
	}
	mp, root := canonical(mountPoint), canonical(nas.LocalRoot)
	if within(mp, root) || within(root, mp) {
		return fmt.Errorf("%w: mount point %q and local_root %q are, contain or sit inside one another",
			ErrOverlapsNAS, mountPoint, nas.LocalRoot)
	}
	return nil
}

// CheckTokenNotNAS refuses a token file that is the NAS-mode rclone_config.
// This covers an older NAS-mode binary that takes no flock. An unset
// rclone_config means rclone's own default location (rcloneDefault).
func CheckTokenNotNAS(tokenFile string, nas *NASInfo, rcloneDefault string) error {
	if nas == nil {
		return nil
	}
	other := nas.RcloneConfig
	if strings.TrimSpace(other) == "" {
		other = rcloneDefault
	}
	if canonical(tokenFile) == canonical(other) {
		return fmt.Errorf("%w: %q; the token file must be private to one process, name another token_file in desktop.yaml",
			ErrTokenIsNASConfig, tokenFile)
	}
	return nil
}

// canonical returns an absolute, cleaned path with symlinks resolved as far
// as the path exists (the mount point may not exist yet).
func canonical(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	rest := ""
	cur := abs
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// within reports whether child equals parent or is inside it.
func within(child, parent string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
