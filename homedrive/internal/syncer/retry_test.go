package syncer

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestRetryFunc_SucceedsFirstAttempt(t *testing.T) {
	cfg := RetryConfig{MaxAttempts: 3, InitialBackoff: time.Second, MaxBackoff: time.Minute}
	logger := newDiscardLogger()

	calls := 0
	err := retryFunc(context.Background(), cfg, logger, "test", noSleep,
		func(_ context.Context) error {
			calls++
			return nil
		},
	)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}
}

func TestRetryFunc_SucceedsAfterRetries(t *testing.T) {
	cfg := RetryConfig{MaxAttempts: 5, InitialBackoff: time.Second, MaxBackoff: time.Minute}
	logger := newDiscardLogger()

	calls := 0
	err := retryFunc(context.Background(), cfg, logger, "test", noSleep,
		func(_ context.Context) error {
			calls++
			if calls < 3 {
				return fmt.Errorf("try again")
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	if calls != 3 {
		t.Errorf("expected 3 calls, got %d", calls)
	}
}

func TestRetryFunc_AllAttemptsExhausted(t *testing.T) {
	cfg := RetryConfig{MaxAttempts: 3, InitialBackoff: time.Second, MaxBackoff: time.Minute}
	logger := newDiscardLogger()

	err := retryFunc(context.Background(), cfg, logger, "test", noSleep,
		func(_ context.Context) error {
			return fmt.Errorf("permanent failure")
		},
	)
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if !errors.Is(err, ErrRetriesExhausted) {
		t.Errorf("expected ErrRetriesExhausted in error chain, got %v", err)
	}
}

func TestRetryFunc_ContextCancelled(t *testing.T) {
	cfg := RetryConfig{MaxAttempts: 5, InitialBackoff: time.Second, MaxBackoff: time.Minute}
	logger := newDiscardLogger()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	err := retryFunc(ctx, cfg, logger, "test", noSleep,
		func(_ context.Context) error {
			return fmt.Errorf("should not be called")
		},
	)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestNextBackoff(t *testing.T) {
	tests := []struct {
		name    string
		current time.Duration
		max     time.Duration
		want    time.Duration
	}{
		{
			name:    "doubles",
			current: 5 * time.Second,
			max:     5 * time.Minute,
			want:    10 * time.Second,
		},
		{
			name:    "caps_at_max",
			current: 3 * time.Minute,
			max:     5 * time.Minute,
			want:    5 * time.Minute,
		},
		{
			name:    "already_at_max",
			current: 5 * time.Minute,
			max:     5 * time.Minute,
			want:    5 * time.Minute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nextBackoff(tt.current, tt.max)
			if got != tt.want {
				t.Errorf("nextBackoff(%v, %v) = %v, want %v",
					tt.current, tt.max, got, tt.want)
			}
		})
	}
}
