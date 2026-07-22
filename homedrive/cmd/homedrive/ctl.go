// ctl.go implements the `homedrive ctl` subcommands as real HTTP calls
// against the running agent's control endpoint (internal/http), replacing
// the previous no-op stubs that only logged a message.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"github.com/asnowfix/home-drive/homedrive/internal/config"
	httpctl "github.com/asnowfix/home-drive/homedrive/internal/http"
)

// defaultCtlAddr matches httpctl.NewServer's default when http.listen is
// unset in config.
const defaultCtlAddr = "127.0.0.1:6090"

// ctlHTTPTimeout bounds every ctl HTTP call.
const ctlHTTPTimeout = 10 * time.Second

// ctlAddr resolves the control endpoint address from the config file at
// configPath, falling back to the default loopback address if the config
// cannot be loaded or does not set http.listen.
func ctlAddr(configPath string) string {
	cfg, err := config.Load(configPath)
	if err != nil {
		slog.Warn("ctl: failed to load config, using default control address",
			"config", configPath, "default_addr", defaultCtlAddr, "error", err)
		return defaultCtlAddr
	}
	if cfg.HTTP.Listen == "" {
		return defaultCtlAddr
	}
	return cfg.HTTP.Listen
}

// ctlRunStatus calls GET /status and logs the agent's real state.
func ctlRunStatus(cmd *cobra.Command) error {
	addr := ctlAddr(ctlConfigPath(cmd))

	var info httpctl.StatusInfo
	if err := ctlDo(cmd.Context(), http.MethodGet, addr, "/status", &info); err != nil {
		return err
	}

	slog.Info("agent status",
		"state", info.State,
		"version", info.Version,
		"pending_up", info.PendingUp,
		"pending_down", info.PendingDown,
		"last_push", info.LastPush,
		"last_pull", info.LastPull,
		"quota_used_pct", info.QuotaUsedPct,
		"conflicts_24h", info.Conflicts24h,
		"dry_run", info.DryRun,
		"uptime_seconds", info.UptimeSeconds,
	)
	return nil
}

// ctlRunAction calls POST /<action> (pause, resume, or resync) and logs
// the agent's real response.
func ctlRunAction(cmd *cobra.Command, action string) error {
	addr := ctlAddr(ctlConfigPath(cmd))

	var result map[string]string
	if err := ctlDo(cmd.Context(), http.MethodPost, addr, "/"+action, &result); err != nil {
		return err
	}

	slog.Info("ctl action completed", "action", action, "result", result["status"])
	return nil
}

// ctlConfigPath reads the --config flag, which is registered as a
// persistent flag on the `ctl` parent command and inherited by every
// subcommand.
func ctlConfigPath(cmd *cobra.Command) string {
	configPath, _ := cmd.Flags().GetString("config")
	return configPath
}

// ctlDo issues an HTTP request against the running agent's control
// endpoint and decodes the JSON response into out (skipped if out is nil).
func ctlDo(ctx context.Context, method, addr, path string, out any) error {
	url := "http://" + addr + path
	reqCtx, cancel := context.WithTimeout(ctx, ctlHTTPTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, method, url, nil)
	if err != nil {
		return fmt.Errorf("ctl: build request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("ctl: request to %s failed (is the agent running?): %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("ctl: %s %s returned %d: %s", method, path, resp.StatusCode, string(body))
	}

	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("ctl: decode response from %s: %w", path, err)
	}
	return nil
}
