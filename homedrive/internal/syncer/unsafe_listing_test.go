package syncer

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// issue #161: remote paths from the bisync listing and from pulled changes
// are untrusted; none may be joined onto local_root unvalidated.

// strayFiles returns every regular file under dir, relative to it.
func strayFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// assertUnsafeEntryWarned checks the structured log holds one Warn naming the
// dropped path and no file contents (the mock remote writes "content-of-...").
func assertUnsafeEntryWarned(t *testing.T, logs, bad string) {
	t.Helper()
	found := false
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec["level"] == "WARN" && rec["path"] == bad &&
			rec["msg"] == "bisync skipping remote entry with an unsafe path" {
			found = true
		}
	}
	if !found {
		t.Errorf("no Warn for dropped path %q in logs:\n%s", bad, logs)
	}
	if strings.Contains(logs, "content-of-") {
		t.Errorf("logs contain file contents:\n%s", logs)
	}
}

func TestBisync_UnsafeListingPathsNotPulled(t *testing.T) {
	tests := []struct {
		name string
		bad  string
	}{
		{"parent_escape", "../x"},
		{"inner_escape", "a/../../x"},
		{"inner_dotdot_stays_inside", "a/../b"},
		{"absolute", "/abs"},
		{"dot_component", "a/./b"},
		{"trailing_slash", "a/"},
		{"backslash", "a\\b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sandbox := t.TempDir()
			root := filepath.Join(sandbox, "root")
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatal(err)
			}
			b, _, remote, _, _, _ := newTestBisync(t, root, false)
			logs := &threadSafeBuffer{}
			b.log = slog.New(slog.NewJSONHandler(logs, nil))
			remote.Seed(tt.bad, deleteT0, "m1")
			remote.Seed("ok.txt", deleteT0, "m2")

			b.execute(context.Background())

			if got := strayFiles(t, root); len(got) != 1 || got[0] != "ok.txt" {
				t.Errorf("files under root = %v, want only the valid sibling ok.txt", got)
			}
			if got := strayFiles(t, sandbox); len(got) != 1 {
				t.Errorf("files under sandbox = %v, want only root/ok.txt", got)
			}
			assertUnsafeEntryWarned(t, logs.String(), tt.bad)
			for _, c := range remote.downloadedFiles {
				if c.RemotePath == tt.bad {
					t.Errorf("unsafe path %q was downloaded", tt.bad)
				}
			}
		})
	}
}

// An unsafe listing entry is not a deletion signal: a local file that sits at
// exactly the dropped path (legal locally, e.g. a backslash in the name) still
// exists on Drive, so it must be neither deleted locally nor re-pushed.
func TestBisync_UnsafeListingEntryNotADeletion(t *testing.T) {
	const rel = "d\\f.txt"
	root := t.TempDir()
	b, _, remote, journal, _, _ := newTestBisync(t, root, false)
	createLocalFile(t, root, rel, deleteT0)
	seedBaseline(journal, rel)
	remote.Seed(rel, deleteT0, "m0")

	b.execute(context.Background())

	if _, err := os.Lstat(filepath.Join(root, rel)); err != nil {
		t.Errorf("local file deleted because the listing could not represent it: %v", err)
	}
	if n := len(remote.getDeleteCalls()) + remote.CopyCount() + len(remote.downloadedFiles); n != 0 {
		t.Errorf("remote operations = %d, want 0", n)
	}
	if !journal.Exists(rel) {
		t.Error("journal entry dropped")
	}
}

func TestPuller_UnsafeFileChangeNotDownloaded(t *testing.T) {
	tests := []struct {
		name string
		bad  string
	}{
		{"parent_escape", "../x"},
		{"inner_escape", "a/../../x"},
		{"inner_dotdot_stays_inside", "a/../b"},
		{"absolute", "/abs"},
		{"backslash", "a\\b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sandbox := t.TempDir()
			root := filepath.Join(sandbox, "root")
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatal(err)
			}
			remote := newMockRemoteFS()
			good := RemoteObject{Path: "ok.txt", ModTime: subtreeT0, MD5: "m", Size: 1}
			badObj := RemoteObject{Path: tt.bad, ModTime: subtreeT0, MD5: "m", Size: 1}
			runPullOn(t, remote, root, newMockStore(), []Change{
				{Path: tt.bad, Object: &badObj},
				{Path: "ok.txt", Object: &good},
			})

			if got := strayFiles(t, root); len(got) != 1 || got[0] != "ok.txt" {
				t.Errorf("files under root = %v, want only ok.txt", got)
			}
			if got := strayFiles(t, sandbox); len(got) != 1 {
				t.Errorf("files under sandbox = %v, want only root/ok.txt", got)
			}
		})
	}
}
