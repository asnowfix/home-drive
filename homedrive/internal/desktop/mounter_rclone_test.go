package desktop

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	// Test-only: an in-process rclone backend so the FUSE path is exercised
	// with no Google call and no network. This departs from the
	// homedrive-test-mocks "MemFS/FlakyFS via RemoteFS" rule on purpose: the
	// code under test IS the rclone vfs/mount2 integration, which has no
	// RemoteFS seam. The CI backend grep skips _test.go files.
	_ "github.com/rclone/rclone/backend/local"
	rclonefs "github.com/rclone/rclone/fs"
)

func requireFUSE(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("requires Linux FUSE")
	}
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skip("requires /dev/fuse")
	}
	if _, err := exec.LookPath("fusermount3"); err != nil {
		t.Skip("requires fusermount3")
	}
}

func TestRcloneMounter_Spec(t *testing.T) {
	spec := Spec{
		Remote: "gdrive:folder", CacheMaxSize: 7 << 30, MinFreeSpace: 5 << 30,
		CacheMaxAge: noAgeLimit, CachePoll: 10 * time.Second, PollInterval: 30 * time.Second,
	}
	v := vfsOptions(spec)
	if v.CacheMode.String() != "full" {
		t.Errorf("cache mode = %v, want full", v.CacheMode)
	}
	if int64(v.CacheMaxSize) != spec.CacheMaxSize || int64(v.CacheMinFreeSpace) != spec.MinFreeSpace ||
		time.Duration(v.CacheMaxAge) != noAgeLimit || time.Duration(v.CachePollInterval) != 10*time.Second ||
		time.Duration(v.PollInterval) != 30*time.Second {
		t.Errorf("vfs options = %+v", v)
	}
	if o := mountOptions(spec); o.DeviceName != "gdrive:folder" {
		t.Errorf("device name = %q", o.DeviceName)
	}
}

func TestRcloneMounter_OpenFsError(t *testing.T) {
	boom := errors.New("no such remote")
	r := RcloneMounter{OpenFs: func(context.Context, string) (rclonefs.Fs, error) { return nil, boom }}
	dir := t.TempDir()
	_, err := r.Mount(context.Background(), Spec{
		Remote: "x:", MountPoint: filepath.Join(dir, "mnt"), TokenFile: filepath.Join(dir, "rclone.conf"),
		CacheDir: filepath.Join(dir, "cache"),
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

// TestRcloneMounter_FUSE mounts a local directory through the real
// vfs + mount2 stack and checks the behaviours D1 relies on: a write is
// uploaded by the shutdown flush, a cached read survives cache cleaning (the
// max_age = 0 mapping), and rclone writes nothing outside the cache dir.
func TestRcloneMounter_FUSE(t *testing.T) {
	requireFUSE(t)
	if testing.Short() {
		t.Skip("takes about 6 s (vfs write-back delay)")
	}
	sandbox := t.TempDir()
	home := filepath.Join(sandbox, "home")
	for _, k := range []string{"HOME", "XDG_CACHE_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME"} {
		t.Setenv(k, home)
	}
	t.Setenv("TMPDIR", filepath.Join(sandbox, "tmp"))
	mustMkdir(t, home)
	mustMkdir(t, filepath.Join(sandbox, "tmp"))
	remote := filepath.Join(sandbox, "remote")
	mustMkdir(t, remote)
	if err := os.WriteFile(filepath.Join(remote, "existing.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := Spec{
		Remote:       remote,
		MountPoint:   filepath.Join(sandbox, "mnt"),
		TokenFile:    filepath.Join(sandbox, "cfg", "rclone.conf"),
		CacheDir:     filepath.Join(sandbox, "cache"),
		CacheMaxSize: 1 << 30,
		CacheMaxAge:  noAgeLimit,
		CachePoll:    100 * time.Millisecond,
		PollInterval: time.Second,
	}
	for _, d := range []string{spec.MountPoint, spec.CacheDir, filepath.Dir(spec.TokenFile)} {
		mustMkdir(t, d)
	}

	m, err := RcloneMounter{}.Mount(context.Background(), spec)
	if err != nil {
		// Issue #184: Ubuntu's enforcing fusermount3 AppArmor profile refuses the
		// control socket of an AppArmor-confined parent (e.g. the snap "go"
		// running "go test"), reported as "file descriptor 3 is not a socket".
		// That environment cannot mount, so skip with the cause; any other
		// failure is a real failure.
		if label, confined := appArmorLabel(); confined {
			t.Skipf("process is AppArmor-confined (label %q): fusermount3 cannot receive the FUSE socket from it. "+
				"Run the test from an unconfined shell: go test -c -o desktop.test ./homedrive/internal/desktop/ && ./desktop.test -test.run TestRcloneMounter_FUSE "+
				"(see homedrive/docs/dev-environment.md); mount error: %v", label, err)
		}
		t.Fatalf("FUSE mount failed: %v", err)
	}
	mounted := true
	defer func() {
		if mounted {
			_ = m.Unmount()
			m.Close()
		}
	}()

	got, err := os.ReadFile(filepath.Join(spec.MountPoint, "existing.txt"))
	if err != nil || string(got) != "hello" {
		t.Fatalf("read through the mount: %q, %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(spec.MountPoint, "new.txt"), []byte("written"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second) // several cache-clean polls
	cached := findFiles(t, filepath.Join(spec.CacheDir, "vfs"))
	if len(cached) == 0 {
		t.Error("cached files were evicted by cache cleaning although max_age is unlimited")
	}

	res, err := Shutdown(m, NopNotifier{}, 20*time.Second, quietLog)
	mounted = false
	if err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if res.FlushTimedOut {
		t.Error("flush timed out")
	}
	up, err := os.ReadFile(filepath.Join(remote, "new.txt"))
	if err != nil || string(up) != "written" {
		t.Fatalf("write was not uploaded before unmount: %q, %v", up, err)
	}

	assertOnlyUnder(t, spec.CacheDir, "vfs", "vfsMeta")
	if left := findFiles(t, home); len(left) != 0 {
		t.Errorf("rclone wrote outside the cache dir (HOME/XDG): %v", left)
	}
}

func findFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func assertOnlyUnder(t *testing.T, dir string, allowed ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		ok := false
		for _, a := range allowed {
			ok = ok || e.Name() == a
		}
		if !ok {
			t.Errorf("unexpected entry %q at the top of the cache dir", e.Name())
		}
	}
}
