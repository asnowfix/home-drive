package syncer

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Guard for issue #146: a file pulled from Drive and then changed only on
// Drive seconds later is an ordinary remote update, not a conflict. The
// puller already requires both sides to have changed (detectConflict).
func TestPuller_RemoteOnlyChangeAfterPull_NoConflict(t *testing.T) {
	root := t.TempDir()
	t0 := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	const rel = "scratch/s1.txt"

	remote := newMockRemoteFS()
	store := newMockStore()
	p := NewPuller(
		PullerConfig{Interval: 30 * time.Second, LocalRoot: root, ConflictPolicy: PolicyNewerWins},
		remote, store, newMockAuditLogger(), newMockPublisher(),
		newDiscardLogger(), fixedClock(t0.Add(time.Minute)),
	)

	remote.startToken = "t1"
	for i, tok := range []string{"t1", "t2"} {
		obj := RemoteObject{
			Path: rel, Size: 10, MD5: []string{"m1", "m2"}[i],
			ModTime: t0.Add(time.Duration(i) * 5 * time.Second), RemoteID: "id-1",
		}
		remote.changes[tok] = Changes{
			Items:         []Change{{Path: rel, Object: &obj}},
			NextPageToken: "t" + string(rune('2'+i)),
		}
	}

	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatalf("first PollOnce: %v", err)
	}
	// The mock download stamps the file with the current wall-clock time;
	// real DownloadFile applies the remote mtime. Align it with the journal
	// so the test models "local untouched since the pull".
	e, ok := store.entries[rel]
	if !ok {
		t.Fatal("first pull did not journal the file")
	}
	writeTestFileWithMtime(t, filepath.Join(root, rel), "content-of-"+rel, e.LocalMtime)

	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatalf("second PollOnce: %v", err)
	}

	if mv := remote.getMoveCalls(); len(mv) != 0 {
		t.Errorf("unexpected remote rename: %v", mv)
	}
	for path := range store.entries {
		if strings.Contains(path, ".old.") {
			t.Errorf("unexpected conflict journal entry %s", path)
		}
	}
	if got := len(remote.downloadedFiles); got != 2 {
		t.Errorf("downloads = %d, want 2 (initial pull + plain update)", got)
	}
}
