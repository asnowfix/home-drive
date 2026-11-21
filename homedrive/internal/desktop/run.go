package desktop

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/tokenlock"
)

// Spec is the resolved input of a mount: every path absolute, every size and
// interval final.
type Spec struct {
	Remote       string
	MountPoint   string
	TokenFile    string
	CacheDir     string
	CacheMaxSize int64
	CacheMaxAge  time.Duration
	MinFreeSpace int64
	CachePoll    time.Duration
	PollInterval time.Duration
}

// Mounter mounts a Spec. The production implementation is RcloneMounter; tests
// inject a fake.
type Mounter interface {
	Mount(ctx context.Context, spec Spec) (Mount, error)
}

// Deps are the injectable collaborators of Run.
type Deps struct {
	Mounter  Mounter
	Notifier Notifier
	Env      Env
	Log      *slog.Logger
	// FilesystemSize, IsMount and FilesystemID override the real probes in
	// tests; nil selects the real ones.
	FilesystemSize func(path string) (int64, error)
	IsMount        func(path string) (bool, error)
	FilesystemID   func(path string) (string, error)
}

func (d *Deps) defaults() {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.Notifier == nil {
		d.Notifier = NopNotifier{}
	}
	if d.FilesystemSize == nil {
		d.FilesystemSize = FilesystemSize
	}
}

// Run performs the start-up checks, mounts, signals readiness and serves
// until ctx is cancelled (SIGTERM/SIGINT in production) or the mount ends,
// then flushes and unmounts. Any failed start-up check returns an error
// (the caller exits non-zero) before anything is mounted.
func Run(ctx context.Context, cfg *Config, deps Deps) error {
	deps.defaults()
	log := deps.Log

	release, err := startupChecks(cfg, &deps)
	if err != nil {
		log.Error("start-up check failed, refusing to start", "op", "startup", "error", err)
		return err
	}
	defer release()

	size, err := deps.FilesystemSize(cfg.Cache.Dir)
	if err != nil {
		return fmt.Errorf("desktop: size of cache filesystem: %w", err)
	}
	spec := Spec{
		Remote:       cfg.Remote,
		MountPoint:   cfg.MountPoint,
		TokenFile:    cfg.TokenFile,
		CacheDir:     cfg.Cache.Dir,
		CacheMaxSize: cfg.Cache.EffectiveMaxSize(size),
		CacheMaxAge:  cfg.Cache.EffectiveMaxAge(),
		MinFreeSpace: int64(cfg.Cache.MinFreeSpace),
		CachePoll:    cfg.Cache.PollInterval.Duration,
		PollInterval: cfg.PollInterval.Duration,
	}
	m, err := deps.Mounter.Mount(ctx, spec)
	if err != nil {
		return fmt.Errorf("desktop: mount %q on %q: %w", spec.Remote, spec.MountPoint, err)
	}
	log.Info("mounted", "op", "mount", "remote", spec.Remote, "path", spec.MountPoint,
		"cache_dir", spec.CacheDir, "cache_max_bytes", spec.CacheMaxSize)
	if err := deps.Notifier.Ready(); err != nil {
		log.Warn("sd_notify READY failed", "error", err)
	}
	return serve(ctx, m, deps, cfg.Shutdown.FlushTimeout.Duration)
}

// serve blocks until shutdown is requested or the mount ends on its own.
func serve(ctx context.Context, m Mount, deps Deps, flushTimeout time.Duration) error {
	log := deps.Log
	select {
	case <-ctx.Done():
		log.Info("shutdown requested, flushing then unmounting", "op", "shutdown")
		res, err := Shutdown(m, deps.Notifier, flushTimeout, log)
		if err != nil {
			return err
		}
		log.Info("unmounted", "op", "shutdown", "lazy", res.Lazy, "flush_timed_out", res.FlushTimedOut)
		return nil
	case err := <-m.Done():
		// Unmounted from outside (fusermount3 -u) or the server died.
		_ = deps.Notifier.Stopping()
		// Same promise as the SIGTERM path: queued uploads are flushed
		// (bounded) before the cache is shut down, not dropped.
		m.WaitForWriters(flushTimeout)
		m.Close()
		if err != nil {
			return fmt.Errorf("desktop: mount ended: %w", err)
		}
		log.Warn("mount ended without a shutdown request (unmounted externally?)", "op", "mount")
		return ErrMountEnded
	}
}

// ErrMountEnded is returned when the filesystem was unmounted from outside
// while homedrive was running; the process exits non-zero so a unit with
// Restart=on-failure brings the mount back.
var ErrMountEnded = errors.New("desktop: mount ended without a shutdown request")

// startupChecks runs every check of design sections 8.8 and 10 and returns a
// release function for the token-file lock, held for the process lifetime.
func startupChecks(cfg *Config, deps *Deps) (func(), error) {
	log := deps.Log
	nas, err := LoadNASInfo(cfg.NASConfig)
	if err != nil {
		return nil, err
	}
	if err := CheckNASOverlap(cfg.MountPoint, nas); err != nil {
		return nil, err
	}
	if err := CheckPathsOutsideMount(cfg, nas); err != nil {
		return nil, err
	}
	if err := CheckTokenNotNAS(cfg.TokenFile, nas, deps.Env.RcloneDefaultConfig()); err != nil {
		return nil, err
	}
	lock, err := tokenlock.Acquire(cfg.TokenFile)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = lock.Release()
		}
	}()
	if err := CheckMountPoint(cfg.MountPoint, deps.IsMount); err != nil {
		return nil, err
	}
	if err := CheckCacheDir(cfg.Cache.Dir, deps.FilesystemID, nil); err != nil {
		return nil, err
	}
	for _, d := range []string{cfg.StateDir, cfg.RuntimeDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, fmt.Errorf("desktop: create %q: %w", d, err)
		}
	}
	log.Info("start-up checks passed", "op", "startup", "nas_config_found", nas != nil,
		"token_file", cfg.TokenFile, "path", cfg.MountPoint)
	ok = true
	return func() { _ = lock.Release() }, nil
}
