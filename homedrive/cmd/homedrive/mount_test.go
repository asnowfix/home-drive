package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/asnowfix/home-drive/homedrive/internal/desktop"
)

func TestNewMountCmd_Flags(t *testing.T) {
	cmd := newMountCmd()
	if cmd.Use != "mount" {
		t.Fatalf("Use = %q", cmd.Use)
	}
	f := cmd.Flags().Lookup("config")
	if f == nil {
		t.Fatal("missing --config flag")
	}
	if filepath.Base(f.DefValue) != "desktop.yaml" {
		t.Errorf("default config = %q, want .../desktop.yaml", f.DefValue)
	}
}

func TestRunMount_ConfigErrors(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("cache:\n  max_size_percent: 500\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		path string
		want error
	}{
		{"ExplicitMissingFile", filepath.Join(dir, "missing.yaml"), os.ErrNotExist},
		{"InvalidValue", bad, desktop.ErrInvalidConfig},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := newRootCmd(nil)
			root.SetArgs([]string{"mount", "--config", tc.path})
			err := root.Execute()
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}
