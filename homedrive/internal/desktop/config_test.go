package desktop

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "desktop.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoad_Defaults(t *testing.T) {
	c, err := Load(writeCfg(t, "{}\n"), testEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		name      string
		got, want any
	}{
		{"remote", c.Remote, "gdrive:"},
		{"mount", c.MountPoint, "/home/u/GoogleDrive"},
		{"token", c.TokenFile, "/home/u/.config/homedrive/rclone.conf"},
		{"cache dir", c.Cache.Dir, "/home/u/.cache/home-drive"},
		{"state dir", c.StateDir, "/home/u/.local/state/home-drive"},
		{"percent", c.Cache.MaxSizePercent, 25},
		{"min free", c.Cache.MinFreeSpace, ByteSize(5 << 30)},
		{"cache poll", c.Cache.PollInterval.Duration, 10 * time.Second},
		{"poll", c.PollInterval.Duration, 30 * time.Second},
		{"flush", c.Shutdown.FlushTimeout.Duration, 30 * time.Second},
		{"max age", c.Cache.MaxAge.Duration, time.Duration(0)},
	}
	for _, ck := range checks {
		if ck.got != ck.want {
			t.Errorf("%s = %v, want %v", ck.name, ck.got, ck.want)
		}
	}
}

func TestLoad_Overrides(t *testing.T) {
	body := `
remote: "gdrive:some-folder"
mount_point: ~/Drive
token_file: /scratch/rclone.conf
nas_config: /scratch/nas.yaml
state_dir: /scratch/state
runtime_dir: /scratch/run
poll_interval: 1m
cache:
  dir: /scratch/cache
  max_size: 200GiB
  max_age: 48h
  min_free_space: 512MiB
  poll_interval: 5s
shutdown:
  flush_timeout: 10s
`
	c, err := Load(writeCfg(t, body), testEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Remote != "gdrive:some-folder" || c.MountPoint != "/home/u/Drive" ||
		c.TokenFile != "/scratch/rclone.conf" || c.Cache.Dir != "/scratch/cache" ||
		c.StateDir != "/scratch/state" || c.RuntimeDir != "/scratch/run" {
		t.Errorf("path overrides not honoured: %+v", c)
	}
	if c.Cache.MaxSize != 200<<30 || c.Cache.MinFreeSpace != 512<<20 ||
		c.Cache.MaxAge.Duration != 48*time.Hour || c.PollInterval.Duration != time.Minute ||
		c.Shutdown.FlushTimeout.Duration != 10*time.Second {
		t.Errorf("value overrides not honoured: %+v", c)
	}
}

func TestLoad_Invalid(t *testing.T) {
	tests := []struct {
		name, body, field string
		wantInvalid       bool
	}{
		{"NoColonRemote", "remote: gdrive\n", "remote", true},
		{"ConnectionStringRemote", "remote: \":drive:\"\n", "remote", true},
		{"RelativeMountPoint", "mount_point: rel/dir\n", "mount_point", true},
		{"RelativeCacheDir", "cache:\n  dir: rel\n", "cache.dir", true},
		{"PercentTooHigh", "cache:\n  max_size_percent: 101\n", "cache.max_size_percent", true},
		{"NegativePoll", "poll_interval: -1s\n", "poll_interval", true},
		{"NegativeFlush", "shutdown:\n  flush_timeout: -5s\n", "shutdown.flush_timeout", true},
		{"NegativeMaxAge", "cache:\n  max_age: -1h\n", "cache.max_age", true},
		{"UnknownKey", "mountpoint: /x\n", "", true},
		{"BadDuration", "poll_interval: soon\n", "", true},
		{"BadSize", "cache:\n  max_size: lots\n", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeCfg(t, tc.body), testEnv(nil))
			if err == nil {
				t.Fatal("expected an error")
			}
			if tc.wantInvalid && !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("err = %v, want ErrInvalidConfig", err)
			}
			var ce *ConfigError
			if tc.field != "" && (!errors.As(err, &ce) || ce.Field != tc.field) {
				t.Fatalf("err = %v, want ConfigError for %q", err, tc.field)
			}
		})
	}
}

func TestLoadOrDefault_MissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.yaml")
	tests := []struct {
		name     string
		explicit bool
		wantErr  bool
	}{
		{"DefaultPathUsesDefaults", false, false},
		{"ExplicitPathFails", true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := LoadOrDefault(missing, testEnv(nil), tc.explicit)
			if tc.wantErr {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("err = %v, want ErrNotExist", err)
				}
				return
			}
			if err != nil || c.Remote != DefaultRemote {
				t.Fatalf("c=%+v err=%v", c, err)
			}
		})
	}
}

func TestParseByteSize_Cases(t *testing.T) {
	tests := []struct {
		in      string
		want    ByteSize
		wantErr bool
	}{
		{"5GiB", 5 << 30, false},
		{"5G", 5 << 30, false},
		{"512 MB", 512 << 20, false},
		{"1.5K", 1536, false},
		{"1024", 1024, false},
		{"2TiB", 2 << 40, false},
		{"-1G", 0, true},
		{"abc", 0, true},
		{"nan", 0, true},
		{"inf", 0, true},
		{"99999999TiB", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseByteSize(tc.in)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("got %d, %v; want %d (err=%v)", got, err, tc.want, tc.wantErr)
			}
		})
	}
}
