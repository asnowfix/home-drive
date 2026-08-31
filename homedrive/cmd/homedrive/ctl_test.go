package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// newTimeoutFlagCmd builds a bare *cobra.Command carrying the same
// "timeout" persistent flag newCtlCmd registers on the real `ctl` parent
// command, parsed with args, so ctlEffectiveTimeout can be exercised
// without building the full command tree.
func newTimeoutFlagCmd(t *testing.T, args []string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "x"}
	var timeout time.Duration
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "")
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatalf("ParseFlags(%v): %v", args, err)
	}
	return cmd
}

func TestCtlEffectiveTimeout_Cases(t *testing.T) {
	tests := []struct {
		name       string
		flagArgs   []string
		cfgTimeout time.Duration
		want       time.Duration
	}{
		{
			name:       "flag overrides config",
			flagArgs:   []string{"--timeout=5s"},
			cfgTimeout: 45 * time.Second,
			want:       5 * time.Second,
		},
		{
			name:       "config is used when flag is unset",
			flagArgs:   nil,
			cfgTimeout: 45 * time.Second,
			want:       45 * time.Second,
		},
		{
			name:       "default is used when neither flag nor config is set",
			flagArgs:   nil,
			cfgTimeout: 0,
			want:       defaultCtlHTTPTimeout,
		},
		{
			name:       "flag overrides the default when config is unset",
			flagArgs:   []string{"--timeout=2s"},
			cfgTimeout: 0,
			want:       2 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newTimeoutFlagCmd(t, tt.flagArgs)
			got := ctlEffectiveTimeout(cmd, tt.cfgTimeout)
			if got != tt.want {
				t.Errorf("ctlEffectiveTimeout() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCtlDo_HonorsExplicitTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	addr := srv.Listener.Addr().String()

	err := ctlDo(context.Background(), http.MethodGet, addr, "", 5*time.Millisecond, "/", nil)
	if err == nil {
		t.Fatal("expected an error when the timeout is shorter than the server's response delay")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want one wrapping context.DeadlineExceeded", err)
	}

	if err := ctlDo(context.Background(), http.MethodGet, addr, "", time.Second, "/", nil); err != nil {
		t.Errorf("unexpected error with a generous timeout: %v", err)
	}
}

func TestCtl_TimeoutFlag_AppliesEndToEnd(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"state":"running"}`))
	}))
	defer slow.Close()

	configPath := writeCtlConfig(t, slow.Listener.Addr().String())

	root := newRootCmd(nil)
	root.SetArgs([]string{"ctl", "--config", configPath, "--timeout", "5ms", "status"})
	if err := root.ExecuteContext(context.Background()); err == nil {
		t.Fatal("expected an error with a --timeout shorter than the server's response delay")
	}

	root = newRootCmd(nil)
	root.SetArgs([]string{"ctl", "--config", configPath, "--timeout", "1s", "status"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Errorf("unexpected error with a generous --timeout: %v", err)
	}
}

// writeCtlConfig writes a minimal config.yaml pointing http.listen at addr
// and returns its path.
func writeCtlConfig(t *testing.T, addr string) string {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	yaml := "http:\n  listen: \"" + addr + "\"\n"
	if err := os.WriteFile(configPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return configPath
}
