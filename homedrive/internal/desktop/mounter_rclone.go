package desktop

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"

	// Desktop-only rclone packages (see the homedrive-rclone-import skill):
	// the CI grep restricts them to this package and cmd/homedrive/mount*.go.
	_ "github.com/rclone/rclone/cmd/mount2" // registers the "mount2" FUSE binding with mountlib
	"github.com/rclone/rclone/cmd/mountlib"
	rclonefs "github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/config/configfile"
	"github.com/rclone/rclone/vfs/vfscommon"
)

// mountType is the rclone FUSE binding used: pure Go (hanwen/go-fuse), no cgo.
const mountType = "mount2"

// ErrNoMountSupport is returned on platforms where mount2 is not built.
var ErrNoMountSupport = errors.New("desktop: FUSE mount (mount2) is not supported on this platform")

// RcloneMounter is the production Mounter: rclone vfs (cache mode full)
// served through cmd/mount2.
type RcloneMounter struct {
	// OpenFs opens the remote; nil selects rclone's fs.NewFs. Tests inject a
	// local-backend Fs here (in _test.go only).
	OpenFs func(ctx context.Context, remote string) (rclonefs.Fs, error)
}

// Mount implements Mounter. It touches no Google API itself beyond what
// opening the remote does.
func (r RcloneMounter) Mount(ctx context.Context, spec Spec) (Mount, error) {
	open := r.OpenFs
	if open == nil {
		open = rclonefs.NewFs
	}
	_, mountFn := mountlib.ResolveMountMethod(mountType)
	if mountFn == nil {
		return nil, ErrNoMountSupport
	}
	if err := prepareRclone(spec); err != nil {
		return nil, err
	}
	f, err := open(ctx, spec.Remote)
	if err != nil {
		return nil, fmt.Errorf("desktop: open remote %q: %w", spec.Remote, err)
	}
	mp := mountlib.NewMountPoint(mountFn, spec.MountPoint, f, mountOptions(spec), vfsOptions(spec))
	// Mount (not mountlib's cobra command): that command would notify
	// systemd and install its own signal handling; homedrive owns both.
	if _, err := mp.Mount(); err != nil {
		label, confined := appArmorLabel()
		return nil, withAppArmorHint(fmt.Errorf("desktop: %w", err), label, confined)
	}
	return &rcloneMount{mp: mp}, nil
}

// prepareRclone points rclone at homedrive's own token file and cache dir.
// rclone's vfs writes only below config.GetCacheDir(): vfs/<remote>/ for data
// and vfsMeta/<remote>/ for metadata (vfs/vfscache/cache.go).
func prepareRclone(spec Spec) error {
	// Loads the defaults of every registered option block (vfs, mount, main).
	// It also reads RCLONE_* environment variables, which can only affect
	// options desktop.yaml does not expose; every option it does expose is
	// overwritten from the Spec afterwards.
	if err := rclonefs.GlobalOptionsInit(); err != nil {
		return fmt.Errorf("desktop: initialise rclone options: %w", err)
	}
	configfile.Install()
	if err := config.SetConfigPath(spec.TokenFile); err != nil {
		return fmt.Errorf("desktop: set token file %q: %w", spec.TokenFile, err)
	}
	if err := config.SetCacheDir(spec.CacheDir); err != nil {
		return fmt.Errorf("desktop: set cache dir %q: %w", spec.CacheDir, err)
	}
	return nil
}

func vfsOptions(spec Spec) *vfscommon.Options {
	o := vfscommon.Opt // defaults, see prepareRclone
	o.CacheMode = vfscommon.CacheModeFull
	o.CacheMaxSize = rclonefs.SizeSuffix(spec.CacheMaxSize)
	o.CacheMinFreeSpace = rclonefs.SizeSuffix(spec.MinFreeSpace)
	o.CacheMaxAge = rclonefs.Duration(spec.CacheMaxAge)
	o.CachePollInterval = rclonefs.Duration(spec.CachePoll)
	o.PollInterval = rclonefs.Duration(spec.PollInterval)
	return &o
}

func mountOptions(spec Spec) *mountlib.Options {
	o := mountlib.Opt // defaults, see prepareRclone
	o.DeviceName = spec.Remote
	o.VolumeName = "Google Drive"
	return &o
}

// rcloneMount adapts a mountlib.MountPoint to Mount.
type rcloneMount struct{ mp *mountlib.MountPoint }

func (m *rcloneMount) Busy() bool { return m.mp.VFS.Busy() }

func (m *rcloneMount) WaitForWriters(timeout time.Duration) { m.mp.VFS.WaitForWriters(timeout) }

// Unmount calls the binding's unmount directly: MountPoint.Unmount would shut
// the VFS down even when the unmount fails with EBUSY, before the lazy
// fallback has run.
func (m *rcloneMount) Unmount() error {
	if m.mp.UnmountFn == nil {
		return nil
	}
	return m.mp.UnmountFn()
}

func (m *rcloneMount) LazyUnmount() error {
	bin, err := exec.LookPath("fusermount3")
	if err != nil {
		if bin, err = exec.LookPath("fusermount"); err != nil {
			return fmt.Errorf("desktop: lazy unmount: %w", err)
		}
	}
	out, err := exec.Command(bin, "-u", "-z", m.mp.MountPoint).CombinedOutput() //nolint:gosec // fixed binary, configured mount point
	if err != nil {
		return fmt.Errorf("desktop: %s -u -z %q: %w: %s", bin, m.mp.MountPoint, err, out)
	}
	return nil
}

func (m *rcloneMount) Close() { m.mp.VFS.Shutdown() }

func (m *rcloneMount) Done() <-chan error { return m.mp.ErrChan }
