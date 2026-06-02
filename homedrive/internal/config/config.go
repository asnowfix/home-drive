// Package config loads homedrive configuration from YAML files,
// /etc/default/homedrive environment variables, and CLI flags.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
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
	Policy          string `yaml:"policy"`
	OldSuffixFormat string `yaml:"old_suffix_format"`
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

// Load reads and parses the YAML config file at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %q: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config: parse %q: %w", path, err)
	}
	return &cfg, nil
}
