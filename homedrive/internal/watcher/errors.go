package watcher

import "errors"

// Sentinel errors for the watcher package.
var (
	// ErrInvalidConfig indicates a configuration value is missing or invalid.
	ErrInvalidConfig = errors.New("watcher: invalid configuration")
)
