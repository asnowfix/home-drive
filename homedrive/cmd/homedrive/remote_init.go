package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/syncer"
)

// exitTempFail is the process exit status (sysexits EX_TEMPFAIL) used when
// remote init gave up after all backoff attempts. The systemd units list it
// in RestartPreventExitStatus= so the agent is not restarted after giving
// up (#88).
const exitTempFail = 75

// ErrRemoteInitExhausted indicates the Drive remote could not be initialised
// after every startup backoff attempt.
var ErrRemoteInitExhausted = errors.New("remote init failed after all startup attempts")

// remoteInitRetryConfig is the startup backoff for the remote init (#88):
// 8 attempts, waits 5, 10, 20, 40, 80, 160, 300s (about 10 min 15 s total).
func remoteInitRetryConfig() syncer.RetryConfig {
	return syncer.RetryConfig{
		MaxAttempts:    8,
		InitialBackoff: 5 * time.Second,
		MaxBackoff:     5 * time.Minute,
	}
}

// initWithBackoff calls construct until it succeeds, ctx is cancelled, or
// cfg.MaxAttempts is reached. It reuses syncer.Retry rather than adding a
// third backoff loop. On exhaustion the returned error wraps both
// ErrRemoteInitExhausted and the last construct error; on cancellation it
// wraps ctx's error instead.
func initWithBackoff[T any](
	ctx context.Context,
	cfg syncer.RetryConfig,
	log *slog.Logger,
	sleepFn func(context.Context, time.Duration),
	construct func(ctx context.Context) (T, error),
) (T, error) {
	var out T
	err := syncer.Retry(ctx, cfg, log, "init rclone remote", sleepFn,
		func(ctx context.Context) error {
			v, err := construct(ctx)
			if err != nil {
				return err
			}
			out = v
			return nil
		})
	if err != nil {
		var zero T
		if errors.Is(err, syncer.ErrRetriesExhausted) {
			return zero, errors.Join(ErrRemoteInitExhausted, err)
		}
		return zero, err
	}
	return out, nil
}

// exitCode maps a fatal error from the root command to a process exit
// status: 75 when remote init gave up, 1 otherwise.
func exitCode(err error) int {
	if errors.Is(err, ErrRemoteInitExhausted) {
		return exitTempFail
	}
	return 1
}
