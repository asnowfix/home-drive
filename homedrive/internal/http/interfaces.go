// Package http provides the loopback HTTP control endpoint for homedrive,
// serving /status, /pause, /resume, /resync, /reload, /healthz, and /metrics.
package http

import "context"

// StatusInfo holds the current agent state returned by GET /status.
type StatusInfo struct {
	State          string `json:"state"`
	Version        string `json:"version"`
	PendingUp      int    `json:"pending_up"`
	PendingDown    int    `json:"pending_down"`
	LastPush       string `json:"last_push,omitempty"`
	LastPull       string `json:"last_pull,omitempty"`
	QuotaUsedPct   int    `json:"quota_used_pct"`
	Conflicts24h   int    `json:"conflicts_24h"`
	BytesUp24h     int64  `json:"bytes_up_24h"`
	BytesDown24h   int64  `json:"bytes_down_24h"`
	DryRun         bool   `json:"dry_run"`
	UptimeSeconds  int64  `json:"uptime_seconds"`
}

// ComponentHealth reports the health of a single subsystem.
type ComponentHealth struct {
	Name    string `json:"name"`
	Healthy bool   `json:"healthy"`
	Message string `json:"message,omitempty"`
}

// HealthResult aggregates health from all checked components.
type HealthResult struct {
	Healthy    bool              `json:"healthy"`
	Components []ComponentHealth `json:"components"`
}

// Pausable is implemented by components that can be paused and resumed
// (e.g., the watcher and push workers).
type Pausable interface {
	Pause(ctx context.Context) error
	Resume(ctx context.Context) error
}

// Resyncable is implemented by the syncer to trigger an immediate bisync.
type Resyncable interface {
	ForceResync(ctx context.Context) error
}

// Reloadable is implemented by the config loader to hot-reload configuration.
type Reloadable interface {
	Reload(ctx context.Context) error
}

// StatusProvider returns the current agent status.
type StatusProvider interface {
	Status(ctx context.Context) (StatusInfo, error)
}

// HealthChecker returns the health of all monitored components.
type HealthChecker interface {
	Healthz(ctx context.Context) (HealthResult, error)
}
