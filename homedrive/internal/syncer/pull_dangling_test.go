package syncer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var errDanglingTest = fmt.Errorf("mock download: %w", ErrDanglingShortcut)

func danglingChange(path, id string, mtime time.Time) Change {
	return Change{Path: path, Object: &RemoteObject{
		Path: path, Size: 1, MD5: "md5-" + path, ModTime: mtime, RemoteID: id,
	}}
}

type danglingFixture struct {
	root   string
	remote *mockRemoteFS
	store  *mockStore
	pub    *mockPublisher
	audit  *mockAuditLogger
	puller *Puller
}

func newDanglingFixture(t *testing.T) *danglingFixture {
	t.Helper()
	f := &danglingFixture{
		root:   t.TempDir(),
		remote: newMockRemoteFS(),
		store:  newMockStore(),
		pub:    newMockPublisher(),
		audit:  newMockAuditLogger(),
	}
	f.remote.startToken = "t1"
	f.puller = NewPuller(
		PullerConfig{Interval: time.Second, LocalRoot: f.root},
		f.remote, f.store, f.audit, f.pub, slog.Default(),
		fixedClock(time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC)),
	)
	return f
}

func (f *danglingFixture) failures() int {
	return len(f.pub.getMessagesByTopic(f.pub.Topic("events", "pull.failure")))
}

func (f *danglingFixture) downloads(path string) int {
	n := 0
	for _, d := range f.remote.downloadedFiles {
		if d.RemotePath == path {
			n++
		}
	}
	return n
}

func TestPuller_DanglingShortcut_CycleContinues(t *testing.T) {
	f := newDanglingFixture(t)
	now := time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC)
	f.remote.downloadErr["link"] = errDanglingTest
	f.remote.changes["t1"] = Changes{
		Items: []Change{
			danglingChange("a.txt", "id-a", now),
			danglingChange("link", "id-link", now),
			danglingChange("b.txt", "id-b", now),
		},
		NextPageToken: "t2",
	}

	if err := f.puller.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v, want nil for a dangling shortcut", err)
	}

	for _, name := range []string{"a.txt", "b.txt"} {
		if _, err := os.Stat(filepath.Join(f.root, name)); err != nil {
			t.Errorf("%s not downloaded: %v", name, err)
		}
		if _, ok, _ := f.store.Get(context.Background(), name); !ok {
			t.Errorf("no journal entry for %s", name)
		}
	}
	if _, ok, _ := f.store.Get(context.Background(), "link"); ok {
		t.Error("journal entry written for dangling shortcut; bisync could treat it as a local deletion")
	}
	if tok, _ := f.store.GetPageToken(context.Background()); tok != "t2" {
		t.Errorf("page token = %q, want t2", tok)
	}
	if n := f.failures(); n != 1 {
		t.Errorf("pull.failure events = %d, want 1", n)
	}
}

func TestPuller_DanglingShortcut_SecondSightingSkipped(t *testing.T) {
	f := newDanglingFixture(t)
	now := time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC)
	f.remote.downloadErr["link"] = errDanglingTest
	ch := danglingChange("link", "id-link", now)
	f.remote.changes["t1"] = Changes{Items: []Change{ch}, NextPageToken: "t2"}
	f.remote.changes["t2"] = Changes{Items: []Change{ch}, NextPageToken: "t3"}

	for i := 0; i < 2; i++ {
		if err := f.puller.PollOnce(context.Background()); err != nil {
			t.Fatalf("PollOnce #%d: %v", i+1, err)
		}
	}
	if n := f.downloads("link"); n != 1 {
		t.Errorf("DownloadFile calls = %d, want 1", n)
	}
	if n := f.failures(); n != 1 {
		t.Errorf("pull.failure events = %d, want 1", n)
	}
	if n := len(f.audit.getEntries()); n != 1 {
		t.Errorf("audit lines = %d, want 1", n)
	}
}

func TestPuller_DanglingShortcut_ChangedObjectRetried(t *testing.T) {
	now := time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		next Change
	}{
		{"different mtime", danglingChange("link", "id-link", now.Add(time.Hour))},
		{"different remote id", danglingChange("link", "id-other", now)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newDanglingFixture(t)
			f.remote.downloadErr["link"] = errDanglingTest
			f.remote.changes["t1"] = Changes{
				Items: []Change{danglingChange("link", "id-link", now)}, NextPageToken: "t2",
			}
			f.remote.changes["t2"] = Changes{Items: []Change{tc.next}, NextPageToken: "t3"}

			for i := 0; i < 2; i++ {
				if err := f.puller.PollOnce(context.Background()); err != nil {
					t.Fatalf("PollOnce #%d: %v", i+1, err)
				}
			}
			if n := f.downloads("link"); n != 2 {
				t.Errorf("DownloadFile calls = %d, want 2", n)
			}
		})
	}
}

func TestPuller_DanglingShortcut_OtherErrorStillFails(t *testing.T) {
	f := newDanglingFixture(t)
	now := time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC)
	boom := errors.New("boom")
	f.remote.downloadErr["bad"] = boom
	f.remote.changes["t1"] = Changes{
		Items: []Change{danglingChange("bad", "id-bad", now)}, NextPageToken: "t2",
	}

	err := f.puller.PollOnce(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("PollOnce error = %v, want it to wrap boom", err)
	}
	if n := f.failures(); n != 1 {
		t.Errorf("pull.failure events = %d, want 1", n)
	}
}
