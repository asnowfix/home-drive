package main

import (
	"fmt"
	"log/slog"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/asnowfix/home-drive/homedrive/internal/desktop"
)

// newMountCmd builds `homedrive mount`, the desktop mode: an on-demand Google
// Drive mount (rclone vfs + mount2). All logic lives in internal/desktop; this
// is wiring only. See homedrive/docs/design/desktop-mode.md.
func newMountCmd() *cobra.Command {
	var configPath string

	cmd := &cobra.Command{
		Use:   "mount",
		Short: "Mount Google Drive on demand (desktop mode)",
		Long: "Mount Google Drive as a filesystem with a bounded local cache " +
			"(desktop mode). Configuration: desktop.yaml, by default " +
			"$XDG_CONFIG_HOME/homedrive/desktop.yaml. Stops cleanly on " +
			"SIGTERM/SIGINT: waits for uploads, then unmounts.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runMount(cmd, configPath)
		},
	}
	env, err := desktop.OSEnv()
	def := ""
	if err == nil {
		def = env.DefaultConfigPath()
	}
	cmd.Flags().StringVar(&configPath, "config", def, "path to desktop.yaml")
	return cmd
}

func runMount(cmd *cobra.Command, configPath string) error {
	env, err := desktop.OSEnv()
	if err != nil {
		return err
	}
	cfg, err := desktop.LoadOrDefault(configPath, env, cmd.Flags().Changed("config"))
	if err != nil {
		return fmt.Errorf("load desktop config: %w", err)
	}

	ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	return desktop.Run(ctx, cfg, desktop.Deps{
		Mounter:  desktop.RcloneMounter{},
		Notifier: desktop.NewNotifier(env),
		Env:      env,
		Log:      slog.Default(),
	})
}
