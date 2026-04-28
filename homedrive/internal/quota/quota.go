// Package quota implements quota-aware push throttling for homedrive.
//
// It periodically polls the remote filesystem's quota and transitions
// through states (normal -> warned -> blocked) based on configurable
// thresholds with hysteresis to prevent flapping. Push workers are
// paused when quota is exhausted; pull continues unaffected.
package quota

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// QuotaInfo holds used/total byte counts returned by the remote filesystem.
type QuotaInfo struct {
	Used  int64
	Total int64
}

// UsedPercent returns the quota usage as a percentage (0-100).
// Returns 0 if Total is zero or negative (avoids division by zero).
func (q QuotaInfo) UsedPercent() float64 {
	if q.Total <= 0 {
		return 0
	}
	return float64(q.Used) / float64(q.Total) * 100
}

// RemoteFS is the subset of the rclone client interface needed by the
// quota monitor. Defined locally so this package compiles independently
// of internal/rcloneclient.
type RemoteFS interface {
	Quota(ctx context.Context) (QuotaInfo, error)
}

// Publisher is the subset of the MQTT publisher interface needed to emit
// quota warning and exhaustion events. Defined locally so this package
// compiles independently of internal/mqtt.
type Publisher interface {
	PublishJSON(topic string, payload any) error
	Topic(parts ...string) string
}

// PushController allows the quota monitor to pause and resume push
// workers without depending on the syncer package directly.
type PushController interface {
	PausePush()
	ResumePush()
}

// State represents the current quota state of the monitor.
type State string

const (
	// StateNormal means quota usage is below the warning threshold.
	StateNormal State = "normal"
	// StateWarned means quota usage is above warn_pct but below stop_push_pct.
	StateWarned State = "warned"
	// StateBlocked means quota usage is at or above stop_push_pct; pushes are paused.
	StateBlocked State = "quota_blocked"
)

// Config holds the quota monitor's tunable parameters.
type Config struct {
	// PollInterval is how often to poll Quota(). Default 5 minutes.
	PollInterval time.Duration
	// WarnPct is the usage percentage above which a warning event is emitted.
	// Default 90.
	WarnPct float64
	// StopPushPct is the usage percentage at or above which push workers are
	// paused. Default 99.
	StopPushPct float64
	// HysteresisPct is the percentage below StopPushPct at which pushes
	// resume after being blocked. Default is StopPushPct - 5 (i.e. 94).
	HysteresisPct float64
	// DryRun when true polls quota and logs transitions but does not
	// actually pause or resume push workers.
	DryRun bool
}

// DefaultConfig returns the default quota configuration per PLAN.md.
func DefaultConfig() Config {
	return Config{
		PollInterval:  5 * time.Minute,
		WarnPct:       90,
		StopPushPct:   99,
		HysteresisPct: 94,
		DryRun:        false,
	}
}

// Snapshot is a point-in-time view of the quota monitor's state, exposed
// to the /status HTTP endpoint.
type Snapshot struct {
	State       State     `json:"state"`
	UsedBytes   int64     `json:"used_bytes"`
	TotalBytes  int64     `json:"total_bytes"`
	UsedPercent float64   `json:"used_percent"`
	LastPoll    time.Time `json:"last_poll"`
	LastError   string    `json:"last_error,omitempty"`
}

// Monitor polls the remote filesystem quota and manages push throttling.
type Monitor struct {
	remote RemoteFS
	pub    Publisher
	push   PushController
	cfg    Config
	log    *slog.Logger

	mu        sync.RWMutex
	state     State
	lastQuota QuotaInfo
	lastPoll  time.Time
	lastErr   error
}

// NewMonitor creates a quota monitor. The publisher and push controller
// may be nil if the features are not wired yet (events and pause/resume
// are skipped gracefully).
func NewMonitor(remote RemoteFS, pub Publisher, push PushController, cfg Config, log *slog.Logger) *Monitor {
	if log == nil {
		log = slog.Default()
	}
	return &Monitor{
		remote: remote,
		pub:    pub,
		push:   push,
		cfg:    cfg,
		log:    log,
		state:  StateNormal,
	}
}

// State returns the current quota state snapshot.
func (m *Monitor) State() Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	errStr := ""
	if m.lastErr != nil {
		errStr = m.lastErr.Error()
	}
	return Snapshot{
		State:       m.state,
		UsedBytes:   m.lastQuota.Used,
		TotalBytes:  m.lastQuota.Total,
		UsedPercent: m.lastQuota.UsedPercent(),
		LastPoll:    m.lastPoll,
		LastError:   errStr,
	}
}

// Run starts the polling loop. It blocks until the context is cancelled.
func (m *Monitor) Run(ctx context.Context) error {
	// Poll immediately on start, then on the configured interval.
	m.poll(ctx)

	ticker := time.NewTicker(m.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			m.poll(ctx)
		}
	}
}

// Poll performs a single quota check and state transition. Exported for
// testing; production code should use Run.
func (m *Monitor) Poll(ctx context.Context) {
	m.poll(ctx)
}

func (m *Monitor) poll(ctx context.Context) {
	qi, err := m.remote.Quota(ctx)

	m.mu.Lock()
	defer m.mu.Unlock()

	m.lastPoll = time.Now()
	if err != nil {
		m.lastErr = fmt.Errorf("quota poll failed: %w", err)
		m.log.Error("quota poll failed", "error", err)
		return
	}
	m.lastErr = nil
	m.lastQuota = qi

	pct := qi.UsedPercent()
	prev := m.state
	next := m.evaluate(pct, prev)

	if next != prev {
		m.transition(prev, next, pct)
	}
}

// evaluate determines the next state given the current usage percentage
// and previous state. It implements the hysteresis logic: once blocked,
// the monitor stays blocked until usage drops below HysteresisPct.
func (m *Monitor) evaluate(pct float64, prev State) State {
	switch {
	case pct >= m.cfg.StopPushPct:
		return StateBlocked
	case prev == StateBlocked && pct >= m.cfg.HysteresisPct:
		// Still above hysteresis threshold; stay blocked.
		return StateBlocked
	case pct >= m.cfg.WarnPct:
		return StateWarned
	default:
		return StateNormal
	}
}

func (m *Monitor) transition(prev, next State, pct float64) {
	m.state = next

	m.log.Info("quota state transition",
		"op", "quota_transition",
		"from", string(prev),
		"to", string(next),
		"used_percent", pct,
		"dry_run", m.cfg.DryRun,
	)

	switch next {
	case StateWarned:
		m.emitEvent("quota.warning", pct)
		// If we were blocked and dropped to warned, resume pushes.
		if prev == StateBlocked {
			m.resumePush()
		}
	case StateBlocked:
		m.emitEvent("quota.exhausted", pct)
		m.pausePush()
	case StateNormal:
		// If transitioning from blocked or warned back to normal, resume.
		if prev == StateBlocked {
			m.resumePush()
		}
	}
}

func (m *Monitor) pausePush() {
	if m.cfg.DryRun {
		m.log.Info("dry-run: would pause push workers",
			"op", "quota_pause_push",
		)
		return
	}
	if m.push != nil {
		m.push.PausePush()
	}
}

func (m *Monitor) resumePush() {
	if m.cfg.DryRun {
		m.log.Info("dry-run: would resume push workers",
			"op", "quota_resume_push",
		)
		return
	}
	if m.push != nil {
		m.push.ResumePush()
	}
}

func (m *Monitor) emitEvent(eventType string, pct float64) {
	if m.pub == nil {
		return
	}
	topic := m.pub.Topic("events", eventType)
	payload := map[string]any{
		"ts":           time.Now().UTC().Format(time.RFC3339),
		"type":         eventType,
		"used_percent": pct,
	}
	if err := m.pub.PublishJSON(topic, payload); err != nil {
		m.log.Error("failed to publish quota event",
			"error", err,
			"event_type", eventType,
		)
	}
}
