package desktop

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Decided defaults (issue #176, design sections 8.3 and 12).
const (
	DefaultRemote            = "gdrive:"
	DefaultCacheMaxPercent   = 25
	DefaultCachePollInterval = 10 * time.Second
	DefaultPollInterval      = 30 * time.Second
	DefaultFlushTimeout      = 30 * time.Second
	DefaultMinFreeSpace      = 5 * gib
)

const gib int64 = 1 << 30

// ErrInvalidConfig is matched (errors.Is) by every *ConfigError.
var ErrInvalidConfig = errors.New("desktop: invalid configuration")

// ConfigError reports one invalid desktop.yaml key.
type ConfigError struct {
	Field string // dotted YAML key, e.g. "cache.max_size_percent"
	Msg   string
}

func (e *ConfigError) Error() string {
	return fmt.Sprintf("desktop: config %s: %s", e.Field, e.Msg)
}

// Is makes errors.Is(err, ErrInvalidConfig) true for every ConfigError.
func (e *ConfigError) Is(target error) bool { return target == ErrInvalidConfig }

func cfgErr(field, format string, args ...any) error {
	return &ConfigError{Field: field, Msg: fmt.Sprintf(format, args...)}
}

// Config is the content of desktop.yaml. Zero values mean "use the default":
// call Load or Config.ApplyDefaults before use.
type Config struct {
	// Remote is the rclone remote to mount, "name:" for the Drive root or
	// "name:sub/folder" for a subfolder.
	Remote string `yaml:"remote"`
	// MountPoint is where the filesystem is mounted (default ~/GoogleDrive).
	MountPoint string `yaml:"mount_point"`
	// TokenFile is the private rclone.conf holding the OAuth token.
	TokenFile string `yaml:"token_file"`
	// NASConfig is the NAS-mode config consulted for overlap checks. The
	// check is skipped when the file does not exist.
	NASConfig string `yaml:"nas_config"`
	// StateDir and RuntimeDir default to the XDG state and runtime dirs.
	StateDir   string `yaml:"state_dir"`
	RuntimeDir string `yaml:"runtime_dir"`
	// PollInterval is how often the VFS asks Drive for changes.
	PollInterval Duration    `yaml:"poll_interval"`
	Cache        CacheCfg    `yaml:"cache"`
	Shutdown     ShutdownCfg `yaml:"shutdown"`
}

// CacheCfg configures the VFS cache (cache mode is always "full").
type CacheCfg struct {
	// Dir is the cache directory (default $XDG_CACHE_HOME/home-drive).
	Dir string `yaml:"dir"`
	// MaxSize is an absolute cap ("200GiB"). When zero the cap is
	// MaxSizePercent of the filesystem holding Dir.
	MaxSize        ByteSize `yaml:"max_size"`
	MaxSizePercent int      `yaml:"max_size_percent"`
	// MaxAge is the age-based eviction limit; zero (default) disables it.
	MaxAge       Duration `yaml:"max_age"`
	MinFreeSpace ByteSize `yaml:"min_free_space"`
	PollInterval Duration `yaml:"poll_interval"`
}

// ShutdownCfg configures SIGTERM/SIGINT handling.
type ShutdownCfg struct {
	// FlushTimeout bounds the wait for queued and in-progress uploads.
	FlushTimeout Duration `yaml:"flush_timeout"`
}

// Duration is a time.Duration that unmarshals from "30s" style strings.
type Duration struct{ time.Duration }

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", n.Value, err)
	}
	d.Duration = v
	return nil
}

// ByteSize is a size in bytes that unmarshals from "5GiB", "512M" or a plain
// number of bytes. Suffixes are binary (K, KiB, KB are all 1024).
type ByteSize int64

var sizeUnits = []struct {
	suffix string
	mult   int64
}{
	{"TIB", 1 << 40}, {"GIB", 1 << 30}, {"MIB", 1 << 20}, {"KIB", 1 << 10},
	{"TB", 1 << 40}, {"GB", 1 << 30}, {"MB", 1 << 20}, {"KB", 1 << 10},
	{"T", 1 << 40}, {"G", 1 << 30}, {"M", 1 << 20}, {"K", 1 << 10}, {"B", 1},
}

// ParseByteSize parses a size such as "5GiB".
func ParseByteSize(s string) (ByteSize, error) {
	t := strings.ToUpper(strings.TrimSpace(s))
	mult := int64(1)
	for _, u := range sizeUnits {
		if strings.HasSuffix(t, u.suffix) {
			mult = u.mult
			t = strings.TrimSpace(strings.TrimSuffix(t, u.suffix))
			break
		}
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f*float64(mult) >= math.MaxInt64 {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	return ByteSize(f * float64(mult)), nil
}

// UnmarshalYAML implements yaml.Unmarshaler.
func (b *ByteSize) UnmarshalYAML(n *yaml.Node) error {
	v, err := ParseByteSize(n.Value)
	if err != nil {
		return err
	}
	*b = v
	return nil
}

// Load reads desktop.yaml at path (unknown keys are rejected), applies the
// defaults resolved from env and validates the result. A missing file is
// reported as an error wrapping os.ErrNotExist; callers decide whether that
// is fatal (see LoadOrDefault).
func Load(path string, env Env) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("desktop: read config %q: %w", path, err)
	}
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("desktop: parse config %q: %w: %w", path, ErrInvalidConfig, err)
	}
	c.ApplyDefaults(env)
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// LoadOrDefault is Load, except that a missing file yields the defaults when
// explicit is false (the user did not name a config file).
func LoadOrDefault(path string, env Env, explicit bool) (*Config, error) {
	c, err := Load(path, env)
	if err == nil || explicit || !errors.Is(err, os.ErrNotExist) {
		return c, err
	}
	c = &Config{}
	c.ApplyDefaults(env)
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// ApplyDefaults fills every unset key with its decided default. For the
// numeric and duration keys, 0 is "unset": it cannot be told apart from an
// omitted key, so an explicit 0 also selects the default. (Telling them apart
// would need pointer fields; not worth it while no key needs 0 to mean "off".)
func (c *Config) ApplyDefaults(env Env) {
	setStr(&c.Remote, DefaultRemote)
	setStr(&c.MountPoint, env.DefaultMountPoint())
	setStr(&c.TokenFile, env.DefaultTokenFile())
	setStr(&c.NASConfig, env.DefaultNASConfigPath())
	setStr(&c.StateDir, env.StateDir())
	setStr(&c.RuntimeDir, env.RuntimeDir())
	setStr(&c.Cache.Dir, env.CacheDir())
	if c.PollInterval.Duration == 0 {
		c.PollInterval.Duration = DefaultPollInterval
	}
	if c.Cache.MaxSizePercent == 0 {
		c.Cache.MaxSizePercent = DefaultCacheMaxPercent
	}
	if c.Cache.MinFreeSpace == 0 {
		c.Cache.MinFreeSpace = ByteSize(DefaultMinFreeSpace)
	}
	if c.Cache.PollInterval.Duration == 0 {
		c.Cache.PollInterval.Duration = DefaultCachePollInterval
	}
	if c.Shutdown.FlushTimeout.Duration == 0 {
		c.Shutdown.FlushTimeout.Duration = DefaultFlushTimeout
	}
	for _, p := range []*string{&c.MountPoint, &c.TokenFile, &c.NASConfig,
		&c.StateDir, &c.RuntimeDir, &c.Cache.Dir} {
		*p = expandHome(*p, env.Home)
	}
}

func setStr(dst *string, def string) {
	if strings.TrimSpace(*dst) == "" {
		*dst = def
	}
}

// expandHome expands a leading "~" or "~/" to home.
func expandHome(p, home string) string {
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

// Validate checks the (defaulted) configuration and returns a *ConfigError.
func (c *Config) Validate() error {
	if !strings.Contains(c.Remote, ":") {
		return cfgErr("remote", "%q must be an rclone remote such as \"gdrive:\" or \"gdrive:folder\"", c.Remote)
	}
	if strings.HasPrefix(c.Remote, ":") {
		return cfgErr("remote", "%q: connection-string remotes are not supported", c.Remote)
	}
	paths := []struct{ field, value string }{
		{"mount_point", c.MountPoint}, {"token_file", c.TokenFile},
		{"nas_config", c.NASConfig}, {"state_dir", c.StateDir},
		{"runtime_dir", c.RuntimeDir}, {"cache.dir", c.Cache.Dir},
	}
	for _, p := range paths {
		if !filepath.IsAbs(p.value) {
			return cfgErr(p.field, "%q must be an absolute path (or start with ~/)", p.value)
		}
	}
	return c.validateNumbers()
}

func (c *Config) validateNumbers() error {
	if c.Cache.MaxSizePercent < 1 || c.Cache.MaxSizePercent > 100 {
		return cfgErr("cache.max_size_percent", "must be between 1 and 100, got %d", c.Cache.MaxSizePercent)
	}
	if c.Cache.MaxSize < 0 || c.Cache.MinFreeSpace < 0 {
		return cfgErr("cache", "sizes must not be negative")
	}
	if c.Cache.MaxAge.Duration < 0 {
		return cfgErr("cache.max_age", "must not be negative")
	}
	for _, d := range []struct {
		field string
		v     time.Duration
	}{
		{"poll_interval", c.PollInterval.Duration},
		{"cache.poll_interval", c.Cache.PollInterval.Duration},
		{"shutdown.flush_timeout", c.Shutdown.FlushTimeout.Duration},
	} {
		if d.v <= 0 {
			return cfgErr(d.field, "must be positive, got %s", d.v)
		}
	}
	return nil
}
