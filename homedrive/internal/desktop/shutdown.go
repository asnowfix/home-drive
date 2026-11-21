package desktop

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"syscall"
	"time"
)

// Mount is a live mount as the lifecycle code sees it. The production
// implementation wraps rclone's mountlib.MountPoint (mounter_rclone.go); tests
// inject a fake, so shutdown sequencing is verified without FUSE.
type Mount interface {
	// Busy reports whether files are open or waiting to be uploaded.
	Busy() bool
	// WaitForWriters blocks until no file is open or dirty, or until
	// timeout has elapsed.
	WaitForWriters(timeout time.Duration)
	// Unmount unmounts the filesystem. It fails with EBUSY while a process
	// still holds a file open.
	Unmount() error
	// LazyUnmount detaches the mount immediately (fusermount3 -u -z); the
	// kernel finishes the unmount when the last user goes away.
	LazyUnmount() error
	// Close stops the VFS background work. Call it after unmounting.
	Close()
	// Done receives once when the FUSE server loop has ended.
	Done() <-chan error
}

// shutdownGrace bounds the wait for the FUSE server loop to end after an
// unmount (the unit's TimeoutStopSec leaves room for it).
const shutdownGrace = 5 * time.Second

// ShutdownResult describes how a shutdown went.
type ShutdownResult struct {
	// FlushTimedOut is true when uploads were still pending after the
	// flush timeout; the dirty items stay in the cache and rclone re-queues
	// them at the next start.
	FlushTimedOut bool
	// Lazy is true when the unmount had to fall back to a lazy unmount.
	Lazy bool
}

// Shutdown flushes then unmounts: it announces STOPPING, waits up to
// flushTimeout for queued and in-progress uploads, unmounts, falls back to a
// lazy unmount on EBUSY, and finally stops the VFS. Dirty items that were not
// uploaded stay in the cache.
func Shutdown(m Mount, n Notifier, flushTimeout time.Duration, log *slog.Logger) (ShutdownResult, error) {
	var res ShutdownResult
	if err := n.Stopping(); err != nil {
		log.Warn("sd_notify STOPPING failed", "error", err)
	}
	defer m.Close()

	res.FlushTimedOut = !flush(m, flushTimeout)
	if res.FlushTimedOut {
		log.Warn("uploads still pending at flush timeout; dirty items stay in the cache and re-upload at next start",
			"op", "shutdown", "duration_ms", flushTimeout.Milliseconds())
	}

	err := m.Unmount()
	if err != nil && isBusy(err) {
		log.Warn("mount is busy, falling back to lazy unmount", "op", "shutdown", "error", err)
		res.Lazy = true
		err = m.LazyUnmount()
	}
	if err != nil {
		return res, fmt.Errorf("desktop: unmount: %w", err)
	}
	waitDone(m, log)
	return res, nil
}

// flush waits for pending uploads and reports whether they finished within
// timeout. The wait runs in a goroutine so the bound holds even if the
// underlying wait misbehaves; the goroutine ends when WaitForWriters returns
// or the process exits.
func flush(m Mount, timeout time.Duration) bool {
	idle := make(chan struct{})
	go func() {
		m.WaitForWriters(timeout)
		close(idle)
	}()
	timer := time.NewTimer(timeout + time.Second)
	defer timer.Stop()
	select {
	case <-idle:
	case <-timer.C:
	}
	return !m.Busy()
}

func waitDone(m Mount, log *slog.Logger) {
	timer := time.NewTimer(shutdownGrace)
	defer timer.Stop()
	select {
	case err := <-m.Done():
		if err != nil {
			log.Warn("FUSE server ended with an error", "op", "shutdown", "error", err)
		}
	case <-timer.C:
		log.Warn("FUSE server did not end after unmount", "op", "shutdown", "duration_ms", shutdownGrace.Milliseconds())
	}
}

// isBusy reports whether an unmount error means "a file is still open".
// go-fuse's Server.Unmount returns the fusermount stderr as a plain string
// error (no typed errno), so besides errors.Is(err, EBUSY) the message is
// matched on "busy" ("Device or resource busy").
func isBusy(err error) bool {
	return errors.Is(err, syscall.EBUSY) || strings.Contains(strings.ToLower(err.Error()), "busy")
}
