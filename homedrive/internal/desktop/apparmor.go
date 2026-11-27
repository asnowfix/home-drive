package desktop

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// ErrAppArmorConfined marks a mount failure that is probably caused by the
// process's AppArmor confinement (issue #184), not by homedrive or fuse3.
var ErrAppArmorConfined = errors.New("desktop: process is AppArmor-confined")

const appArmorAttr = "/proc/self/attr/current"

// appArmorHintDoc is the documentation section the hint points at.
const appArmorHintDoc = "homedrive/docs/dev-environment.md (\"FUSE tests and the snap go command\")"

// parseAppArmorLabel interprets the content of /proc/self/attr/current. It
// returns the label and whether the process is confined: any non-empty label
// other than "unconfined" (e.g. "snap.go.go (complain)").
func parseAppArmorLabel(raw string) (label string, confined bool) {
	label = strings.TrimSpace(strings.Trim(raw, "\x00"))
	return label, label != "" && label != "unconfined"
}

// appArmorLabel reads the current process's label. A missing or unreadable
// attribute (non-Linux, no LSM) reports "not confined".
func appArmorLabel() (label string, confined bool) {
	b, err := os.ReadFile(appArmorAttr)
	if err != nil {
		return "", false
	}
	return parseAppArmorLabel(string(b))
}

// withAppArmorHint adds an actionable hint to a fusermount failure when the
// process carries an AppArmor label. Ubuntu's enforcing fusermount3 profile
// denies the FUSE control socket created by a snap-confined parent (e.g. the
// snap "go" running "go test"/"go run"), so fusermount3 fails with "file
// descriptor 3 is not a socket". err is kept in the chain with %w.
func withAppArmorHint(err error, label string, confined bool) error {
	if err == nil || !confined || !strings.Contains(strings.ToLower(err.Error()), "fusermount") {
		return err
	}
	return fmt.Errorf("%w: %w (label %q): the fusermount3 AppArmor profile probably refused the FUSE control socket "+
		"from this confined parent; start the process from an unconfined shell, desktop session or systemd user unit. "+
		"See %s", err, ErrAppArmorConfined, label, appArmorHintDoc)
}
