package main

import (
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
// A config that cannot be loaded is not this function's business: it returns
// a nil lock and no error so newAgent reports the real config problem.
func acquireTokenLock(configPath string, log *slog.Logger) (*tokenlock.Lock, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, nil //nolint:nilerr // newAgent re-loads and reports the error
	}
	token := cfg.RcloneConfig
	if token == "" {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return nil, nil //nolint:nilerr // no token path to lock without a home directory
		}
		token = tokenlock.RcloneDefaultPath(os.Getenv("XDG_CONFIG_HOME"), home)
	}
	lock, err := tokenlock.Acquire(token)
	if err != nil {
		return nil, fmt.Errorf("token file %q: %w", token, err)
	}
	log.Info("token file lock acquired", "token_file", token, "lock_file", tokenlock.Path(token))
	return lock, nil
}
