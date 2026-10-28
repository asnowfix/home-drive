package syncer

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestSyncer_ExecRemove_Cases(t *testing.T) {
	tests := []struct {
		name        string
		delErr      error
		wantCalls   int
		wantSuccess bool
	}{
		{
			name:        "already absent: one attempt, success",
			delErr:      fmt.Errorf("delete: %w", ErrRemoteNotFound),
			wantCalls:   1,
			wantSuccess: true,
		},
		{
			name:        "other error: still retried and fails",
			delErr:      errors.New("boom"),
			wantCalls:   DefaultConfig().Retry.MaxAttempts,
			wantSuccess: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			remote := newMockRemoteFS()
			remote.delErr = func(string) error { return tt.delErr }
			pub := newMockPublisher()
			s := New(DefaultConfig(), remote, newMockStore(), newMockAuditLogger(),
				pub, newDiscardLogger(), WithSleepFunc(noSleep))

			events := make(chan Event, 1)
			events <- Event{Path: "docs/gone.txt", Op: OpRemove, At: time.Now()}
			runSyncer(t, s, events, make(chan DirRename), func() {})

			if got := len(remote.getDeleteCalls()); got != tt.wantCalls {
				t.Errorf("DeleteFile calls = %d, want %d", got, tt.wantCalls)
			}
			wantTopic := "homedrive/test/events/push.failure"
			if tt.wantSuccess {
				wantTopic = "homedrive/test/events/push.success"
			}
			msgs := pub.getEvents()
			if len(msgs) != 1 || msgs[0].Topic != wantTopic {
				t.Errorf("published = %+v, want one %s", msgs, wantTopic)
			}
		})
	}
}
