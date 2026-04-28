// Binary homedrive is a bidirectional Google Drive sync agent for headless
// ARM64 Linux (Raspberry Pi NAS). See PLAN.md for architecture details.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/spf13/cobra"
)

// version is set at build time via -ldflags.
var version = "dev"

// ctxKey is an unexported type for context keys to avoid collisions.
type ctxKey string

const (
	// DryRunKey is the context key for the dry-run flag.
	DryRunKey ctxKey = "dry_run"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	root := newRootCmd()
	if err := root.ExecuteContext(context.Background()); err != nil {
		slog.Error("fatal error", "error", err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	var dryRun bool

	root := &cobra.Command{
		Use:     "homedrive",
		Short:   "Bidirectional Google Drive sync agent",
		Version: version,
		PersistentPreRun: func(cmd *cobra.Command, _ []string) {
			ctx := context.WithValue(cmd.Context(), DryRunKey, dryRun)
			cmd.SetContext(ctx)
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.PersistentFlags().BoolVar(&dryRun, "dry-run", false,
		"log intended actions without making remote changes")

	root.AddCommand(newRunCmd())
	root.AddCommand(newCtlCmd())

	return root
}

func newRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Start the sync agent",
		RunE:  runAgent,
	}
}

func runAgent(cmd *cobra.Command, _ []string) error {
	dryRun, _ := cmd.Context().Value(DryRunKey).(bool)
	slog.Info("starting homedrive agent",
		"version", version,
		"dry_run", dryRun,
	)
	// Phase 1+ will wire watcher, syncer, http, mqtt here.
	return nil
}

func newCtlCmd() *cobra.Command {
	ctl := &cobra.Command{
		Use:   "ctl",
		Short: "Control a running homedrive agent via HTTP",
	}

	ctl.AddCommand(newCtlStatusCmd())
	ctl.AddCommand(newCtlPauseCmd())
	ctl.AddCommand(newCtlResumeCmd())
	ctl.AddCommand(newCtlResyncCmd())

	return ctl
}

func newCtlStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show agent status",
		RunE: func(cmd *cobra.Command, _ []string) error {
			slog.Info("ctl: status requested")
			// Phase 8 will call GET /status on the HTTP endpoint.
			return nil
		},
	}
}

func newCtlPauseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pause",
		Short: "Pause the sync agent",
		RunE: func(cmd *cobra.Command, _ []string) error {
			slog.Info("ctl: pause requested")
			// Phase 8 will call POST /pause on the HTTP endpoint.
			return nil
		},
	}
}

func newCtlResumeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resume",
		Short: "Resume the sync agent",
		RunE: func(cmd *cobra.Command, _ []string) error {
			slog.Info("ctl: resume requested")
			// Phase 8 will call POST /resume on the HTTP endpoint.
			return nil
		},
	}
}

func newCtlResyncCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resync",
		Short: "Force an immediate bisync",
		RunE: func(cmd *cobra.Command, _ []string) error {
			slog.Info("ctl: resync requested")
			// Phase 8 will call POST /resync on the HTTP endpoint.
			return nil
		},
	}
}
