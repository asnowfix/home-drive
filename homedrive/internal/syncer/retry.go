package syncer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// RetryConfig controls exponential backoff behavior.
type RetryConfig struct {
	MaxAttempts    int           // Maximum number of attempts (default 5).
	InitialBackoff time.Duration // Initial backoff duration (default 5s).
	MaxBackoff     time.Duration // Maximum backoff cap (default 5m).
}

// DefaultRetryConfig returns the PLAN.md default retry configuration.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts:    5,
		InitialBackoff: 5 * time.Second,
		MaxBackoff:     5 * time.Minute,
	}
}

// ErrRetriesExhausted indicates all retry attempts failed.
var ErrRetriesExhausted = errors.New("all retry attempts exhausted")

// retryFunc executes fn with exponential backoff. It uses the provided
// sleep function so tests can inject a no-op or mock. Returns the last
// error wrapped with ErrRetriesExhausted if all attempts fail.
func retryFunc(
	ctx context.Context,
	cfg RetryConfig,
	logger *slog.Logger,
	desc string,
	sleepFn func(context.Context, time.Duration),
	fn func(ctx context.Context) error,
) error {
	var lastErr error
	backoff := cfg.InitialBackoff

	for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("retry cancelled: %w", err)
		}

		lastErr = fn(ctx)
		if lastErr == nil {
			return nil
		}

		logger.Warn("attempt failed",
			"op", desc,
			"attempt", attempt,
			"max_attempts", cfg.MaxAttempts,
			"error", lastErr.Error(),
			"next_backoff_ms", backoff.Milliseconds(),
		)

		if attempt < cfg.MaxAttempts {
			sleepFn(ctx, backoff)
			backoff = nextBackoff(backoff, cfg.MaxBackoff)
		}
	}

	return fmt.Errorf("%s: %w: %w", desc, ErrRetriesExhausted, lastErr)
}

// nextBackoff doubles the backoff, capping at maxBackoff.
func nextBackoff(current, max time.Duration) time.Duration {
	next := current * 2
	if next > max {
		return max
	}
	return next
}
