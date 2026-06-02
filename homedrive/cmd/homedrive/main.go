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
	"time"

	"github.com/spf13/cobra"

	"github.com/asnowfix/home-drive/homedrive/internal/config"
	"github.com/asnowfix/home-drive/homedrive/internal/rcloneclient"
	"github.com/asnowfix/home-drive/homedrive/internal/store"
	"github.com/asnowfix/home-drive/homedrive/internal/syncer"
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

// defaultConfigPath returns the XDG-compliant per-user config path.
func defaultConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "/etc/homedrive/config.yaml"
	}
	return filepath.Join(dir, "homedrive", "config.yaml")
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
	var configPath string

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Start the sync agent",
		RunE:  runAgent,
	}
	cmd.Flags().StringVar(&configPath, "config", defaultConfigPath(),
		"path to config file")
	return cmd
}

func runAgent(cmd *cobra.Command, _ []string) error {
	dryRun, _ := cmd.Context().Value(DryRunKey).(bool)
	configPath, _ := cmd.Flags().GetString("config")

	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if dryRun {
		cfg.DryRun = true
	}

	if err := os.MkdirAll(filepath.Dir(cfg.State.Path), 0o755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	if err := os.MkdirAll(cfg.LocalRoot, 0o755); err != nil {
		return fmt.Errorf("create local root: %w", err)
	}

	journal, err := store.OpenJournal(cfg.State.Path, slog.Default())
	if err != nil {
		return fmt.Errorf("open journal: %w", err)
	}
	defer func() { _ = journal.Close() }()

	var auditAdapter syncer.AuditLogger
	if cfg.State.AuditLog != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.State.AuditLog), 0o755); err != nil {
			return fmt.Errorf("create audit log dir: %w", err)
		}
		f, err := os.OpenFile(cfg.State.AuditLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
		if err != nil {
			return fmt.Errorf("open audit log: %w", err)
		}
		defer func() { _ = f.Close() }()
		auditAdapter = &auditLoggerAdapter{a: store.NewAuditor(f, slog.Default())}
	}

	slog.Info("starting homedrive agent",
		"version", version,
		"config", configPath,
		"local_root", cfg.LocalRoot,
		"remote", cfg.Remote,
		"dry_run", cfg.DryRun,
	)

	ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	rfs, err := rcloneclient.NewRcloneFS(ctx, rcloneclient.RcloneFSConfig{
		Remote:     cfg.Remote,
		ConfigPath: cfg.RcloneConfig,
		Log:        slog.Default(),
	})
	if err != nil {
		return fmt.Errorf("init rclone remote: %w", err)
	}

	interval := cfg.Pull.ChangesAPIInterval.Duration
	if interval <= 0 {
		interval = 30 * time.Second
	}

	puller := syncer.NewPuller(
		syncer.PullerConfig{
			Interval:       interval,
			LocalRoot:      cfg.LocalRoot,
			ConflictPolicy: syncer.ConflictPolicy(cfg.Conflict.Policy),
			DryRun:         cfg.DryRun,
		},
		&rcloneSyncerAdapter{fs: rfs},
		&journalSyncerAdapter{j: journal, logger: slog.Default()},
		auditAdapter,
		noopPublisher{},
		slog.Default(),
		time.Now,
	)

	pullerDone := make(chan error, 1)
	go func() { pullerDone <- puller.Run(ctx) }()

	<-ctx.Done()
	err = <-pullerDone
	slog.Info("homedrive agent stopped")
	if err != nil && err != context.Canceled {
		return err
	}
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
			return nil
		},
	}
}
