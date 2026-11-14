package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/syncer"
)

func quietLog() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

func TestRemoteInitRetryConfig_Numbers(t *testing.T) {
	cfg := remoteInitRetryConfig()
	var waits []time.Duration
	b := cfg.InitialBackoff
	for i := 1; i < cfg.MaxAttempts; i++ {
		waits = append(waits, b)
		b = min(b*2, cfg.MaxBackoff)
	}
	want := []time.Duration{5, 10, 20, 40, 80, 160, 300}
	if cfg.MaxAttempts != 8 || len(waits) != len(want) {
		t.Fatalf("attempts=%d waits=%v", cfg.MaxAttempts, waits)
	}
	for i, w := range want {
		if waits[i] != w*time.Second {
			t.Errorf("wait %d = %v, want %v", i, waits[i], w*time.Second)
		}
	}
}

func TestInitWithBackoff_Outcomes(t *testing.T) {
	boom := errors.New("token rejected")
	tests := []struct {
		name       string
		failures   int // construct fails this many times, then succeeds
		wantCalls  int
		wantSleeps []time.Duration
		wantErr    bool
	}{
		{"first_try", 0, 1, nil, false},
		{"after_3_failures", 3, 4, []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second}, false},
		{"success_on_last_attempt", 7, 8, nil, false},
		{"exhausted", 100, 8, nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			var sleeps []time.Duration
			got, err := initWithBackoff(context.Background(), remoteInitRetryConfig(), quietLog(),
				func(_ context.Context, d time.Duration) { sleeps = append(sleeps, d) },
				func(context.Context) (string, error) {
					calls++
					if calls <= tc.failures {
						return "", boom
					}
					return "ok", nil
				})
			if calls != tc.wantCalls {
				t.Errorf("calls = %d, want %d", calls, tc.wantCalls)
			}
			if tc.wantErr {
				if !errors.Is(err, ErrRemoteInitExhausted) || !errors.Is(err, boom) {
					t.Fatalf("err = %v, want sentinel and cause", err)
				}
				if len(sleeps) != 7 || sleeps[6] != 300*time.Second {
					t.Errorf("sleeps = %v", sleeps)
				}
				return
			}
			if err != nil || got != "ok" {
				t.Fatalf("got %q, %v", got, err)
			}
			if tc.wantSleeps != nil && fmt.Sprint(sleeps) != fmt.Sprint(tc.wantSleeps) {
				t.Errorf("sleeps = %v, want %v", sleeps, tc.wantSleeps)
			}
		})
	}
}

func TestInitWithBackoff_CancelDuringWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	_, err := initWithBackoff(ctx, remoteInitRetryConfig(), quietLog(),
		func(context.Context, time.Duration) { cancel() },
		func(context.Context) (int, error) {
			calls++
			return 0, errors.New("down")
		})
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if errors.Is(err, ErrRemoteInitExhausted) || exitCode(err) != 1 {
		t.Errorf("cancellation must not map to exhaustion (exit %d)", exitCode(err))
	}
}

func TestInitWithBackoff_RealSleepCancelsPromptly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	start := time.Now()
	_, err := initWithBackoff(ctx, remoteInitRetryConfig(), quietLog(), syncer.ContextSleep,
		func(context.Context) (int, error) { return 0, errors.New("down") })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("took %v, want prompt exit", time.Since(start))
	}
}

func TestExitCode_Mapping(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"exhausted", ErrRemoteInitExhausted, 75},
		{"wrapped_exhausted", fmt.Errorf("build agent: %w", fmt.Errorf("init: %w", ErrRemoteInitExhausted)), 75},
		{"joined_exhausted", errors.Join(ErrRemoteInitExhausted, errors.New("x")), 75},
		{"other", errors.New("load config"), 1},
		{"canceled", context.Canceled, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := exitCode(tc.err); got != tc.want {
				t.Errorf("exitCode = %d, want %d", got, tc.want)
			}
		})
	}
}
