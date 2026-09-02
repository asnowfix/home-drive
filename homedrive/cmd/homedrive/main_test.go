package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"

	httpctl "github.com/asnowfix/home-drive/homedrive/internal/http"
)

func TestNewRootCmd_HasSubcommands(t *testing.T) {
	root := newRootCmd(nil)

	want := map[string]bool{
		"run": false,
		"ctl": false,
	}

	for _, cmd := range root.Commands() {
		if _, ok := want[cmd.Name()]; ok {
			want[cmd.Name()] = true
		}
	}

	for name, found := range want {
		if !found {
			t.Errorf("expected subcommand %q not found on root", name)
		}
	}
}

func TestNewCtlCmd_HasSubcommands(t *testing.T) {
	root := newRootCmd(nil)

	var ctlCmd *cobra.Command
	for _, cmd := range root.Commands() {
		if cmd.Name() == "ctl" {
			ctlCmd = cmd
			break
		}
	}
	if ctlCmd == nil {
		t.Fatal("ctl subcommand not found")
	}

	want := map[string]bool{
		"status": false,
		"pause":  false,
		"resume": false,
		"resync": false,
	}

	for _, cmd := range ctlCmd.Commands() {
		if _, ok := want[cmd.Name()]; ok {
			want[cmd.Name()] = true
		}
	}

	for name, found := range want {
		if !found {
			t.Errorf("expected ctl subcommand %q not found", name)
		}
	}
}

func TestDryRunFlag_DefaultFalse(t *testing.T) {
	root := newRootCmd(nil)
	root.SetArgs([]string{"run"})
	root.SetContext(context.Background())

	var capturedCtx context.Context
	for _, cmd := range root.Commands() {
		if cmd.Name() == "run" {
			cmd.RunE = func(cmd *cobra.Command, args []string) error {
				capturedCtx = cmd.Context()
				return nil // capture only; don't start the agent
			}
			break
		}
	}

	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedCtx == nil {
		t.Fatal("context was not captured")
	}

	dryRun, ok := capturedCtx.Value(DryRunKey).(bool)
	if !ok {
		t.Fatal("dry_run key not found in context")
	}
	if dryRun {
		t.Error("expected dry_run=false by default, got true")
	}
}

func TestDryRunFlag_SetTrue(t *testing.T) {
	root := newRootCmd(nil)
	root.SetArgs([]string{"--dry-run", "run"})

	var capturedCtx context.Context
	for _, cmd := range root.Commands() {
		if cmd.Name() == "run" {
			cmd.RunE = func(cmd *cobra.Command, args []string) error {
				capturedCtx = cmd.Context()
				return nil // capture only; don't start the agent
			}
			break
		}
	}

	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedCtx == nil {
		t.Fatal("context was not captured")
	}

	dryRun, ok := capturedCtx.Value(DryRunKey).(bool)
	if !ok {
		t.Fatal("dry_run key not found in context")
	}
	if !dryRun {
		t.Error("expected dry_run=true when --dry-run is set, got false")
	}
}

func TestVersionFlag(t *testing.T) {
	root := newRootCmd(nil)
	root.SetArgs([]string{"--version"})

	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// stubCtlDeps implements every httpctl.Deps interface with canned
// responses, so ctl subcommand tests exercise a real HTTP round trip
// without needing a full Agent (rclone/mqtt/watcher).
type stubCtlDeps struct{}

func (stubCtlDeps) Pause(context.Context) error  { return nil }
func (stubCtlDeps) Resume(context.Context) error { return nil }
func (stubCtlDeps) ForceResync(context.Context) error {
	return nil
}
func (stubCtlDeps) Reload(context.Context) error { return nil }
func (stubCtlDeps) Status(context.Context) (httpctl.StatusInfo, error) {
	return httpctl.StatusInfo{State: "running", Version: "test"}, nil
}
func (stubCtlDeps) Healthz(context.Context) (httpctl.HealthResult, error) {
	return httpctl.HealthResult{Healthy: true}, nil
}

// startStubCtlServer starts a real internal/http.Server backed by
// stubCtlDeps on an ephemeral loopback port and writes a config.yaml
// pointing at it, so `homedrive ctl` subcommands can be tested end to end.
func startStubCtlServer(t *testing.T) (configPath string) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	deps := httpctl.Deps{
		Pausable:       stubCtlDeps{},
		Resyncable:     stubCtlDeps{},
		Reloadable:     stubCtlDeps{},
		StatusProvider: stubCtlDeps{},
		HealthChecker:  stubCtlDeps{},
	}
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	srv, err := httpctl.NewServer(httpctl.ServerConfig{}, deps, httpctl.NewMetrics(), log)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		_ = srv.Shutdown(context.Background())
	})

	dir := t.TempDir()
	configPath = filepath.Join(dir, "config.yaml")
	yaml := fmt.Sprintf("http:\n  listen: %q\n", ln.Addr().String())
	if err := os.WriteFile(configPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return configPath
}

func TestCtlStatus_Executes(t *testing.T) {
	configPath := startStubCtlServer(t)

	root := newRootCmd(nil)
	root.SetArgs([]string{"ctl", "--config", configPath, "status"})

	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCtlPause_Executes(t *testing.T) {
	configPath := startStubCtlServer(t)

	root := newRootCmd(nil)
	root.SetArgs([]string{"ctl", "--config", configPath, "pause"})

	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCtlResume_Executes(t *testing.T) {
	configPath := startStubCtlServer(t)

	root := newRootCmd(nil)
	root.SetArgs([]string{"ctl", "--config", configPath, "resume"})

	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCtlResync_Executes(t *testing.T) {
	configPath := startStubCtlServer(t)

	root := newRootCmd(nil)
	root.SetArgs([]string{"ctl", "--config", configPath, "resync"})

	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCtl_NoServerRunning_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	// Point at a port nothing is listening on.
	if err := os.WriteFile(configPath, []byte("http:\n  listen: \"127.0.0.1:1\"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	root := newRootCmd(nil)
	root.SetArgs([]string{"ctl", "--config", configPath, "status"})

	if err := root.ExecuteContext(context.Background()); err == nil {
		t.Fatal("expected an error when no agent is listening")
	}
}

func TestCtlTarget_Cases(t *testing.T) {
	t.Run("missing config falls back to default", func(t *testing.T) {
		addr, token, timeout := ctlTarget("/nonexistent/config.yaml")
		if addr != defaultCtlAddr {
			t.Errorf("addr = %q, want %q", addr, defaultCtlAddr)
		}
		if token != "" {
			t.Errorf("token = %q, want empty", token)
		}
		if timeout != 0 {
			t.Errorf("timeout = %v, want 0", timeout)
		}
	})

	t.Run("empty http.listen falls back to default", func(t *testing.T) {
		dir := t.TempDir()
		configPath := filepath.Join(dir, "config.yaml")
		if err := os.WriteFile(configPath, []byte("local_root: /tmp\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		addr, token, timeout := ctlTarget(configPath)
		if addr != defaultCtlAddr {
			t.Errorf("addr = %q, want %q", addr, defaultCtlAddr)
		}
		if token != "" {
			t.Errorf("token = %q, want empty", token)
		}
		if timeout != 0 {
			t.Errorf("timeout = %v, want 0", timeout)
		}
	})

	t.Run("explicit http.listen is used", func(t *testing.T) {
		dir := t.TempDir()
		configPath := filepath.Join(dir, "config.yaml")
		if err := os.WriteFile(configPath, []byte("http:\n  listen: \"10.0.0.1:9999\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		addr, token, _ := ctlTarget(configPath)
		if addr != "10.0.0.1:9999" {
			t.Errorf("addr = %q, want 10.0.0.1:9999", addr)
		}
		if token != "" {
			t.Errorf("token = %q, want empty", token)
		}
	})

	t.Run("auth_token is read from config", func(t *testing.T) {
		dir := t.TempDir()
		configPath := filepath.Join(dir, "config.yaml")
		yaml := "http:\n  listen: \"127.0.0.1:6090\"\n  auth_token: \"s3cr3t\"\n"
		if err := os.WriteFile(configPath, []byte(yaml), 0o600); err != nil {
			t.Fatal(err)
		}
		addr, token, _ := ctlTarget(configPath)
		if addr != "127.0.0.1:6090" {
			t.Errorf("addr = %q, want 127.0.0.1:6090", addr)
		}
		if token != "s3cr3t" {
			t.Errorf("token = %q, want s3cr3t", token)
		}
	})

	t.Run("ctl_timeout is read from config", func(t *testing.T) {
		dir := t.TempDir()
		configPath := filepath.Join(dir, "config.yaml")
		yaml := "http:\n  listen: \"127.0.0.1:6090\"\n  ctl_timeout: 45s\n"
		if err := os.WriteFile(configPath, []byte(yaml), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, timeout := ctlTarget(configPath)
		if timeout != 45*time.Second {
			t.Errorf("timeout = %v, want 45s", timeout)
		}
	})
}

func TestRunAgent_InvalidConfigPath_ReturnsError(t *testing.T) {
	root := newRootCmd(nil)
	root.SetArgs([]string{"run", "--config", "/nonexistent/config.yaml"})

	if err := root.ExecuteContext(context.Background()); err == nil {
		t.Fatal("expected an error for a nonexistent config path")
	}
}

func TestDryRunFlag_PropagatedToCtl(t *testing.T) {
	configPath := startStubCtlServer(t)

	root := newRootCmd(nil)
	root.SetArgs([]string{"--dry-run", "ctl", "--config", configPath, "status"})

	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
