package syncer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/trash"
)

// Issue #155: sync's local deletions go to the freedesktop.org trash. Under
// the packaged system unit the home trash is on another mount (EXDEV) and the
// trash ends up in <local_root>/.Trash-<uid>, inside the synced tree, where
// it must never be synced.

const trashUID = 1000

// trashEnv is a Trasher whose home trash is a temp dir on another "mount"
// (renames into it fail with EXDEV when exdev is set) and whose local root
// is a mount point in the fake mount table, like the NAS under the sandbox.
type trashEnv struct {
	root, home string
	tr         *trash.Trasher
}

func newTrashEnv(t *testing.T, homeErr, anyErr error) *trashEnv {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &trashEnv{root: filepath.Join(base, "root"), home: filepath.Join(base, "home", "Trash")}
	if err := os.MkdirAll(e.root, 0o755); err != nil {
		t.Fatal(err)
	}
	e.tr = trash.New(trash.Config{
		HomeTrash: e.home,
		UID:       trashUID,
		Now:       func() time.Time { return deleteT0 },
		Rename: func(oldp, newp string) error {
			if anyErr != nil {
				return &os.LinkError{Op: "rename", Old: oldp, New: newp, Err: anyErr}
			}
			if homeErr != nil && strings.HasPrefix(newp, e.home+string(filepath.Separator)) {
				return &os.LinkError{Op: "rename", Old: oldp, New: newp, Err: homeErr}
			}
			return os.Rename(oldp, newp)
		},
		Mountinfo: func() ([]byte, error) {
			return []byte("1 0 8:1 / / ro - ext4 /dev/sda1 ro\n2 1 8:1 /d " + e.root + " rw - ext4 /dev/sda1 rw\n"), nil
		},
		Log: newDiscardLogger(),
	})
	return e
}

func (e *trashEnv) trashed(rel string) string {
	return filepath.Join(e.root, ".Trash-1000", "files", rel)
}

func fileExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// A file deleted on Drive is moved to the topdir trash under the sandbox
// (EXDEV), and the next bisync pass neither pushes it back nor plans its
// deletion on Drive: the trashed copy under local_root is never synced.
func TestBisync_TrashedFile_NeverResynced(t *testing.T) {
	const rel = "d/f.txt"
	te := newTrashEnv(t, syscall.EXDEV, nil)
	b, _, remote, journal, _, _ := newTestBisync(t, te.root, false)
	b.cfg.Remover = te.tr
	createLocalFile(t, te.root, rel, deleteT0)
	seedBaseline(journal, rel)

	b.execute(context.Background())

	if fileExists(filepath.Join(te.root, rel)) || !fileExists(te.trashed("f.txt")) {
		t.Fatalf("file not moved to <root>/.Trash-%d (trashed exists: %v)", trashUID, fileExists(te.trashed("f.txt")))
	}
	if journal.Exists(rel) {
		t.Error("journal entry kept after a successful deletion")
	}
	// Second pass: with the trash tree on disk next to the journaled state.
	// Also seed a Drive-side object inside the trash dir, which must be
	// ignored in the remote listing too.
	remote.Seed(".Trash-1000/files/f.txt", deleteT0, "m0")
	b.execute(context.Background())

	if remote.CopyCount() != 0 || len(remote.downloadedFiles) != 0 || len(remote.getDeleteCalls()) != 0 {
		t.Errorf("trash content was synced: copies=%d downloads=%d deletes=%d",
			remote.CopyCount(), len(remote.downloadedFiles), len(remote.getDeleteCalls()))
	}
	if !fileExists(te.trashed("f.txt")) {
		t.Error("trashed file disappeared on the second pass")
	}
}

// When neither trash works the file and its journal entry stay, nothing is
// deleted, and the same deletion is planned again on the next pass.
func TestBisync_TrashFailure_KeepsFileAndJournal(t *testing.T) {
	const rel = "d/f.txt"
	te := newTrashEnv(t, nil, syscall.EXDEV) // every rename fails
	b, _, _, journal, _, buf := newTestBisync(t, te.root, false)
	b.cfg.Remover = te.tr
	createLocalFile(t, te.root, rel, deleteT0)
	seedBaseline(journal, rel)

	for pass := 1; pass <= 2; pass++ {
		b.execute(context.Background())
		if !fileExists(filepath.Join(te.root, rel)) {
			t.Fatalf("pass %d: file deleted although no trash could take it", pass)
		}
		if !journal.Exists(rel) {
			t.Fatalf("pass %d: journal entry dropped, the deletion would not be replanned", pass)
		}
		if fileExists(filepath.Join(te.root, ".Trash-1000", "files", "f.txt")) {
			t.Fatalf("pass %d: a copy appeared in the trash", pass)
		}
	}
	for _, l := range parseAuditLines(t, buf) {
		if l.Op == "pull_delete" {
			t.Errorf("pull_delete audited for a deletion that did not happen: %+v", l)
		}
	}
}

// The puller (Changes API path): file delete, folder delete with a journaled
// subtree, and the failure case.
func TestPuller_Delete_UsesTrash(t *testing.T) {
	tests := []struct {
		name      string
		homeErr   error
		anyErr    error
		change    Change
		files     []string
		wantTrash []string // basenames expected under .Trash-1000/files
		wantKept  []string // rel paths expected to remain with their journal entry
	}{
		{
			name: "file_trashed_via_topdir", homeErr: syscall.EXDEV,
			change: Change{Path: "f.txt", Deleted: true, ID: "id-1"}, files: []string{"f.txt"},
			wantTrash: []string{"f.txt"},
		},
		{
			name: "folder_subtree_trashed", homeErr: syscall.EROFS,
			change: Change{Path: "dir", Deleted: true, ID: "dir-id"}, files: []string{"dir/a.txt", "dir/s/b.txt"},
			wantTrash: []string{"a.txt", "b.txt"},
		},
		{
			name: "no_trash_available_keeps_file_and_journal", anyErr: syscall.EXDEV,
			change: Change{Path: "f.txt", Deleted: true, ID: "id-1"}, files: []string{"f.txt"},
			wantKept: []string{"f.txt"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			te := newTrashEnv(t, tc.homeErr, tc.anyErr)
			st := newMockStore()
			for _, f := range tc.files {
				createLocalFile(t, te.root, f, subtreeT0)
				id := "id-1"
				if strings.HasPrefix(f, "dir/") {
					id = ""
				}
				putBaseline(t, st, f, id)
			}
			remote := newMockRemoteFS()
			remote.startToken = "start-1"
			remote.changes["start-1"] = Changes{Items: []Change{tc.change}, NextPageToken: "token-2"}
			p := NewPuller(PullerConfig{
				Interval: 30 * time.Second, LocalRoot: te.root,
				ConflictPolicy: PolicyNewerWins, Remover: te.tr,
			}, remote, st, newMockAuditLogger(), newMockPublisher(), newDiscardLogger(), fixedClock(subtreeT0))
			_ = p.PollOnce(context.Background())

			for _, name := range tc.wantTrash {
				if !fileExists(te.trashed(name)) {
					t.Errorf("%s not in the trash", name)
				}
			}
			for _, f := range tc.files {
				kept := false
				for _, k := range tc.wantKept {
					kept = kept || k == f
				}
				if got := fileExists(filepath.Join(te.root, f)); got != kept {
					t.Errorf("%s exists = %v, want %v", f, got, kept)
				}
				if _, has, _ := st.Get(context.Background(), f); has != kept {
					t.Errorf("%s journaled = %v, want %v", f, has, kept)
				}
			}
		})
	}
}

// A remote change reported under a root trash directory is ignored, and the
// push path drops watcher events for it.
func TestTrashDir_NeverPushedNorPulled(t *testing.T) {
	root := t.TempDir()
	createLocalFile(t, root, ".Trash-1000/files/f.txt", subtreeT0)

	remote := newMockRemoteFS()
	st := newMockStore()
	cfg := DefaultConfig()
	cfg.LocalRoot = root
	s := New(cfg, remote, st, newMockAuditLogger(), newMockPublisher(),
		newDiscardLogger(), WithSleepFunc(noSleep))
	events := make(chan Event, 2)
	events <- Event{Path: ".Trash-1000/files/f.txt", Op: OpCreate, At: time.Now()}
	events <- Event{Path: ".Trash/1000/files/f.txt", Op: OpWrite, At: time.Now()}
	runSyncer(t, s, events, make(chan DirRename), func() {})
	if n := len(remote.getCopyCalls()); n != 0 || len(st.getPuts()) != 0 {
		t.Errorf("push of a trash path: copies=%d puts=%d", n, len(st.getPuts()))
	}

	pullRemote := newMockRemoteFS()
	pullRemote.startToken = "start-1"
	pullRemote.changes["start-1"] = Changes{Items: []Change{
		{Path: ".Trash-1000/files/f.txt", Object: &RemoteObject{Path: ".Trash-1000/files/f.txt"}},
		{Path: ".Trash-1000/files/f.txt", Deleted: true},
	}, NextPageToken: "token-2"}
	p := NewPuller(PullerConfig{Interval: 30 * time.Second, LocalRoot: root, ConflictPolicy: PolicyNewerWins},
		pullRemote, newMockStore(), newMockAuditLogger(), newMockPublisher(), newDiscardLogger(), fixedClock(subtreeT0))
	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(pullRemote.downloadedFiles) != 0 || !fileExists(filepath.Join(root, ".Trash-1000", "files", "f.txt")) {
		t.Errorf("pull touched the trash dir: downloads=%d", len(pullRemote.downloadedFiles))
	}
}

func TestRemoveLocalPath_TrashErrorIsReported(t *testing.T) {
	te := newTrashEnv(t, nil, syscall.EXDEV)
	createLocalFile(t, te.root, "f.txt", deleteT0)
	err := removeLocalPath(te.tr, filepath.Join(te.root, "f.txt"))
	if !errors.Is(err, trash.ErrUnavailable) || !fileExists(filepath.Join(te.root, "f.txt")) {
		t.Fatalf("err = %v, want ErrUnavailable with the file kept", err)
	}
}
