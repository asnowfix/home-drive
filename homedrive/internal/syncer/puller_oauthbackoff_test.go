package syncer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/rcloneclient"
)

// TestPuller_OAuthClientMissing_BacksOffPollInterval proves the narrow
// backoff issue #67 asks for: repeated consecutive ListChanges failures
// classified as ErrOAuthClientMissing grow the poll interval, and a
// single success restores it immediately.
func TestPuller_OAuthClientMissing_BacksOffPollInterval(t *testing.T) {
	localRoot := t.TempDir()
	now := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)

	remote := newMockRemoteFS()
	oauthErr := fmt.Errorf("mock changes.list: %w", rcloneclient.ErrOAuthClientMissing)
	remote.genericErrTokens["stuck-token"] = oauthErr

	store := newMockStore()
	_ = store.SetPageToken(context.Background(), "stuck-token")

	p := NewPuller(
		PullerConfig{Interval: 30 * time.Second, LocalRoot: localRoot},
		remote, store, newMockAuditLogger(), nil,
		slog.Default(), fixedClock(now),
	)

	if got := p.nextPollInterval(); got != 30*time.Second {
		t.Fatalf("initial nextPollInterval = %v, want %v", got, 30*time.Second)
	}

	wantIntervals := []time.Duration{
		30 * time.Second, // after 1st consecutive failure
		time.Minute,      // after 2nd
		2 * time.Minute,  // after 3rd
	}
	for i, want := range wantIntervals {
		err := p.PollOnce(context.Background())
		if !errors.Is(err, rcloneclient.ErrOAuthClientMissing) {
			t.Fatalf("poll %d: PollOnce error = %v, want it to wrap ErrOAuthClientMissing", i+1, err)
		}
		if got := p.nextPollInterval(); got != want {
			t.Errorf("poll %d: nextPollInterval = %v, want %v", i+1, got, want)
		}
	}

	// Stored token must still be untouched -- ErrOAuthClientMissing is
	// not a reset-worthy class (unlike ErrGone/ErrTokenRejected).
	token, _ := store.GetPageToken(context.Background())
	if token != "stuck-token" {
		t.Errorf("token = %q, want unchanged %q", token, "stuck-token")
	}

	// Once the underlying failure clears (operator fixes rclone.conf and
	// restarts, or -- for this test -- the mock stops erroring), the very
	// next successful poll must restore the base interval immediately,
	// not decay gradually.
	delete(remote.genericErrTokens, "stuck-token")
	remote.changes["stuck-token"] = Changes{Items: []Change{}, NextPageToken: "stuck-token-next"}
	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatalf("recovery PollOnce: %v", err)
	}
	if got := p.nextPollInterval(); got != 30*time.Second {
		t.Errorf("after recovery, nextPollInterval = %v, want restored %v", got, 30*time.Second)
	}
}

// TestPuller_NextPollInterval_CapsAtOAuthBackoffMax proves the backoff is
// bounded rather than growing unboundedly across a long outage.
func TestPuller_NextPollInterval_CapsAtOAuthBackoffMax(t *testing.T) {
	p := &Puller{cfg: PullerConfig{Interval: 30 * time.Second}}
	p.oauthMissingStreak = 1000 // far more than needed to reach the cap
	if got := p.nextPollInterval(); got != oauthBackoffMax {
		t.Errorf("nextPollInterval with a long streak = %v, want capped at %v", got, oauthBackoffMax)
	}
}

// TestPuller_UnrelatedError_LeavesOAuthStreakAtZero proves the backoff is
// scoped to ErrOAuthClientMissing specifically -- an unrelated ListChanges
// failure (e.g. rate limiting) must never advance it (issue #67 rules out
// a general "any auth/poll failure backs off" design).
func TestPuller_UnrelatedError_LeavesOAuthStreakAtZero(t *testing.T) {
	localRoot := t.TempDir()
	now := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)

	remote := newMockRemoteFS()
	remote.genericErrTokens["rate-limited-token"] =
		errors.New("googleapi: Error 403: User Rate Limit Exceeded, rateLimitExceeded")

	store := newMockStore()
	_ = store.SetPageToken(context.Background(), "rate-limited-token")

	p := NewPuller(
		PullerConfig{Interval: 30 * time.Second, LocalRoot: localRoot},
		remote, store, newMockAuditLogger(), nil,
		slog.Default(), fixedClock(now),
	)

	_ = p.PollOnce(context.Background())
	if p.oauthMissingStreak != 0 {
		t.Errorf("oauthMissingStreak = %d after an unrelated error, want 0", p.oauthMissingStreak)
	}
	if got := p.nextPollInterval(); got != 30*time.Second {
		t.Errorf("nextPollInterval after an unrelated error = %v, want unchanged %v", got, 30*time.Second)
	}
}

// TestPuller_Run_AppliesOAuthBackoffViaTimer is an end-to-end confirmation
// that Run() actually applies nextPollInterval's backoff through
// timer.Reset, not just that the pure function computes the right values
// (proven above). Uses a tiny base interval so the test finishes fast
// regardless of the real oauthBackoffMax cap.
func TestPuller_Run_AppliesOAuthBackoffViaTimer(t *testing.T) {
	localRoot := t.TempDir()
	now := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)

	remote := newMockRemoteFS()
	remote.genericErrTokens["stuck-token"] =
		fmt.Errorf("mock changes.list: %w", rcloneclient.ErrOAuthClientMissing)

	store := newMockStore()
	_ = store.SetPageToken(context.Background(), "stuck-token")

	p := NewPuller(
		PullerConfig{Interval: 20 * time.Millisecond, LocalRoot: localRoot},
		remote, store, newMockAuditLogger(), nil,
		slog.Default(), fixedClock(now),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()

	select {
	case err := <-done:
		if err != context.DeadlineExceeded {
			t.Errorf("Run returned %v, want context.DeadlineExceeded", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit after context deadline")
	}

	// With a 20ms base interval doubling per consecutive failure, a
	// 300ms window comfortably pushes the streak well past 1 -- the
	// point being Run() actually grew the interval (fewer,
	// increasingly-spaced polls) rather than hammering at 20ms the whole
	// time. An exact count would be timing-flaky; ">1" is not: at 20ms
	// flat the window would fit ~15 polls, but backing off after the
	// first few makes that impossible.
	if p.oauthMissingStreak <= 1 {
		t.Errorf("oauthMissingStreak = %d after a sustained OAuth failure via Run(), want > 1", p.oauthMissingStreak)
	}
}

// TestPuller_410Reset_LeavesOAuthStreakAtZero proves a token-reset
// (410/400) failure, though also an "error", does not get folded into the
// OAuth backoff streak either -- it is a different, self-healing failure
// class with its own recovery path.
func TestPuller_410Reset_LeavesOAuthStreakAtZero(t *testing.T) {
	localRoot := t.TempDir()
	now := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)

	remote := newMockRemoteFS()
	remote.startToken = "fresh-start"
	remote.goneTokens["stale-token"] = true
	remote.changes["fresh-start"] = Changes{Items: []Change{}, NextPageToken: "fresh-start-next"}

	store := newMockStore()
	_ = store.SetPageToken(context.Background(), "stale-token")

	p := NewPuller(
		PullerConfig{Interval: 30 * time.Second, LocalRoot: localRoot},
		remote, store, newMockAuditLogger(), nil,
		slog.Default(), fixedClock(now),
	)

	// Manually put the streak in a non-zero state first, to prove the
	// 410 path actively resets it rather than merely never incrementing
	// it from zero.
	p.oauthMissingStreak = 3

	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if p.oauthMissingStreak != 0 {
		t.Errorf("oauthMissingStreak = %d after a 410 reset, want 0", p.oauthMissingStreak)
	}
}
