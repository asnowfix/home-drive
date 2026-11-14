package syncer

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/rcloneclient"
)

// memClock adapts a fixed instant to rcloneclient.Clock.
type memClock struct{ t time.Time }

func (c memClock) Now() time.Time { return c.t }

// newNestedBisync wires Bisync to the real rcloneclient.MemFS (not the
// syncer's mockRemoteFS, whose List ignores its dir argument and so hides
// non-recursive listing behaviour; issue #135).
func newNestedBisync(t *testing.T, root string, now time.Time) (*Bisync, *rcloneclient.MemFS, *mockJournal) {
	t.Helper()
	remote := rcloneclient.NewMemFS(rcloneclient.WithClock(memClock{now}))
	journal := newMockJournal()
	b, _ := NewBisync(BisyncOpts{
		Config:  BisyncConfig{Interval: time.Hour, LocalRoot: root},
		Remote:  remote,
		Journal: journal,
		Clock:   newMockClock(now),
		Mu:      &sync.RWMutex{},
	})
	return b, remote, journal
}

func TestBisync_NestedFiles_Recursive(t *testing.T) {
	t0 := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Hour)

	tests := []struct {
		name  string
		setup func(t *testing.T, root string, r *rcloneclient.MemFS, j *mockJournal)
		check func(t *testing.T, root string, r *rcloneclient.MemFS)
	}{
		{
			name: "in sync on both sides: nothing re-uploaded",
			setup: func(t *testing.T, root string, r *rcloneclient.MemFS, j *mockJournal) {
				createLocalFile(t, root, "a/b/f.txt", t0)
				r.Seed("a/b/f.txt", t0, "m1")
				j.Seed(JournalEntry{Path: "a/b/f.txt", LocalMtime: t0, RemoteMtime: t0, RemoteMD5: "m1"})
			},
			check: func(t *testing.T, _ string, r *rcloneclient.MemFS) {
				if got := r.Files()["a/b/f.txt"]; got.MD5 != "m1" {
					t.Errorf("remote file was re-uploaded: md5=%q, want m1", got.MD5)
				}
			},
		},
		{
			name: "both sides, remote newer: remote wins, local kept as .old",
			setup: func(t *testing.T, root string, r *rcloneclient.MemFS, j *mockJournal) {
				createLocalFile(t, root, "a/f.txt", t0)
				r.Seed("a/f.txt", t1, "m2")
				j.Seed(JournalEntry{Path: "a/f.txt", LocalMtime: t0, RemoteMtime: t0, RemoteMD5: "m1"})
			},
			check: func(t *testing.T, root string, r *rcloneclient.MemFS) {
				if got := r.Files()["a/f.txt"]; got.MD5 != "m2" {
					t.Errorf("newer remote version overwritten: md5=%q, want m2", got.MD5)
				}
				data, err := os.ReadFile(filepath.Join(root, "a", "f.txt"))
				if err != nil || string(data) != "content-of-a/f.txt" {
					t.Errorf("local not updated from remote: %q, %v", data, err)
				}
			},
		},
		{
			name: "remote only in sub-directory: pulled",
			setup: func(t *testing.T, root string, r *rcloneclient.MemFS, _ *mockJournal) {
				r.Seed("x/y/z.txt", t0, "m3")
			},
			check: func(t *testing.T, root string, _ *rcloneclient.MemFS) {
				if _, err := os.Stat(filepath.Join(root, "x", "y", "z.txt")); err != nil {
					t.Errorf("remote-only nested file not pulled: %v", err)
				}
			},
		},
		{
			name: "local only in sub-directory: pushed",
			setup: func(t *testing.T, root string, _ *rcloneclient.MemFS, _ *mockJournal) {
				createLocalFile(t, root, "p/q.txt", t0)
			},
			check: func(t *testing.T, _ string, r *rcloneclient.MemFS) {
				if _, ok := r.Files()["p/q.txt"]; !ok {
					t.Error("local-only nested file not pushed")
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			b, remote, journal := newNestedBisync(t, root, t0.Add(2*time.Hour))
			tt.setup(t, root, remote, journal)
			b.execute(context.Background())
			tt.check(t, root, remote)
		})
	}
}
