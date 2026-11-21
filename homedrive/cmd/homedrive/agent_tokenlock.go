package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/asnowfix/home-drive/homedrive/internal/config"
	"github.com/asnowfix/home-drive/homedrive/internal/tokenlock"
)

// acquireTokenLock takes the exclusive token-file lock shared with desktop
// mode (`homedrive mount`) so two homedrive processes can never share one
// rclone.conf (design section 10, issue #176). The token file is the
// configured rclone_config, or rclone's default location when unset.
//
// Only a lock held by another process is fatal. Where the lock file cannot be
// created (a read-only token directory under ProtectHome=read-only, no home
// directory) the agent still starts, with a warning: the lock is a guard
// against a misconfiguration, not something the sync itself depends on. A
// config that cannot be loaded is not this function's business either: newAgent
// reports the real config problem.
func acquireTokenLock(configPath string, log *slog.Logger) (*tokenlock.Lock, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		log.Warn("token file lock skipped: config not loadable", "error", err)
		return nil, nil //nolint:nilerr // newAgent re-loads and reports the error
	}
	token := cfg.RcloneConfig
	if token == "" {
		home, herr := os.UserHomeDir()
		if herr != nil {
			log.Warn("token file lock skipped: no home directory", "error", herr)
			return nil, nil //nolint:nilerr // no token path to lock without a home directory
		}
		token = tokenlock.RcloneDefaultPath(os.Getenv("XDG_CONFIG_HOME"), home)
	}
	lock, err := tokenlock.Acquire(token)
	if errors.Is(err, tokenlock.ErrHeld) {
		return nil, fmt.Errorf("token file %q: %w", token, err)
	}
	if err != nil {
		log.Warn("token file lock skipped: lock file unavailable", "token_file", token, "error", err)
		return nil, nil //nolint:nilerr // see the function comment: not fatal
	}
	log.Info("token file lock acquired", "token_file", token, "lock_file", tokenlock.Path(token))
	return lock, nil
}
