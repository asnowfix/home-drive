//go:build !linux

package desktop

import (
	"fmt"
)

// FilesystemID returns the device number of the filesystem holding path.
// (On Linux the statfs fsid is used, which is stable across reboots.)
func FilesystemID(path string) (string, error) {
	dev, err := statDev(path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("dev:%d", dev), nil
}
