package syncer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// T1 (#152 review 2): a delete change whose path has an empty, "." or ".."
// component, a backslash or a leading slash must never reach the local
// filesystem, even when a journaled file sits at the path it would resolve
// to.
func TestPuller_UnsafeDeletePathIgnored(t *testing.T) {
	tests := []struct {
		name   string
		change string
		victim string // path relative to the sandbox dir that must survive
	}{
		{"escapes_root", "../outside.txt", "outside.txt"},
		{"inner_dotdot", "d/../f.txt", "root/f.txt"},
		{"inner_dot", "d/./f.txt", "root/d/f.txt"},
		{"parent_via_trailing_dotdot", "d/sub/..", "root/d/keep.txt"},
		{"backslash", "d\\f.txt", "root/d\\f.txt"},
		{"leading_slash", "/f.txt", "root/f.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sandbox := t.TempDir()
			root := filepath.Join(sandbox, "root")
			st := newMockStore()
			for _, rel := range []string{"outside.txt", "root/f.txt", "root/d/f.txt", "root/d/keep.txt", "root/d\\f.txt"} {
				createLocalFile(t, sandbox, rel, subtreeT0)
			}
			for _, rel := range []string{"f.txt", "d/f.txt", "d/keep.txt", "d/sub/x.txt", "../outside.txt", "d\\f.txt"} {
				putBaseline(t, st, rel, "")
			}

			audit, _ := runPull(t, root, st, []Change{{Path: tt.change, Deleted: true}})

			if _, err := os.Lstat(filepath.Join(sandbox, tt.victim)); err != nil {
				t.Errorf("%s removed by a delete of %q: %v", tt.victim, tt.change, err)
			}
			if n := len(audit.getEntries()); n != 0 {
				t.Errorf("audit lines = %d, want 0", n)
			}
			if _, ok, _ := st.Get(context.Background(), "f.txt"); !ok {
				t.Error("journal entry dropped")
			}
		})
	}
}

// T2 (#152 review 2): the user deleted x locally (the push trashed ID1 on
// Drive and journaled x without baseline or ID), then recreated x before the
// push. The Trashed change for ID1 must not delete the new file.
func TestPuller_FileDelete_RecreatedAfterLocalDeleteKept(t *testing.T) {
	tests := []struct {
		name    string
		journal *JournalEntry
	}{
		{"nil_object_push_entry", &JournalEntry{Path: "x", LastOrigin: "local"}},
		{"no_journal_entry", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			st := newMockStore()
			createLocalFile(t, root, "x", subtreeT0)
			if tt.journal != nil {
				_ = st.Put(context.Background(), *tt.journal)
			}

			runPull(t, root, st, []Change{{Path: "x", Deleted: true, ID: "ID1"}})

			if _, err := os.Lstat(filepath.Join(root, "x")); err != nil {
				t.Errorf("recreated file deleted: %v", err)
			}
		})
	}
}
