package desktop

import (
	"path/filepath"
	"testing"
)

func testEnv(vars map[string]string) Env {
	return Env{Getenv: func(k string) string { return vars[k] }, Home: "/home/u"}
}

func TestEnvPaths_XDG(t *testing.T) {
	tests := []struct {
		name                          string
		vars                          map[string]string
		config, cache, state, runtime string
	}{
		{
			name:    "Unset",
			vars:    nil,
			config:  "/home/u/.config/homedrive",
			cache:   "/home/u/.cache/home-drive",
			state:   "/home/u/.local/state/home-drive",
			runtime: "/home/u/.local/state/home-drive/run",
		},
		{
			name: "AllAbsolute",
			vars: map[string]string{
				"XDG_CONFIG_HOME": "/x/c", "XDG_CACHE_HOME": "/x/k",
				"XDG_STATE_HOME": "/x/s", "XDG_RUNTIME_DIR": "/run/user/1000",
			},
			config:  "/x/c/homedrive",
			cache:   "/x/k/home-drive",
			state:   "/x/s/home-drive",
			runtime: "/run/user/1000/home-drive",
		},
		{
			name: "RelativeIgnored",
			vars: map[string]string{
				"XDG_CONFIG_HOME": "rel/c", "XDG_CACHE_HOME": "rel/k",
				"XDG_STATE_HOME": "rel/s", "XDG_RUNTIME_DIR": "rel/r",
			},
			config:  "/home/u/.config/homedrive",
			cache:   "/home/u/.cache/home-drive",
			state:   "/home/u/.local/state/home-drive",
			runtime: "/home/u/.local/state/home-drive/run",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := testEnv(tc.vars)
			if got := e.ConfigDir(); got != tc.config {
				t.Errorf("ConfigDir = %q, want %q", got, tc.config)
			}
			if got := e.CacheDir(); got != tc.cache {
				t.Errorf("CacheDir = %q, want %q", got, tc.cache)
			}
			if got := e.StateDir(); got != tc.state {
				t.Errorf("StateDir = %q, want %q", got, tc.state)
			}
			if got := e.RuntimeDir(); got != tc.runtime {
				t.Errorf("RuntimeDir = %q, want %q", got, tc.runtime)
			}
		})
	}
}

func TestEnvPaths_Defaults(t *testing.T) {
	e := testEnv(map[string]string{"XDG_CONFIG_HOME": "/x/c"})
	want := map[string]string{
		e.DefaultConfigPath():    "/x/c/homedrive/desktop.yaml",
		e.DefaultTokenFile():     "/x/c/homedrive/rclone.conf",
		e.DefaultNASConfigPath(): "/x/c/homedrive/config.yaml",
		e.DefaultMountPoint():    "/home/u/GoogleDrive",
		e.RcloneDefaultConfig():  "/x/c/rclone/rclone.conf",
	}
	for got, w := range want {
		if filepath.ToSlash(got) != w {
			t.Errorf("got %q, want %q", got, w)
		}
	}
}

func TestOSEnv_Home(t *testing.T) {
	t.Setenv("HOME", "/tmp/somehome")
	e, err := OSEnv()
	if err != nil {
		t.Fatal(err)
	}
	if e.Home != "/tmp/somehome" {
		t.Errorf("Home = %q", e.Home)
	}
}
