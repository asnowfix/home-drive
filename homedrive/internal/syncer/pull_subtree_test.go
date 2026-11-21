package syncer

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// issue #152: a Drive folder deletion must remove its synced descendants at
// any depth from the folder entry alone. Drive is not known to report an
// entry per descendant, and the ID-to-path cache that resolves such entries
// is lost on restart, so these tests send only the folder's change (which
// is also what remains when child changes carry unknown IDs and are dropped).

var subtreeT0 = time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)

type subtreeFile struct {
	path      string
	journaled bool // journal entry with a full baseline
	modified  bool // mtime moved since the journal fingerprint
	noRemote  bool // journal entry lacks any remote state (loser entry)
}

func TestPuller_FolderDelete_RemovesSyncedDescendants(t *testing.T) {
	tests := []struct {
		name     string
		files    []subtreeFile
		dirs     []string
		change   string
		exclude  []string
		dryRun   bool
		wantGone []string
		wantKept []string
		// wantJournal lists paths whose journal entry must remain.
		wantJournal []string
		wantConfl   []string
		// wantAudit, when non-nil, is the exact set of pull_delete audit paths.
		wantAudit []string
	}{
		{
			name:      "depth1",
			files:     []subtreeFile{{path: "d/a.txt", journaled: true}},
			change:    "d",
			wantGone:  []string{"d/a.txt", "d"},
			wantAudit: []string{"d/a.txt", "d"},
		},
		{
			name: "depth2",
			files: []subtreeFile{
				{path: "d/a.txt", journaled: true},
				{path: "d/s/b.txt", journaled: true},
			},
			change:   "d",
			wantGone: []string{"d/a.txt", "d/s/b.txt", "d/s", "d"},
		},
		{
			name: "depth3",
			files: []subtreeFile{
				{path: "d/s/t/c.txt", journaled: true},
				{path: "d/s/b.txt", journaled: true},
			},
			change:   "d",
			wantGone: []string{"d/s/t/c.txt", "d/s/t", "d/s/b.txt", "d/s", "d"},
		},
		{
			name:     "nested_folder_change_only",
			files:    []subtreeFile{{path: "d/s/t/c.txt", journaled: true}, {path: "d/x.txt", journaled: true}},
			change:   "d/s",
			wantGone: []string{"d/s/t/c.txt", "d/s"},
			wantKept: []string{"d/x.txt", "d"},
			// d/x.txt is outside the deleted folder and keeps its entry.
			wantJournal: []string{"d/x.txt"},
		},
		{
			name: "unjournaled_content_kept",
			files: []subtreeFile{
				{path: "d/synced.txt", journaled: true},
				{path: "d/s/unsynced.txt"},
			},
			change:   "d",
			wantGone: []string{"d/synced.txt"},
			wantKept: []string{"d/s/unsynced.txt", "d/s", "d"},
		},
		{
			name: "entry_without_remote_state_kept",
			files: []subtreeFile{
				{path: "d/loser.old.1", journaled: true, noRemote: true},
			},
			change:      "d",
			wantKept:    []string{"d/loser.old.1", "d"},
			wantJournal: []string{"d/loser.old.1"},
		},
		{
			name: "locally_modified_kept_with_conflict",
			files: []subtreeFile{
				{path: "d/edited.txt", journaled: true, modified: true},
				{path: "d/s/clean.txt", journaled: true},
			},
			change:    "d",
			wantGone:  []string{"d/s/clean.txt", "d/s"},
			wantKept:  []string{"d/edited.txt", "d"},
			wantConfl: []string{"d/edited.txt"},
		},
		{
			name: "excluded_descendant_kept",
			files: []subtreeFile{
				{path: "d/.git/HEAD", journaled: true},
				{path: "d/a.txt", journaled: true},
			},
			change:      "d",
			exclude:     []string{"**/.git/**"},
			wantGone:    []string{"d/a.txt"},
			wantKept:    []string{"d/.git/HEAD"},
			wantJournal: []string{"d/.git/HEAD"},
		},
		{
			// A directory the user created, that never held a synced file,
			// is not ours to prune; the folder holding it therefore stays.
			name:     "user_empty_dir_kept",
			files:    []subtreeFile{{path: "d/a.txt", journaled: true}},
			dirs:     []string{"d/empty/deeper"},
			change:   "d",
			wantGone: []string{"d/a.txt"},
			wantKept: []string{"d/empty/deeper", "d/empty", "d"},
		},
		{
			// An empty directory under an excluded tree is never pruned.
			name:     "excluded_empty_dir_kept",
			files:    []subtreeFile{{path: "d/a.txt", journaled: true}},
			dirs:     []string{"d/.git/objects"},
			exclude:  []string{"**/.git/**", "**/.git"},
			change:   "d",
			wantGone: []string{"d/a.txt"},
			wantKept: []string{"d/.git/objects", "d"},
		},
		{
			name:        "dry_run_deletes_nothing",
			files:       []subtreeFile{{path: "d/a.txt", journaled: true}},
			change:      "d",
			dryRun:      true,
			wantKept:    []string{"d/a.txt", "d"},
			wantJournal: []string{"d/a.txt"},
			wantAudit:   []string{"d/a.txt", "d"},
		},
		{
			name:     "sibling_with_common_name_prefix_untouched",
			files:    []subtreeFile{{path: "d/a.txt", journaled: true}, {path: "dd/a.txt", journaled: true}},
			change:   "d",
			wantGone: []string{"d/a.txt", "d"},
			wantKept: []string{"dd/a.txt"}, wantJournal: []string{"dd/a.txt"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			st := newMockStore()
			for _, d := range tt.dirs {
				if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			for _, f := range tt.files {
				createLocalFile(t, root, f.path, subtreeT0)
				if !f.journaled {
					continue
				}
				e := JournalEntry{
					Path: f.path, LocalMtime: subtreeT0, RemoteMtime: subtreeT0,
					RemoteMD5: "m", LastOrigin: "remote",
				}
				if f.noRemote {
					e.RemoteMtime, e.RemoteMD5 = time.Time{}, ""
				}
				_ = st.Put(context.Background(), e)
				if f.modified {
					mt := subtreeT0.Add(time.Hour)
					if err := os.Chtimes(filepath.Join(root, f.path), mt, mt); err != nil {
						t.Fatal(err)
					}
				}
			}

			remote := newMockRemoteFS()
			remote.startToken = "start-1"
			remote.changes["start-1"] = Changes{
				Items:         []Change{{Path: tt.change, Deleted: true}},
				NextPageToken: "token-2",
			}
			audit, pub := newMockAuditLogger(), newMockPublisher()
			p := NewPuller(
				PullerConfig{Remover: osRemover{},
					Interval: 30 * time.Second, LocalRoot: root,
					ConflictPolicy: PolicyNewerWins, Exclude: tt.exclude,
					DryRun: tt.dryRun,
				},
				remote, st, audit, pub, newDiscardLogger(), fixedClock(subtreeT0),
			)
			if err := p.PollOnce(context.Background()); err != nil {
				t.Fatalf("PollOnce: %v", err)
			}

			for _, g := range tt.wantGone {
				if _, err := os.Lstat(filepath.Join(root, g)); !os.IsNotExist(err) {
					t.Errorf("%s still exists (err=%v)", g, err)
				}
			}
			for _, k := range tt.wantKept {
				if _, err := os.Lstat(filepath.Join(root, k)); err != nil {
					t.Errorf("%s was removed: %v", k, err)
				}
			}
			for _, f := range tt.files {
				_, has, _ := st.Get(context.Background(), f.path)
				want := false
				for _, j := range tt.wantJournal {
					want = want || j == f.path
				}
				if f.journaled && has != want {
					t.Errorf("journal entry %s present = %v, want %v", f.path, has, want)
				}
			}
			checkSubtreeAudit(t, tt.dryRun, tt.wantConfl, tt.wantAudit, audit, pub)
		})
	}
}

// checkSubtreeAudit asserts the conflict audit paths and events, that every
// pull_delete audit line carries the dry_run flag of the run, and, when
// wantAudit is non-nil, the exact set of pull_delete paths (one line each).
func checkSubtreeAudit(
	t *testing.T, dryRun bool, wantConfl, wantAudit []string,
	audit *mockAuditLogger, pub *mockPublisher,
) {
	t.Helper()
	var confl, deletes []string
	for _, e := range audit.getEntries() {
		switch e.Op {
		case "conflict":
			confl = append(confl, e.Path)
			if e.Resolution != deleteConflictResolution {
				t.Errorf("conflict resolution = %q", e.Resolution)
			}
		case "pull_delete":
			deletes = append(deletes, e.Path)
			if e.DryRun != dryRun {
				t.Errorf("pull_delete %s dry_run = %v, want %v", e.Path, e.DryRun, dryRun)
			}
		}
	}
	slices.Sort(confl)
	slices.Sort(wantConfl)
	if !slices.Equal(confl, wantConfl) {
		t.Errorf("conflict audit paths = %v, want %v", confl, wantConfl)
	}
	if wantAudit != nil {
		slices.Sort(deletes)
		slices.Sort(wantAudit)
		if !slices.Equal(deletes, wantAudit) {
			t.Errorf("pull_delete audit paths = %v, want %v", deletes, wantAudit)
		}
	}
	if got := len(pub.getMessagesByTopic(pub.Topic("events", "conflict.detected"))); got != len(wantConfl) {
		t.Errorf("conflict.detected events = %d, want %d", got, len(wantConfl))
	}
}

// Each synced descendant removed from a deleted folder is audited once, as
// pull_delete, so the log says what a folder deletion took with it.
func TestPuller_FolderDelete_AuditsEachDescendant(t *testing.T) {
	root := t.TempDir()
	st := newMockStore()
	for _, f := range []string{"d/a.txt", "d/s/b.txt"} {
		createLocalFile(t, root, f, subtreeT0)
		_ = st.Put(context.Background(), JournalEntry{
			Path: f, LocalMtime: subtreeT0, RemoteMtime: subtreeT0, RemoteMD5: "m",
		})
	}
	remote := newMockRemoteFS()
	remote.startToken = "start-1"
	remote.changes["start-1"] = Changes{
		Items: []Change{{Path: "d", Deleted: true}}, NextPageToken: "token-2",
	}
	audit := newMockAuditLogger()
	p := NewPuller(
		PullerConfig{Remover: osRemover{}, Interval: 30 * time.Second, LocalRoot: root},
		remote, st, audit, newMockPublisher(), newDiscardLogger(), fixedClock(subtreeT0),
	)
	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	got := map[string]int{}
	for _, e := range audit.getEntries() {
		if e.Op == "pull_delete" {
			got[e.Path]++
		}
	}
	for _, want := range []string{"d/a.txt", "d/s/b.txt", "d"} {
		if got[want] != 1 {
			t.Errorf("pull_delete audit lines for %s = %d, want 1 (all: %v)", want, got[want], got)
		}
	}
}
