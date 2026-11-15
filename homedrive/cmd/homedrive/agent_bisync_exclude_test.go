package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/rcloneclient"
)

// TestAgent_BisyncHonoursWatcherExclude drives the production buildBisync
// wiring and checks watcher.exclude reaches bisync (issue #135 review).
func TestAgent_BisyncHonoursWatcherExclude(t *testing.T) {
	a, remote := newIntegrationTestAgent(t)
	a.cfg.Watcher.Exclude = []string{"**/.git/**"}
	a.buildBisync(noopPublisher{}, nil, nil)

	t0 := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	remote.mu.Lock()
	remote.files["proj/.git/HEAD"] = rcloneclient.RemoteObject{Path: "proj/.git/HEAD", ModTime: t0}
	remote.files["proj/ok.txt"] = rcloneclient.RemoteObject{Path: "proj/ok.txt", ModTime: t0}
	remote.mu.Unlock()
	local := filepath.Join(a.cfg.LocalRoot, "proj", ".git", "config")
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.bisync.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			t.Logf("bisync Run: %v", err)
		}
	}()
	if err := a.bisync.ForceRun(ctx); err != nil {
		t.Fatalf("ForceRun: %v", err)
	}

	pulled := filepath.Join(a.cfg.LocalRoot, "proj", "ok.txt")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(pulled); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("non-excluded remote file proj/ok.txt was not pulled")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(a.cfg.LocalRoot, "proj", ".git", "HEAD")); err == nil {
		t.Error("excluded remote file proj/.git/HEAD was pulled")
	}
	if remote.HasCopiedFrom(local) {
		t.Error("excluded local file proj/.git/config was pushed")
	}
}
