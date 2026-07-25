package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/oldsuffix"
)

func writeConfig(t *testing.T, yamlBody string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(yamlBody), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoad_ConflictRetentionDefaults(t *testing.T) {
	// The retention: block (and repair_chains) are omitted entirely;
	// applyDefaults must fill in the documented defaults.
	path := writeConfig(t, `
local_root: /tmp/sync
remote: "drive:"
conflict:
  policy: newer_wins
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Conflict.Retention.MaxPerFile != 3 {
		t.Errorf("MaxPerFile = %d, want 3", cfg.Conflict.Retention.MaxPerFile)
	}
	if cfg.Conflict.Retention.MaxAge.Duration != 0 {
		t.Errorf("MaxAge = %v, want 0", cfg.Conflict.Retention.MaxAge.Duration)
	}
	if cfg.Conflict.Retention.SweepInterval.Duration != 24*time.Hour {
		t.Errorf("SweepInterval = %v, want 24h", cfg.Conflict.Retention.SweepInterval.Duration)
	}
	if cfg.Conflict.RepairChains == nil || !*cfg.Conflict.RepairChains {
		t.Errorf("RepairChains = %v, want a non-nil pointer to true", cfg.Conflict.RepairChains)
	}
}

func TestLoad_ConflictRetentionExplicitValuesPreserved(t *testing.T) {
	path := writeConfig(t, `
local_root: /tmp/sync
remote: "drive:"
conflict:
  policy: newer_wins
  retention:
    max_per_file: 5
    max_age: 48h
    sweep_interval: 1h
  repair_chains: false
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Conflict.Retention.MaxPerFile != 5 {
		t.Errorf("MaxPerFile = %d, want 5", cfg.Conflict.Retention.MaxPerFile)
	}
	if cfg.Conflict.Retention.MaxAge.Duration != 48*time.Hour {
		t.Errorf("MaxAge = %v, want 48h", cfg.Conflict.Retention.MaxAge.Duration)
	}
	if cfg.Conflict.Retention.SweepInterval.Duration != time.Hour {
		t.Errorf("SweepInterval = %v, want 1h", cfg.Conflict.Retention.SweepInterval.Duration)
	}
	if cfg.Conflict.RepairChains == nil || *cfg.Conflict.RepairChains {
		t.Errorf("RepairChains = %v, want a non-nil pointer to false", cfg.Conflict.RepairChains)
	}
}

func TestLoad_InvalidOldSuffixFormat(t *testing.T) {
	path := writeConfig(t, `
local_root: /tmp/sync
remote: "drive:"
conflict:
  policy: newer_wins
  old_suffix_format: ".old"
`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load: expected error for invalid old_suffix_format, got nil")
	}
	if !errors.Is(err, oldsuffix.ErrBadFormat) {
		t.Errorf("error = %v, want wrapped oldsuffix.ErrBadFormat", err)
	}
}

func TestLoad_MaxPerFileClamped(t *testing.T) {
	tests := []struct {
		name           string
		maxPerFile     string
		wantMaxPerFile int
	}{
		{name: "zero clamped to 1", maxPerFile: "0", wantMaxPerFile: 1},
		{name: "negative clamped to 1", maxPerFile: "-5", wantMaxPerFile: 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfig(t, `
local_root: /tmp/sync
remote: "drive:"
conflict:
  policy: newer_wins
  retention:
    max_per_file: `+tc.maxPerFile+`
    sweep_interval: 2h
`)
			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.Conflict.Retention.MaxPerFile != tc.wantMaxPerFile {
				t.Errorf("MaxPerFile = %d, want %d", cfg.Conflict.Retention.MaxPerFile, tc.wantMaxPerFile)
			}
			// A partially-configured block must not have its other
			// explicit fields silently overridden by the "whole block
			// omitted" default path.
			if cfg.Conflict.Retention.SweepInterval.Duration != 2*time.Hour {
				t.Errorf("SweepInterval = %v, want 2h (explicit value preserved)", cfg.Conflict.Retention.SweepInterval.Duration)
			}
		})
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err == nil {
		t.Fatal("Load: expected error for missing file, got nil")
	}
}
