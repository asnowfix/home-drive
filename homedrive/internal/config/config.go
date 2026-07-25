// Package config loads homedrive configuration from YAML files,
// /etc/default/homedrive environment variables, and CLI flags.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/asnowfix/home-drive/homedrive/internal/oldsuffix"
)

// Config is the root homedrive configuration.
type Config struct {
	LocalRoot    string        `yaml:"local_root"`
	Remote       string        `yaml:"remote"`
	RcloneConfig string        `yaml:"rclone_config"`
	Watcher      WatcherConfig `yaml:"watcher"`
	Push         PushConfig    `yaml:"push"`
	Pull         PullConfig    `yaml:"pull"`
	Conflict     ConflictCfg   `yaml:"conflict"`
	State        StateConfig   `yaml:"state"`
	HTTP         HTTPConfig    `yaml:"http"`
	MQTT         MQTTConfig    `yaml:"mqtt"`
	Logging      LoggingConfig `yaml:"logging"`
	Quota        QuotaConfig   `yaml:"quota"`
	DryRun       bool          `yaml:"dry_run"`
}

// WatcherConfig configures the fsnotify watcher.
type WatcherConfig struct {
	Debounce            Duration `yaml:"debounce"`
	DirRenamePairWindow Duration `yaml:"dir_rename_pair_window"`
	Exclude             []string `yaml:"exclude"`
}

// PushConfig configures the push worker pool.
type PushConfig struct {
	Workers int         `yaml:"workers"`
	Retry   RetryConfig `yaml:"retry"`
}

// RetryConfig configures exponential backoff for push retries.
type RetryConfig struct {
	MaxAttempts    int      `yaml:"max_attempts"`
	InitialBackoff Duration `yaml:"initial_backoff"`
	MaxBackoff     Duration `yaml:"max_backoff"`
}

// PullConfig configures the pull loop.
type PullConfig struct {
	ChangesAPIInterval Duration `yaml:"changes_api_interval"`
	BisyncInterval     Duration `yaml:"bisync_interval"`
}

// ConflictCfg configures conflict resolution.
type ConflictCfg struct {
	Policy          string       `yaml:"policy"`
	OldSuffixFormat string       `yaml:"old_suffix_format"`
	Retention       RetentionCfg `yaml:"retention"`

	// RepairChains controls whether the one-time repair pass (PLAN.md
	// §11.5) collapses any pre-existing nested .old.<N> chains on the
	// first bisync pass after upgrade. nil means enabled (the default);
	// a pointer is used specifically so "omitted" (enabled) can be told
	// apart from an explicit "repair_chains: false" (disabled).
	RepairChains *bool `yaml:"repair_chains"`
}

// RetentionCfg bounds how many .old.<N> conflict losers are kept. See
// PLAN.md §11.5.
type RetentionCfg struct {
	// MaxPerFile caps how many .old.<N> siblings are kept per base file;
	// the oldest (by LastSyncedAt) beyond this count are pruned. Defaults
	// to 3 when the whole retention: block is omitted; an explicit
	// non-positive value is clamped up to 1 rather than treated as
	// "unlimited" (see Config.applyDefaults).
	MaxPerFile int `yaml:"max_per_file"`

	// MaxAge expires losers older than this regardless of MaxPerFile.
	// Zero (the default) means never expire by age -- age-based deletion
	// can remove a user's only surviving copy of a genuine edit purely
	// because time passed, so it is opt-in.
	MaxAge Duration `yaml:"max_age"`

	// SweepInterval controls how often the periodic full-journal sweep
	// (piggybacked on the bisync tick) runs. Defaults to 24h when the
	// whole retention: block is omitted.
	SweepInterval Duration `yaml:"sweep_interval"`
}

// StateConfig configures BoltDB and audit log paths.
type StateConfig struct {
	Path     string `yaml:"path"`
	AuditLog string `yaml:"audit_log"`
}

// HTTPConfig configures the control endpoint.
type HTTPConfig struct {
	Listen  string `yaml:"listen"`
	Metrics bool   `yaml:"metrics"`
	// AuthToken, if set, is required as a Bearer token on every request to
	// the control endpoint (see PLAN.md §12). Loopback-only, no-token setups
	// remain unauthenticated for zero-config local use; a non-loopback
	// Listen address without AuthToken causes the server to refuse to
	// start (fail closed).
	AuthToken string `yaml:"auth_token"`
}

// MQTTConfig configures the MQTT publisher.
type MQTTConfig struct {
	Enabled           bool     `yaml:"enabled"`
	Broker            string   `yaml:"broker"`
	ClientIDPrefix    string   `yaml:"client_id_prefix"`
	BaseTopic         string   `yaml:"base_topic"`
	HADiscoveryPrefix string   `yaml:"ha_discovery_prefix"`
	PublishInterval   Duration `yaml:"publish_interval"`
	QoS               byte     `yaml:"qos"`
}

// LoggingConfig configures log level and format.
type LoggingConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

// QuotaConfig configures Drive quota thresholds.
type QuotaConfig struct {
	WarnPct     float64 `yaml:"warn_pct"`
	StopPushPct float64 `yaml:"stop_push_pct"`
}

// Duration wraps time.Duration to support YAML unmarshaling of "2s", "500ms".
type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	dur, err := time.ParseDuration(value.Value)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", value.Value, err)
	}
	d.Duration = dur
	return nil
}

// defaultMaxPerFile and defaultSweepInterval are applied by applyDefaults
// when the whole conflict.retention: block is omitted from YAML. See
// PLAN.md §11.5.
const (
	defaultMaxPerFile    = 3
	defaultSweepInterval = 24 * time.Hour
)

// Load reads and parses the YAML config file at path, validates
// conflict.old_suffix_format, and applies defaults for any omitted
// fields covered by applyDefaults.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %q: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config: parse %q: %w", path, err)
	}
	if _, err := oldsuffix.New(cfg.Conflict.OldSuffixFormat); err != nil {
		return nil, fmt.Errorf("config: conflict.old_suffix_format: %w", err)
	}
	cfg.applyDefaults()
	return &cfg, nil
}

// applyDefaults fills in zero-valued fields of Config with their
// documented defaults. Currently scoped to ConflictCfg.Retention and
// ConflictCfg.RepairChains -- the rest of Config has always relied on
// each consumer (agent.go, syncer constructors) applying its own
// zero-value fallback, and widening applyDefaults to cover those too is
// out of scope for this change.
//
// RetentionCfg is a plain value struct (per PLAN.md §11.5), so YAML
// gives no way to distinguish "conflict.retention: was omitted
// entirely" from "conflict.retention: was present with every field left
// at its zero value" -- both unmarshal identically. This method treats
// them the same: if the whole block is the zero value, the documented
// defaults (max_per_file: 3, sweep_interval: 24h) are applied. If the
// block is only partially configured, an explicit non-positive
// max_per_file is clamped up to 1 rather than silently becoming
// "unlimited" (see PLAN.md §11.5); every other field is left as given,
// including an explicit sweep_interval: 0s (periodic sweep disabled).
func (c *Config) applyDefaults() {
	if c.Conflict.Retention == (RetentionCfg{}) {
		c.Conflict.Retention.MaxPerFile = defaultMaxPerFile
		c.Conflict.Retention.SweepInterval = Duration{Duration: defaultSweepInterval}
	} else if c.Conflict.Retention.MaxPerFile <= 0 {
		c.Conflict.Retention.MaxPerFile = 1
	}

	if c.Conflict.RepairChains == nil {
		enabled := true
		c.Conflict.RepairChains = &enabled
	}
}
