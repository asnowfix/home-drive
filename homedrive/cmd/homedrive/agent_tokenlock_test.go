package main

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/asnowfix/home-drive/homedrive/internal/tokenlock"
)

func TestAcquireTokenLock_Cases(t *testing.T) {
	cases := []struct {
		name       string
		yaml       func(token string) string
		preHold    bool
		wantNil    bool
		wantHeld   bool
		wantLockAt func(dir, token string) string
	}{
		{
			name:       "ExplicitRcloneConfig",
			yaml:       func(token string) string { return "remote: \"gdrive:\"\nrclone_config: " + token + "\n" },
			wantLockAt: func(_, token string) string { return token + ".lock" },
		},
		{
			name:     "HeldByDesktopMode",
			yaml:     func(token string) string { return "remote: \"gdrive:\"\nrclone_config: " + token + "\n" },
			preHold:  true,
			wantHeld: true,
		},
		{
			name:    "UnloadableConfigDefersToNewAgent",
			yaml:    func(string) string { return "conflict: [not, a, mapping]\n" },
			wantNil: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			token := filepath.Join(dir, "rclone.conf")
			cfgPath := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(cfgPath, []byte(tc.yaml(token)), 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.preHold {
				other, err := tokenlock.Acquire(token)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = other.Release() }()
			}
			lock, err := acquireTokenLock(cfgPath, slog.Default())
			if tc.wantHeld {
				if !errors.Is(err, tokenlock.ErrHeld) {
					t.Fatalf("err = %v, want tokenlock.ErrHeld", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("acquireTokenLock: %v", err)
			}
			defer func() { _ = lock.Release() }()
			if tc.wantNil {
				if lock != nil {
					t.Fatal("want nil lock for an unloadable config")
				}
				return
			}
			if _, err := os.Stat(tc.wantLockAt(dir, token)); err != nil {
				t.Fatalf("lock file missing: %v", err)
			}
		})
	}
}

func TestAcquireTokenLock_DefaultRclonePath(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("remote: \"gdrive:\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireTokenLock(cfgPath, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Release() }()
	want := filepath.Join(xdg, "rclone", "rclone.conf.lock")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected lock at rclone's default location %q: %v", want, err)
	}
}
