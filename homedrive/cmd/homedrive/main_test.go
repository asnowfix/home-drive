package main

import (
	"context"
	"testing"

	"github.com/spf13/cobra"
)

func TestNewRootCmd_HasSubcommands(t *testing.T) {
	root := newRootCmd()

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
	root := newRootCmd()

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
	root := newRootCmd()
	root.SetArgs([]string{"run"})
	root.SetContext(context.Background())

	var capturedCtx context.Context
	for _, cmd := range root.Commands() {
		if cmd.Name() == "run" {
			original := cmd.RunE
			cmd.RunE = func(cmd *cobra.Command, args []string) error {
				capturedCtx = cmd.Context()
				return original(cmd, args)
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
	root := newRootCmd()
	root.SetArgs([]string{"--dry-run", "run"})

	var capturedCtx context.Context
	for _, cmd := range root.Commands() {
		if cmd.Name() == "run" {
			original := cmd.RunE
			cmd.RunE = func(cmd *cobra.Command, args []string) error {
				capturedCtx = cmd.Context()
				return original(cmd, args)
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
	root := newRootCmd()
	root.SetArgs([]string{"--version"})

	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCtlStatus_Executes(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"ctl", "status"})

	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCtlPause_Executes(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"ctl", "pause"})

	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCtlResume_Executes(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"ctl", "resume"})

	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCtlResync_Executes(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"ctl", "resync"})

	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDryRunFlag_PropagatedToCtl(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"--dry-run", "ctl", "status"})

	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
