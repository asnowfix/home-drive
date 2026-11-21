package desktop

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func always(v bool) isMountFunc { return func(string) (bool, error) { return v, nil } }

func TestCheckMountPoint_Cases(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T, dir string) string
		isMount isMountFunc
		want    error
	}{
		{
			name:    "MissingIsCreated",
			setup:   func(_ *testing.T, dir string) string { return filepath.Join(dir, "a", "b") },
			isMount: always(false),
		},
		{
			name: "EmptyExisting",
			setup: func(t *testing.T, dir string) string {
				p := filepath.Join(dir, "mp")
				mustMkdir(t, p)
				return p
			},
			isMount: always(false),
		},
		{
			name: "NonEmptyRefused",
			setup: func(t *testing.T, dir string) string {
				p := filepath.Join(dir, "mp")
				mustMkdir(t, p)
				if err := os.WriteFile(filepath.Join(p, "f"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
				return p
			},
			isMount: always(false),
			want:    ErrMountPointNotEmpty,
		},
		{
			name: "AlreadyMountedRefused",
			setup: func(t *testing.T, dir string) string {
				p := filepath.Join(dir, "mp")
				mustMkdir(t, p)
				return p
			},
			isMount: always(true),
			want:    ErrMountPointMounted,
		},
		{
			name: "NotADirectory",
			setup: func(t *testing.T, dir string) string {
				p := filepath.Join(dir, "file")
				if err := os.WriteFile(p, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				return p
			},
			isMount: always(false),
			want:    ErrMountPointNotDir,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.setup(t, t.TempDir())
			err := CheckMountPoint(p, tc.isMount)
			if tc.want == nil {
				if err != nil {
					t.Fatal(err)
				}
				if fi, serr := os.Stat(p); serr != nil || !fi.IsDir() {
					t.Fatalf("mount point not a directory after check: %v", serr)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestCheckMountPoint_IsMountError(t *testing.T) {
	p := t.TempDir()
	boom := errors.New("boom")
	err := CheckMountPoint(p, func(string) (bool, error) { return false, boom })
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
}

func TestDeviceDiffersFromParent_Cases(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("relies on /proc being a separate filesystem")
	}
	tests := []struct {
		name string
		path string
		want bool
	}{
		{"ProcIsAMount", "/proc", true},
		{"PlainTempSubdir", mustTempSubdir(t), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := deviceDiffersFromParent(tc.path)
			if err != nil || got != tc.want {
				t.Fatalf("got %v, %v; want %v", got, err, tc.want)
			}
		})
	}
	if _, err := deviceDiffersFromParent(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("missing path must error")
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
}

func mustTempSubdir(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "sub")
	mustMkdir(t, p)
	return p
}

func TestCheckPathsOutsideMount_Cases(t *testing.T) {
	root := t.TempDir()
	base := func() *Config {
		c := &Config{
			MountPoint: filepath.Join(root, "mnt"),
			TokenFile:  filepath.Join(root, "cfg", "rclone.conf"),
			StateDir:   filepath.Join(root, "state"),
			RuntimeDir: filepath.Join(root, "run"),
		}
		c.Cache.Dir = filepath.Join(root, "cache")
		return c
	}
	tests := []struct {
		name string
		edit func(c *Config)
		nas  *NASInfo
		want error
	}{
		{"AllOutside", func(*Config) {}, nil, nil},
		{"CacheInsideMount", func(c *Config) { c.Cache.Dir = filepath.Join(c.MountPoint, ".cache") }, nil, ErrPathInsideMount},
		{"TokenInsideMount", func(c *Config) { c.TokenFile = filepath.Join(c.MountPoint, "rclone.conf") }, nil, ErrPathInsideMount},
		{"StateInsideMount", func(c *Config) { c.StateDir = filepath.Join(c.MountPoint, "state") }, nil, ErrPathInsideMount},
		{"CacheInsideNASRoot", func(*Config) {}, &NASInfo{LocalRoot: root}, ErrCacheInsideNASRoot},
		{"CacheOutsideNASRoot", func(*Config) {}, &NASInfo{LocalRoot: filepath.Join(root, "nas")}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := base()
			tc.edit(c)
			if err := CheckPathsOutsideMount(c, tc.nas); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}
