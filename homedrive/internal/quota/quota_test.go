package quota

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"
)

func TestMonitor_NormalQuota(t *testing.T) {
	remote := &mockRemoteFS{quota: QuotaInfo{Used: 50, Total: 100}}
	pub := &mockPublisher{}
	push := &mockPushController{}
	mon := newTestMonitor(t, remote, pub, push, false)

	mon.Poll(context.Background())

	snap := mon.State()
	if snap.State != StateNormal {
		t.Errorf("expected state %q, got %q", StateNormal, snap.State)
	}
	if snap.UsedPercent != 50 {
		t.Errorf("expected used_percent 50, got %v", snap.UsedPercent)
	}
	if len(pub.Events()) != 0 {
		t.Errorf("expected no events, got %d", len(pub.Events()))
	}
	if push.IsPaused() {
		t.Error("push should not be paused at 50% quota")
	}
}

func TestMonitor_WarningEmitted(t *testing.T) {
	remote := &mockRemoteFS{quota: QuotaInfo{Used: 91, Total: 100}}
	pub := &mockPublisher{}
	push := &mockPushController{}
	mon := newTestMonitor(t, remote, pub, push, false)

	mon.Poll(context.Background())

	snap := mon.State()
	if snap.State != StateWarned {
		t.Errorf("expected state %q, got %q", StateWarned, snap.State)
	}
	events := pub.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Payload["type"] != "quota.warning" {
		t.Errorf("expected event type quota.warning, got %v", events[0].Payload["type"])
	}
	if push.IsPaused() {
		t.Error("push should not be paused at 91% (warned, not blocked)")
	}
}

func TestMonitor_QuotaExhausted(t *testing.T) {
	remote := &mockRemoteFS{quota: QuotaInfo{Used: 99, Total: 100}}
	pub := &mockPublisher{}
	push := &mockPushController{}
	mon := newTestMonitor(t, remote, pub, push, false)

	mon.Poll(context.Background())

	snap := mon.State()
	if snap.State != StateBlocked {
		t.Errorf("expected state %q, got %q", StateBlocked, snap.State)
	}
	events := pub.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Payload["type"] != "quota.exhausted" {
		t.Errorf("expected event type quota.exhausted, got %v", events[0].Payload["type"])
	}
	if !push.IsPaused() {
		t.Error("push should be paused at 99% quota")
	}
}

func TestMonitor_HysteresisResume(t *testing.T) {
	remote := &mockRemoteFS{quota: QuotaInfo{Used: 99, Total: 100}}
	pub := &mockPublisher{}
	push := &mockPushController{}
	mon := newTestMonitor(t, remote, pub, push, false)

	// First poll: enter blocked state.
	mon.Poll(context.Background())
	if mon.State().State != StateBlocked {
		t.Fatalf("expected blocked, got %s", mon.State().State)
	}
	if !push.IsPaused() {
		t.Fatal("expected push paused after 99%")
	}

	// Drop to 93% which is below hysteresis (94%).
	pub.Reset()
	remote.SetQuota(93, 100)
	mon.Poll(context.Background())

	snap := mon.State()
	if snap.State != StateWarned {
		t.Errorf("expected state %q after drop to 93%%, got %q", StateWarned, snap.State)
	}
	if push.IsPaused() {
		t.Error("push should have been resumed after dropping below hysteresis")
	}
	events := pub.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 event on resume, got %d", len(events))
	}
	if events[0].Payload["type"] != "quota.warning" {
		t.Errorf("expected quota.warning on resume, got %v", events[0].Payload["type"])
	}
}

func TestMonitor_HysteresisStaysBlocked(t *testing.T) {
	remote := &mockRemoteFS{quota: QuotaInfo{Used: 99, Total: 100}}
	pub := &mockPublisher{}
	push := &mockPushController{}
	mon := newTestMonitor(t, remote, pub, push, false)

	// Enter blocked state.
	mon.Poll(context.Background())
	if mon.State().State != StateBlocked {
		t.Fatalf("expected blocked, got %s", mon.State().State)
	}
	pausesBefore := push.PauseCount()

	// Drop to 95% which is above hysteresis (94%). Should stay blocked.
	pub.Reset()
	remote.SetQuota(95, 100)
	mon.Poll(context.Background())

	snap := mon.State()
	if snap.State != StateBlocked {
		t.Errorf("expected state %q at 95%% (above hysteresis), got %q", StateBlocked, snap.State)
	}
	if push.PauseCount() != pausesBefore {
		t.Error("should not have called PausePush again (no transition)")
	}
	if len(pub.Events()) != 0 {
		t.Errorf("expected no events (no transition), got %d", len(pub.Events()))
	}
}

func TestMonitor_DuplicatePollNoReEmit(t *testing.T) {
	remote := &mockRemoteFS{quota: QuotaInfo{Used: 91, Total: 100}}
	pub := &mockPublisher{}
	push := &mockPushController{}
	mon := newTestMonitor(t, remote, pub, push, false)

	// First poll: transitions normal -> warned, emits event.
	mon.Poll(context.Background())
	if len(pub.Events()) != 1 {
		t.Fatalf("expected 1 event after first poll, got %d", len(pub.Events()))
	}

	// Second poll at same level: no transition, no event.
	pub.Reset()
	mon.Poll(context.Background())
	if len(pub.Events()) != 0 {
		t.Errorf("expected no events on duplicate poll, got %d", len(pub.Events()))
	}

	// Third poll at same level: still no event.
	mon.Poll(context.Background())
	if len(pub.Events()) != 0 {
		t.Errorf("expected no events on third duplicate poll, got %d", len(pub.Events()))
	}
}

func TestMonitor_DryRun(t *testing.T) {
	remote := &mockRemoteFS{quota: QuotaInfo{Used: 99, Total: 100}}
	pub := &mockPublisher{}
	push := &mockPushController{}
	mon := newTestMonitor(t, remote, pub, push, true) // dry-run = true

	mon.Poll(context.Background())

	snap := mon.State()
	if snap.State != StateBlocked {
		t.Errorf("expected state %q (dry-run still tracks state), got %q", StateBlocked, snap.State)
	}
	// Quota event should still be emitted (observability is not suppressed).
	events := pub.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 event even in dry-run, got %d", len(events))
	}
	// But push should NOT be actually paused.
	if push.IsPaused() {
		t.Error("push should not be paused in dry-run mode")
	}
	if push.PauseCount() != 0 {
		t.Error("PausePush should not have been called in dry-run mode")
	}
}

func TestMonitor_QuotaPollError(t *testing.T) {
	remote := &mockRemoteFS{
		quota: QuotaInfo{Used: 50, Total: 100},
		err:   fmt.Errorf("network timeout"),
	}
	pub := &mockPublisher{}
	push := &mockPushController{}
	mon := newTestMonitor(t, remote, pub, push, false)

	mon.Poll(context.Background())

	snap := mon.State()
	if snap.State != StateNormal {
		t.Errorf("state should remain normal on poll error, got %q", snap.State)
	}
	if snap.LastError == "" {
		t.Error("expected LastError to be set on poll failure")
	}
	if len(pub.Events()) != 0 {
		t.Errorf("expected no events on poll error, got %d", len(pub.Events()))
	}
}

func TestMonitor_NilPublisher(t *testing.T) {
	remote := &mockRemoteFS{quota: QuotaInfo{Used: 91, Total: 100}}
	push := &mockPushController{}
	mon := NewMonitor(remote, nil, push, DefaultConfig(), slog.Default())

	// Should not panic with nil publisher.
	mon.Poll(context.Background())

	snap := mon.State()
	if snap.State != StateWarned {
		t.Errorf("expected warned, got %q", snap.State)
	}
}

func TestMonitor_NilPushController(t *testing.T) {
	remote := &mockRemoteFS{quota: QuotaInfo{Used: 99, Total: 100}}
	pub := &mockPublisher{}
	mon := NewMonitor(remote, pub, nil, DefaultConfig(), slog.Default())

	// Should not panic with nil push controller.
	mon.Poll(context.Background())

	snap := mon.State()
	if snap.State != StateBlocked {
		t.Errorf("expected blocked, got %q", snap.State)
	}
}

func TestQuotaInfo_UsedPercent(t *testing.T) {
	tests := []struct {
		name string
		qi   QuotaInfo
		want float64
	}{
		{"half", QuotaInfo{Used: 50, Total: 100}, 50},
		{"full", QuotaInfo{Used: 100, Total: 100}, 100},
		{"empty", QuotaInfo{Used: 0, Total: 100}, 0},
		{"zero_total", QuotaInfo{Used: 50, Total: 0}, 0},
		{"negative_total", QuotaInfo{Used: 50, Total: -1}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.qi.UsedPercent()
			if got != tt.want {
				t.Errorf("UsedPercent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMonitor_RunCancellation(t *testing.T) {
	remote := &mockRemoteFS{quota: QuotaInfo{Used: 50, Total: 100}}
	pub := &mockPublisher{}
	push := &mockPushController{}
	cfg := DefaultConfig()
	cfg.PollInterval = 10 * time.Millisecond // fast poll for test
	mon := NewMonitor(remote, pub, push, cfg, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- mon.Run(ctx)
	}()

	// Let it run a few poll cycles.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit after context cancellation")
	}
}

func TestMonitor_FullLifecycle(t *testing.T) {
	// Simulate: normal -> warned -> blocked -> hysteresis resume -> normal.
	remote := &mockRemoteFS{quota: QuotaInfo{Used: 50, Total: 100}}
	pub := &mockPublisher{}
	push := &mockPushController{}
	mon := newTestMonitor(t, remote, pub, push, false)
	ctx := context.Background()

	// Step 1: normal at 50%.
	mon.Poll(ctx)
	if mon.State().State != StateNormal {
		t.Fatalf("step 1: expected normal, got %s", mon.State().State)
	}

	// Step 2: rise to 91% -> warned.
	remote.SetQuota(91, 100)
	mon.Poll(ctx)
	if mon.State().State != StateWarned {
		t.Fatalf("step 2: expected warned, got %s", mon.State().State)
	}

	// Step 3: rise to 99% -> blocked.
	remote.SetQuota(99, 100)
	mon.Poll(ctx)
	if mon.State().State != StateBlocked {
		t.Fatalf("step 3: expected blocked, got %s", mon.State().State)
	}
	if !push.IsPaused() {
		t.Fatal("step 3: expected push paused")
	}

	// Step 4: drop to 95%, still above hysteresis -> stays blocked.
	remote.SetQuota(95, 100)
	mon.Poll(ctx)
	if mon.State().State != StateBlocked {
		t.Fatalf("step 4: expected blocked (hysteresis), got %s", mon.State().State)
	}

	// Step 5: drop to 93%, below hysteresis -> resumes to warned.
	remote.SetQuota(93, 100)
	mon.Poll(ctx)
	if mon.State().State != StateWarned {
		t.Fatalf("step 5: expected warned, got %s", mon.State().State)
	}
	if push.IsPaused() {
		t.Fatal("step 5: expected push resumed")
	}

	// Step 6: drop to 50% -> normal.
	remote.SetQuota(50, 100)
	mon.Poll(ctx)
	if mon.State().State != StateNormal {
		t.Fatalf("step 6: expected normal, got %s", mon.State().State)
	}
}
