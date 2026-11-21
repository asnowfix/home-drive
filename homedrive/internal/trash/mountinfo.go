package trash

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// procMountinfo is the kernel's per-process mount table. It is read from
// /proc/self/mountinfo so it reflects the mount namespace the agent really
// runs in (a systemd unit with ReadWritePaths gets its own bind mounts).
const procMountinfo = "/proc/self/mountinfo"

func readProcMountinfo() ([]byte, error) {
	return os.ReadFile(procMountinfo)
}

// topdirOf returns the mount point of the mount that holds path: the longest
// mount point in the mountinfo table that is a path prefix of it. The trash
// spec's "topdir" is that mount point. A device-change walk cannot be used
// instead: several bind mounts of one device share a single st_dev, yet a
// rename between them fails with EXDEV (issue #155).
func topdirOf(mountinfo []byte, path string) (string, error) {
	best := ""
	sc := bufio.NewScanner(bytes.NewReader(mountinfo))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		// id parent major:minor root mountpoint options ... - fstype source super
		fields := strings.Fields(sc.Text())
		if len(fields) < 5 {
			continue
		}
		mp := unescapeMountinfo(fields[4])
		if !underMount(mp, path) || len(mp) <= len(best) {
			continue
		}
		best = mp
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("reading mountinfo: %w", err)
	}
	if best == "" {
		return "", fmt.Errorf("no mount point found for %s", path)
	}
	return best, nil
}

// underMount reports whether path is mp or lies below it.
func underMount(mp, path string) bool {
	if mp == "/" {
		return strings.HasPrefix(path, "/")
	}
	return path == mp || strings.HasPrefix(path, mp+"/")
}

// unescapeMountinfo decodes the octal escapes (\040 for a space, ...) the
// kernel uses in mountinfo path fields.
func unescapeMountinfo(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
