package syncer

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// issue #146: a one-sided change must be a plain push or pull, never a
// conflict; only a change on both sides since the last sync is one.

var classifyT0 = time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)

// localOldFiles lists relative paths of .old.<N> files under root.
func localOldFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.Contains(d.Name(), ".old.") {
			rel, _ := filepath.Rel(root, p)
			out = append(out, rel)
		}
		return err
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

// countOldCopies counts .old.<N> conflict losers created on either side.
func countOldCopies(t *testing.T, root string, remote *mockRemoteFS) int {
	t.Helper()
	n := len(localOldFiles(t, root))
	for _, m := range remote.getMoveCalls() {
		if strings.Contains(m[1], ".old.") {
			n++
		}
	}
	return n
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestBisync_Classify_WithJournalEntry(t *testing.T) {
	const rel = "f.txt"
	later := classifyT0.Add(time.Minute)
	tests := []struct {
		name          string
		localChanged  bool
		remoteChanged bool
		remoteNewer   bool // both-changed only: which side wins
		wantCopies    int
		wantDownloads int
		wantOld       int
	}{
		{name: "remote_only_change", remoteChanged: true, wantDownloads: 1},
		{name: "local_only_change", localChanged: true, wantCopies: 1},
		{name: "both_changed_remote_newer", localChanged: true, remoteChanged: true,
			remoteNewer: true, wantDownloads: 1, wantOld: 1},
		{name: "both_changed_local_newer", localChanged: true, remoteChanged: true,
			wantCopies: 1, wantOld: 1},
		{name: "neither_changed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			b, _, remote, journal, _, _ := newTestBisync(t, root, false)

			createLocalFile(t, root, rel, classifyT0)
			remote.Seed(rel, classifyT0, "m0")
			journal.Seed(JournalEntry{
				Path: rel, LocalMtime: classifyT0, RemoteMtime: classifyT0,
				RemoteMD5: "m0", LastOrigin: "remote",
			})
			if tc.localChanged {
				lt := later
				if tc.remoteChanged && tc.remoteNewer {
					lt = later.Add(-30 * time.Second)
				}
				if err := os.Chtimes(filepath.Join(root, rel), lt, lt); err != nil {
					t.Fatal(err)
				}
			}
			if tc.remoteChanged {
				rt := later
				if tc.localChanged && !tc.remoteNewer {
					rt = later.Add(-30 * time.Second)
				}
				remote.Seed(rel, rt, "m1")
			}

			b.execute(context.Background())

			if got := remote.CopyCount(); got != tc.wantCopies {
				t.Errorf("copies = %d, want %d", got, tc.wantCopies)
			}
			if got := len(remote.downloadedFiles); got != tc.wantDownloads {
				t.Errorf("downloads = %d, want %d", got, tc.wantDownloads)
			}
			if got := countOldCopies(t, root, remote); got != tc.wantOld {
				t.Errorf(".old.N copies = %d, want %d (moves %v, local %v)",
					got, tc.wantOld, remote.getMoveCalls(), localOldFiles(t, root))
			}
		})
	}
}

func TestBisync_Classify_RemoteOnlyUpdatesJournal(t *testing.T) {
	root := t.TempDir()
	b, _, remote, journal, _, _ := newTestBisync(t, root, false)
	createLocalFile(t, root, "f.txt", classifyT0)
	journal.Seed(JournalEntry{Path: "f.txt", LocalMtime: classifyT0,
		RemoteMtime: classifyT0, RemoteMD5: "m0", LastOrigin: "remote"})
	later := classifyT0.Add(time.Minute)
	remote.Seed("f.txt", later, "m1")

	b.execute(context.Background())

	e, err := journal.Get("f.txt")
	if err != nil || e == nil {
		t.Fatalf("journal entry missing: %v", err)
	}
	if e.RemoteMD5 != "m1" || !e.RemoteMtime.Equal(later) || e.LastOrigin != "remote" {
		t.Errorf("journal not updated: %+v", *e)
	}
	data, _ := os.ReadFile(filepath.Join(root, "f.txt"))
	if string(data) != "content-of-f.txt" {
		t.Errorf("local content = %q, want remote content", data)
	}
}

func TestBisync_Classify_NoJournalEntry(t *testing.T) {
	const rel = "f.txt"
	localMD5 := md5Hex("content-" + rel)
	tests := []struct {
		name       string
		remoteMD5  string
		remoteMt   time.Time
		wantOld    int
		wantAction bool // any copy or download
	}{
		{name: "equal_md5_different_mtime", remoteMD5: localMD5,
			remoteMt: classifyT0.Add(time.Hour)},
		{name: "within_tolerance_different_md5", remoteMD5: "other",
			remoteMt: classifyT0.Add(500 * time.Millisecond)},
		{name: "different_md5_different_mtime", remoteMD5: "other",
			remoteMt: classifyT0.Add(time.Hour), wantOld: 1, wantAction: true},
		{name: "no_remote_md5_different_mtime", remoteMD5: "",
			remoteMt: classifyT0.Add(time.Hour), wantOld: 1, wantAction: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			b, _, remote, _, _, _ := newTestBisync(t, root, false)
			createLocalFile(t, root, rel, classifyT0)
			remote.Seed(rel, tc.remoteMt, tc.remoteMD5)

			b.execute(context.Background())

			if got := countOldCopies(t, root, remote); got != tc.wantOld {
				t.Errorf(".old.N copies = %d, want %d", got, tc.wantOld)
			}
			acted := remote.CopyCount()+len(remote.downloadedFiles) > 0
			if acted != tc.wantAction {
				t.Errorf("acted = %v, want %v", acted, tc.wantAction)
			}
		})
	}
}

func TestBisync_Classify_BothChangedSameContent(t *testing.T) {
	root := t.TempDir()
	b, _, remote, journal, _, _ := newTestBisync(t, root, false)
	createLocalFile(t, root, "f.txt", classifyT0.Add(time.Minute))
	journal.Seed(JournalEntry{Path: "f.txt", LocalMtime: classifyT0,
		RemoteMtime: classifyT0, RemoteMD5: "m0", LastOrigin: "remote"})
	remote.Seed("f.txt", classifyT0.Add(2*time.Minute), md5Hex("content-f.txt"))

	b.execute(context.Background())

	if n := remote.CopyCount() + len(remote.downloadedFiles) + remote.MoveCount(); n != 0 {
		t.Errorf("identical content on both sides must be a no-op, got %d ops", n)
	}
}
