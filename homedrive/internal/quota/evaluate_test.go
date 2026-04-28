package quota

import (
	"context"
	"log/slog"
	"testing"
)

func TestMonitor_StateTransitions(t *testing.T) {
	// Table-driven test of the full state machine with hysteresis.
	tests := []struct {
		name          string
		prevState     State
		usedPct       float64
		wantState     State
		wantPaused    bool
		wantEventType string // "" means no event expected
	}{
		{
			name:       "Normal_50pct_stays_normal",
			prevState:  StateNormal,
			usedPct:    50,
			wantState:  StateNormal,
			wantPaused: false,
		},
		{
			name:          "Normal_91pct_transitions_to_warned",
			prevState:     StateNormal,
			usedPct:       91,
			wantState:     StateWarned,
			wantPaused:    false,
			wantEventType: "quota.warning",
		},
		{
			name:          "Normal_99pct_transitions_to_blocked",
			prevState:     StateNormal,
			usedPct:       99,
			wantState:     StateBlocked,
			wantPaused:    true,
			wantEventType: "quota.exhausted",
		},
		{
			name:       "Warned_91pct_stays_warned",
			prevState:  StateWarned,
			usedPct:    91,
			wantState:  StateWarned,
			wantPaused: false,
		},
		{
			name:          "Warned_99pct_transitions_to_blocked",
			prevState:     StateWarned,
			usedPct:       99,
			wantState:     StateBlocked,
			wantPaused:    true,
			wantEventType: "quota.exhausted",
		},
		{
			name:       "Warned_50pct_transitions_to_normal",
			prevState:  StateWarned,
			usedPct:    50,
			wantState:  StateNormal,
			wantPaused: false,
		},
		{
			name:       "Blocked_99pct_stays_blocked",
			prevState:  StateBlocked,
			usedPct:    99,
			wantState:  StateBlocked,
			wantPaused: true,
		},
		{
			name:       "Blocked_95pct_stays_blocked_hysteresis",
			prevState:  StateBlocked,
			usedPct:    95,
			wantState:  StateBlocked,
			wantPaused: true,
		},
		{
			name:       "Blocked_94pct_stays_blocked_at_threshold",
			prevState:  StateBlocked,
			usedPct:    94,
			wantState:  StateBlocked,
			wantPaused: true,
		},
		{
			name:          "Blocked_93pct_resumes_to_warned",
			prevState:     StateBlocked,
			usedPct:       93,
			wantState:     StateWarned,
			wantPaused:    false,
			wantEventType: "quota.warning",
		},
		{
			name:       "Blocked_50pct_resumes_to_normal",
			prevState:  StateBlocked,
			usedPct:    50,
			wantState:  StateNormal,
			wantPaused: false,
		},
		{
			name:       "Blocked_89pct_resumes_to_normal",
			prevState:  StateBlocked,
			usedPct:    89,
			wantState:  StateNormal,
			wantPaused: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			used := int64(tt.usedPct)
			remote := &mockRemoteFS{quota: QuotaInfo{Used: used, Total: 100}}
			pub := &mockPublisher{}
			push := &mockPushController{}
			cfg := DefaultConfig()
			log := slog.Default()
			mon := NewMonitor(remote, pub, push, cfg, log)

			// Set the initial state to simulate prior history.
			mon.mu.Lock()
			mon.state = tt.prevState
			if tt.prevState == StateBlocked {
				push.PausePush() // simulate prior pause
			}
			mon.mu.Unlock()

			// Clear events from setup.
			pub.Reset()
			initialPauseCt := push.PauseCount()
			initialResumeCt := push.ResumeCount()

			mon.Poll(context.Background())

			snap := mon.State()
			if snap.State != tt.wantState {
				t.Errorf("state: got %q, want %q", snap.State, tt.wantState)
			}

			if push.IsPaused() != tt.wantPaused {
				t.Errorf("paused: got %v, want %v", push.IsPaused(), tt.wantPaused)
			}

			events := pub.Events()
			if tt.wantEventType == "" {
				if len(events) != 0 {
					t.Errorf("expected no events, got %d: %+v", len(events), events)
				}
			} else {
				if len(events) != 1 {
					t.Fatalf("expected 1 event, got %d", len(events))
				}
				if events[0].Payload["type"] != tt.wantEventType {
					t.Errorf("event type: got %v, want %s",
						events[0].Payload["type"], tt.wantEventType)
				}
			}

			// No state change means no new pause/resume calls.
			if tt.prevState == tt.wantState {
				if push.PauseCount() != initialPauseCt {
					t.Error("PausePush called without a state transition")
				}
				if push.ResumeCount() != initialResumeCt {
					t.Error("ResumePush called without a state transition")
				}
			}
		})
	}
}
