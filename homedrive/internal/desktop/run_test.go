package desktop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/tokenlock"
)

// runFixture builds a Config rooted in a temp dir (nothing outside it).
func runFixture(t *testing.T) (*Config, Env) {
	t.Helper()
	root := t.TempDir()
	env := Env{Getenv: func(string) string { return "" }, Home: root}
	cfg := &Config{
		MountPoint: filepath.Join(root, "mnt"),
		TokenFile:  filepath.Join(root, "cfg", "rclone.conf"),
		NASConfig:  filepath.Join(root, "cfg", "config.yaml"),
		StateDir:   filepath.Join(root, "state"),
		RuntimeDir: filepath.Join(root, "run"),
		Cache:      CacheCfg{Dir: filepath.Join(root, "cache"), MaxSize: 1 << 30},
	}
	cfg.ApplyDefaults(env)
	cfg.Shutdown.FlushTimeout.Duration = 50 * time.Millisecond
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	return cfg, env
}

func notMounted(string) (bool, error) { return false, nil }

func newDeps(env Env, m *fakeMount, n Notifier) (Deps, *fakeMounter) {
	fm := &fakeMounter{m: m}
	return Deps{Mounter: fm, Notifier: n, Env: env, Log: quietLog, IsMount: notMounted}, fm
}

func TestRun_ShutdownOnContextCancel(t *testing.T) {
	cfg, env := runFixture(t)
	f := newFakeMount()
	f.busy = true
	n := &fakeNotifier{}
	deps, fm := newDeps(env, f, n)

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- Run(ctx, cfg, deps) }()

	deadline := time.After(5 * time.Second)
	for len(n.Events()) == 0 {
		select {
		case <-deadline:
			t.Fatal("READY never sent")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	if err := <-errc; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := n.Events(); !reflect.DeepEqual(got, []string{"ready", "stopping"}) {
		t.Errorf("events = %v", got)
	}
	if got := f.Calls(); !reflect.DeepEqual(got, []string{"wait", "unmount", "close"}) {
		t.Errorf("calls = %v (uploads must be flushed before unmount)", got)
	}
	if fm.spec.CacheMaxSize != 1<<30 || fm.spec.CacheMaxAge != noAgeLimit || fm.spec.Remote != DefaultRemote {
		t.Errorf("spec = %+v", fm.spec)
	}
	// The lock is released on exit.
	l, err := tokenlock.Acquire(cfg.TokenFile)
	if err != nil {
		t.Fatalf("token lock not released: %v", err)
	}
	_ = l.Release()
}

func TestRun_MountEndedExternally(t *testing.T) {
	cfg, env := runFixture(t)
	f := newFakeMount()
	f.done <- nil
	n := &fakeNotifier{}
	deps, _ := newDeps(env, f, n)
	err := Run(context.Background(), cfg, deps)
	if !errors.Is(err, ErrMountEnded) {
		t.Fatalf("err = %v, want ErrMountEnded", err)
	}
	if got := f.Calls(); !reflect.DeepEqual(got, []string{"close"}) {
		t.Errorf("calls = %v", got)
	}
}

func TestRun_ServerError(t *testing.T) {
	cfg, env := runFixture(t)
	f := newFakeMount()
	boom := errors.New("fuse died")
	f.done <- boom
	deps, _ := newDeps(env, f, nil)
	if err := Run(context.Background(), cfg, deps); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestRun_MountFailure(t *testing.T) {
	cfg, env := runFixture(t)
	deps, fm := newDeps(env, newFakeMount(), nil)
	boom := errors.New("no fuse")
	fm.err = boom
	if err := Run(context.Background(), cfg, deps); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestRun_StartupChecksRefuse(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, cfg *Config, deps *Deps)
		want  error
	}{
		{
			name: "MountPointNotEmpty",
			setup: func(t *testing.T, cfg *Config, _ *Deps) {
				mustMkdir(t, cfg.MountPoint)
				if err := os.WriteFile(filepath.Join(cfg.MountPoint, "f"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			},
			want: ErrMountPointNotEmpty,
		},
		{
			name:  "MountPointAlreadyMounted",
			setup: func(t *testing.T, cfg *Config, d *Deps) { mustMkdir(t, cfg.MountPoint); d.IsMount = always(true) },
			want:  ErrMountPointMounted,
		},
		{
			name: "MountPointInsideNASRoot",
			setup: func(t *testing.T, cfg *Config, _ *Deps) {
				writeNAS(t, cfg, "local_root: "+filepath.Dir(cfg.MountPoint)+"\n")
			},
			want: ErrOverlapsNAS,
		},
		{
			name: "TokenIsNASRcloneConfig",
			setup: func(t *testing.T, cfg *Config, _ *Deps) {
				writeNAS(t, cfg, "local_root: /elsewhere\nrclone_config: "+cfg.TokenFile+"\n")
			},
			want: ErrTokenIsNASConfig,
		},
		{
			name: "TokenLockHeld",
			setup: func(t *testing.T, cfg *Config, _ *Deps) {
				l, err := tokenlock.Acquire(cfg.TokenFile)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = l.Release() })
			},
			want: tokenlock.ErrHeld,
		},
		{
			name: "CacheOnAnotherFilesystem",
			setup: func(t *testing.T, cfg *Config, d *Deps) {
				if err := CheckCacheDir(cfg.Cache.Dir, fixedFS("A"), nil); err != nil {
					t.Fatal(err)
				}
				d.FilesystemID = fixedFS("B")
			},
			want: ErrCacheFilesystemChanged,
		},
		{
			name: "UnparsableNASConfig",
			setup: func(t *testing.T, cfg *Config, _ *Deps) {
				writeNAS(t, cfg, "local_root: [\n")
			},
			want: nil, // any error, checked below
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, env := runFixture(t)
			f := newFakeMount()
			deps, fm := newDeps(env, f, nil)
			tc.setup(t, cfg, &deps)
			err := Run(context.Background(), cfg, deps)
			if err == nil {
				t.Fatal("expected the start-up check to refuse")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if fm.spec != (Spec{}) {
				t.Error("nothing may be mounted after a failed check")
			}
		})
	}
}

func TestRun_FilesystemSizeError(t *testing.T) {
	cfg, env := runFixture(t)
	deps, _ := newDeps(env, newFakeMount(), nil)
	boom := errors.New("statfs")
	deps.FilesystemSize = func(string) (int64, error) { return 0, boom }
	if err := Run(context.Background(), cfg, deps); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func writeNAS(t *testing.T, cfg *Config, body string) {
	t.Helper()
	mustMkdir(t, filepath.Dir(cfg.NASConfig))
	if err := os.WriteFile(cfg.NASConfig, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
