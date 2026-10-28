package syncer

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var pushTestMtime = time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)

// pushFile pushes rel (content, mtime) through a real Syncer against the
// mock remote and returns the fixtures.
func pushFile(t *testing.T, rel, content string) (root string, remote *mockRemoteFS, store *mockStore) {
	t.Helper()
	root = t.TempDir()
	writeTestFileWithMtime(t, filepath.Join(root, filepath.FromSlash(rel)), content, pushTestMtime)
	remote = newMockRemoteFS()
	store = newMockStore()
	cfg := DefaultConfig()
	cfg.LocalRoot = root
	s := New(cfg, remote, store, newMockAuditLogger(), newMockPublisher(),
		newDiscardLogger(), WithSleepFunc(noSleep))
	s.handleEvent(context.Background(), Event{Path: rel, Op: OpCreate, At: pushTestMtime})
	if _, ok := remote.files[rel]; !ok {
		t.Fatalf("push did not create remote object %s", rel)
	}
	return root, remote, store
}

// pullBack feeds obj to a Puller sharing root/remote/store, as the Changes
// API reports it after an upload.
func pullBack(t *testing.T, root string, remote *mockRemoteFS, store *mockStore, obj RemoteObject) {
	t.Helper()
	remote.startToken = "t1"
	remote.changes["t1"] = Changes{
		Items:         []Change{{Path: obj.Path, Object: &obj}},
		NextPageToken: "t2",
	}
	p := NewPuller(
		PullerConfig{Interval: 30 * time.Second, LocalRoot: root, ConflictPolicy: PolicyNewerWins},
		remote, store, newMockAuditLogger(), newMockPublisher(),
		newDiscardLogger(), fixedClock(pushTestMtime.Add(time.Minute)),
	)
	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
}

func assertNoConflict(t *testing.T, remote *mockRemoteFS, store *mockStore) {
	t.Helper()
	if mv := remote.getMoveCalls(); len(mv) != 0 {
		t.Errorf("expected no remote rename (.old.N), got %v", mv)
	}
	if n := len(remote.downloadedFiles); n != 0 {
		t.Errorf("expected no download, got %d", n)
	}
	for p := range store.entries {
		if strings.Contains(p, ".old.") {
			t.Errorf("unexpected conflict journal entry %s", p)
		}
	}
}

func TestPush_PullOfOwnUploadIsNotConflict(t *testing.T) {
	for _, rel := range []string{"push-test.txt", "hd-test/push-test.txt"} {
		t.Run(rel, func(t *testing.T) {
			root, remote, store := pushFile(t, rel, "identical content\n")
			pullBack(t, root, remote, store, remote.files[rel])
			assertNoConflict(t, remote, store)
		})
	}
}

func TestPush_JournalsRemoteObjectAndLocalMtime(t *testing.T) {
	_, remote, store := pushFile(t, "a/b.txt", "hello")
	obj := remote.files["a/b.txt"]
	e, ok := store.entries["a/b.txt"]
	if !ok {
		t.Fatal("no journal entry for pushed file")
	}
	if !e.LocalMtime.Equal(pushTestMtime) {
		t.Errorf("LocalMtime = %v, want %v", e.LocalMtime, pushTestMtime)
	}
	if e.RemoteMD5 != obj.MD5 || e.RemoteMD5 == "" {
		t.Errorf("RemoteMD5 = %q, want %q", e.RemoteMD5, obj.MD5)
	}
	if !e.RemoteMtime.Equal(obj.ModTime) || e.RemoteID != obj.RemoteID {
		t.Errorf("remote state not journaled: %+v vs %+v", e, obj)
	}
	if e.LastOrigin != "local" {
		t.Errorf("LastOrigin = %q, want local", e.LastOrigin)
	}
}

// A pre-fix journal entry (path + LastSyncedAt only) must not turn a
// byte-identical remote copy into a conflict, but a remote copy with
// different content and the same mtime must still be one.
func TestPull_UnknownBaseline(t *testing.T) {
	tests := []struct {
		name         string
		local        string
		remote       string
		wantConflict bool
	}{
		{"identical_content", "same", "same", false},
		{"different_content_equal_mtime", "mine", "theirs", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, remote, store := pushFile(t, "f.txt", tt.local)
			// Simulate a journal written before the fix.
			store.entries["f.txt"] = JournalEntry{
				Path: "f.txt", LastSyncedAt: pushTestMtime, LastOrigin: "local",
			}
			// The remote object is what the other side holds.
			src := filepath.Join(t.TempDir(), "f.txt")
			writeTestFileWithMtime(t, src, tt.remote, pushTestMtime)
			obj, err := remote.CopyFile(context.Background(), src, "")
			if err != nil {
				t.Fatal(err)
			}
			pullBack(t, root, remote, store, obj)
			if tt.wantConflict {
				if len(remote.getMoveCalls()) == 0 {
					t.Error("genuine conflict not detected: no .old.N rename")
				}
				return
			}
			assertNoConflict(t, remote, store)
			if e := store.entries["f.txt"]; e.RemoteMD5 != obj.MD5 || e.LocalMtime.IsZero() {
				t.Errorf("baseline not adopted: %+v", e)
			}
		})
	}
}
