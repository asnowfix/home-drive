// Package desktop implements the logic of `homedrive mount`, the desktop
// mode of homedrive: an on-demand Google Drive mount built on rclone's vfs
// and cmd/mount2 (design: homedrive/docs/design/desktop-mode.md).
//
// This package, with homedrive/cmd/homedrive/mount*.go, is the only code
// allowed to import the desktop-only rclone packages (see the
// homedrive-rclone-import skill and the CI grep in
// .github/workflows/homedrive.yml).
package desktop

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/asnowfix/home-drive/homedrive/internal/tokenlock"
)

// appDir is the directory name used under the XDG cache, state and runtime
// roots. The config directory keeps the older name "homedrive" because it is
// shared with NAS mode (design section 10).
const (
	appDir       = "home-drive"
	configAppDir = "homedrive"
)

// Env abstracts the process environment so path resolution is testable.
type Env struct {
	// Getenv returns the value of an environment variable ("" if unset).
	Getenv func(string) string
	// Home is the user's home directory.
	Home string
}

// OSEnv returns the real process environment.
func OSEnv() (Env, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Env{}, fmt.Errorf("desktop: find home directory: %w", err)
	}
	return Env{Getenv: os.Getenv, Home: home}, nil
}

// xdgBase returns the value of the XDG variable name when it is set to an
// absolute path, otherwise home joined with fallback. The XDG Base Directory
// specification says a relative value must be ignored.
func (e Env) xdgBase(name string, fallback ...string) string {
	if v := e.Getenv(name); v != "" && filepath.IsAbs(v) {
		return v
	}
	return filepath.Join(append([]string{e.Home}, fallback...)...)
}

// The four resolvers below are the single place where a default location is
// decided. Every one is overridable from desktop.yaml.

// ConfigDir is the homedrive configuration directory,
// $XDG_CONFIG_HOME/homedrive (fallback ~/.config/homedrive). It holds
// desktop.yaml and the default token file.
func (e Env) ConfigDir() string {
	return filepath.Join(e.xdgBase("XDG_CONFIG_HOME", ".config"), configAppDir)
}

// CacheDir is the default VFS cache directory, $XDG_CACHE_HOME/home-drive
// (fallback ~/.cache/home-drive). rclone adds vfs/<remote>/ and
// vfsMeta/<remote>/ below it and writes nothing outside it.
func (e Env) CacheDir() string {
	return filepath.Join(e.xdgBase("XDG_CACHE_HOME", ".cache"), appDir)
}

// StateDir is the default state directory, $XDG_STATE_HOME/home-drive
// (fallback ~/.local/state/home-drive).
func (e Env) StateDir() string {
	return filepath.Join(e.xdgBase("XDG_STATE_HOME", ".local", "state"), appDir)
}

// RuntimeDir is the default runtime directory, $XDG_RUNTIME_DIR/home-drive.
// The XDG specification defines no fallback for XDG_RUNTIME_DIR, so when it
// is unset or not absolute the runtime directory lives under the state
// directory (StateDir()/run), which is per-user and survives no assumption
// about tmpfs.
func (e Env) RuntimeDir() string {
	if v := e.Getenv("XDG_RUNTIME_DIR"); v != "" && filepath.IsAbs(v) {
		return filepath.Join(v, appDir)
	}
	return filepath.Join(e.StateDir(), "run")
}

// DefaultConfigPath is the default desktop.yaml location.
func (e Env) DefaultConfigPath() string {
	return filepath.Join(e.ConfigDir(), "desktop.yaml")
}

// DefaultTokenFile is the default token (rclone.conf) location.
func (e Env) DefaultTokenFile() string {
	return filepath.Join(e.ConfigDir(), "rclone.conf")
}

// DefaultNASConfigPath is where NAS mode keeps its configuration
// (config.yaml next to desktop.yaml).
func (e Env) DefaultNASConfigPath() string {
	return filepath.Join(e.ConfigDir(), "config.yaml")
}

// DefaultMountPoint is the default mount point, ~/GoogleDrive.
func (e Env) DefaultMountPoint() string {
	return filepath.Join(e.Home, "GoogleDrive")
}

// RcloneDefaultConfig mirrors where rclone itself looks for rclone.conf when
// no explicit path is given ($XDG_CONFIG_HOME/rclone/rclone.conf). It is used
// only to compare against NAS mode's rclone_config when that is unset.
func (e Env) RcloneDefaultConfig() string {
	return tokenlock.RcloneDefaultPath(e.Getenv("XDG_CONFIG_HOME"), e.Home)
}
