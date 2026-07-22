// Binary homedrive is a bidirectional Google Drive sync agent for headless
// ARM64 Linux (Raspberry Pi NAS). See PLAN.md for architecture details.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

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
	logLevel := &slog.LevelVar{}
	logLevel.Set(slog.LevelInfo)
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: logLevel,
	}))
	slog.SetDefault(logger)

	root := newRootCmd(logLevel)
	if err := root.ExecuteContext(context.Background()); err != nil {
		slog.Error("fatal error", "error", err)
		os.Exit(1)
	}
}

// defaultConfigPath returns the XDG-compliant per-user config path.
func defaultConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "/etc/homedrive/config.yaml"
	}
	return filepath.Join(dir, "homedrive", "config.yaml")
}

// newRootCmd builds the cobra command tree. logLevel is the shared,
// mutable log level applied by `run` and updated live by SIGHUP/POST
// /reload; it is nil in tests that don't exercise reload.
func newRootCmd(logLevel *slog.LevelVar) *cobra.Command {
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

	root.AddCommand(newRunCmd(logLevel))
	root.AddCommand(newCtlCmd())

	return root
}

func newRunCmd(logLevel *slog.LevelVar) *cobra.Command {
	var configPath string

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Start the sync agent",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runAgent(cmd, configPath, logLevel)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", defaultConfigPath(),
		"path to config file")
	return cmd
}

// runAgent builds the full sync engine (watcher, push syncer, pull,
// bisync, MQTT, HTTP control endpoint) and runs it until SIGTERM/SIGINT.
func runAgent(cmd *cobra.Command, configPath string, logLevel *slog.LevelVar) error {
	dryRun, _ := cmd.Context().Value(DryRunKey).(bool)

	ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	agent, err := newAgent(ctx, AgentOpts{
		ConfigPath: configPath,
		DryRun:     dryRun,
		Version:    version,
		Log:        slog.Default(),
		LogLevel:   logLevel,
	})
	if err != nil {
		return fmt.Errorf("build agent: %w", err)
	}

	return agent.Run(ctx)
}

func newCtlCmd() *cobra.Command {
	var configPath string

	ctl := &cobra.Command{
		Use:   "ctl",
		Short: "Control a running homedrive agent via HTTP",
	}
	ctl.PersistentFlags().StringVar(&configPath, "config", defaultConfigPath(),
		"path to config file (used to find the control endpoint address)")

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
			return ctlRunStatus(cmd)
		},
	}
}

func newCtlPauseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pause",
		Short: "Pause the sync agent",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ctlRunAction(cmd, "pause")
		},
	}
}

func newCtlResumeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resume",
		Short: "Resume the sync agent",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ctlRunAction(cmd, "resume")
		},
	}
}

func newCtlResyncCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resync",
		Short: "Force an immediate bisync",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ctlRunAction(cmd, "resync")
		},
	}
}
