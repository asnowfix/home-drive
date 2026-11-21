package desktop

import (
	"errors"
	"io"
	"log/slog"
	"reflect"
	"syscall"
	"testing"
	"time"
)

var quietLog = slog.New(slog.NewJSONHandler(io.Discard, nil))

func TestShutdown_Sequencing(t *testing.T) {
	errBusy := errors.New("fusermount3: failed to unmount /x: Device or resource busy (code exit status 1)")
	tests := []struct {
		name      string
		setup     func(f *fakeMount)
		wantCalls []string
		wantRes   ShutdownResult
		wantErr   bool
	}{
		{
			name:      "FlushThenUnmount",
			setup:     func(f *fakeMount) { f.busy = true },
			wantCalls: []string{"wait", "unmount", "close"},
		},
		{
			name: "FlushTimeoutLeavesDirtyInCache",
			setup: func(f *fakeMount) {
				f.busy, f.waitBlocks = true, true
			},
			wantCalls: []string{"wait", "unmount", "close"},
			wantRes:   ShutdownResult{FlushTimedOut: true},
		},
		{
			name: "EBUSYFallsBackToLazy",
			setup: func(f *fakeMount) {
				f.unmountErrs = []error{errBusy}
			},
			wantCalls: []string{"wait", "unmount", "lazy", "close"},
			wantRes:   ShutdownResult{Lazy: true},
		},
		{
			name: "TypedEBUSYFallsBackToLazy",
			setup: func(f *fakeMount) {
				f.unmountErrs = []error{syscall.EBUSY}
			},
			wantCalls: []string{"wait", "unmount", "lazy", "close"},
			wantRes:   ShutdownResult{Lazy: true},
		},
		{
			name: "OtherUnmountErrorIsFatal",
			setup: func(f *fakeMount) {
				f.unmountErrs = []error{errors.New("permission denied")}
			},
			wantCalls: []string{"wait", "unmount", "close"},
			wantErr:   true,
		},
		{
			name: "LazyFailureIsFatal",
			setup: func(f *fakeMount) {
				f.unmountErrs = []error{errBusy}
				f.lazyErr = errors.New("no fusermount3")
			},
			wantCalls: []string{"wait", "unmount", "lazy", "close"},
			wantRes:   ShutdownResult{Lazy: true},
			wantErr:   true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeMount()
			tc.setup(f)
			n := &fakeNotifier{}
			res, err := Shutdown(f, n, 50*time.Millisecond, quietLog)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if res != tc.wantRes {
				t.Errorf("result = %+v, want %+v", res, tc.wantRes)
			}
			if got := f.Calls(); !reflect.DeepEqual(got, tc.wantCalls) {
				t.Errorf("calls = %v, want %v", got, tc.wantCalls)
			}
			if got := n.Events(); !reflect.DeepEqual(got, []string{"stopping"}) {
				t.Errorf("notify events = %v, want [stopping]", got)
			}
		})
	}
}

func TestIsBusy_Cases(t *testing.T) {
	tests := []struct {
		err  error
		want bool
	}{
		{syscall.EBUSY, true},
		{errors.New("Device or resource busy"), true},
		{errors.New("permission denied"), false},
	}
	for _, tc := range tests {
		if got := isBusy(tc.err); got != tc.want {
			t.Errorf("isBusy(%v) = %v", tc.err, got)
		}
	}
}
