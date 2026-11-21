package syncer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// issue #146: .old.<N> comes from the journal; a name that exists on disk
// or on Drive without a journal entry must not be overwritten.

func TestBisync_OldName_NotOverwrittenWhenUnjournaled(t *testing.T) {
	const rel = "f.txt"
	later := classifyT0.Add(time.Hour)
	tests := []struct {
		name  string
		setup func(t *testing.T, root string, remote *mockRemoteFS)
		check func(t *testing.T, root string, remote *mockRemoteFS)
	}{
		{
			name: "local_wins_remote_old1_exists",
			setup: func(t *testing.T, root string, remote *mockRemoteFS) {
				createLocalFile(t, root, rel, later) // local newer
				remote.Seed(rel, classifyT0.Add(time.Minute), "m1")
				remote.Seed("f.txt.old.1", classifyT0, "keep")
			},
			check: func(t *testing.T, _ string, remote *mockRemoteFS) {
				if got := remote.files["f.txt.old.1"].MD5; got != "keep" {
					t.Errorf("remote f.txt.old.1 overwritten: md5 = %q", got)
				}
				if got := remote.files["f.txt.old.2"].MD5; got != "m1" {
					t.Errorf("loser not kept as f.txt.old.2: md5 = %q", got)
				}
			},
		},
		{
			name: "remote_wins_local_old1_exists",
			setup: func(t *testing.T, root string, remote *mockRemoteFS) {
				createLocalFile(t, root, rel, classifyT0.Add(time.Minute))
				createLocalFile(t, root, "f.txt.old.1", classifyT0)
				remote.Seed(rel, later, "m2") // remote newer
			},
			check: func(t *testing.T, root string, _ *mockRemoteFS) {
				got, err := os.ReadFile(filepath.Join(root, "f.txt.old.1"))
				if err != nil || string(got) != "content-f.txt.old.1" {
					t.Errorf("local f.txt.old.1 overwritten: %q, %v", got, err)
				}
				if _, err := os.Stat(filepath.Join(root, "f.txt.old.2")); err != nil {
					t.Errorf("loser not kept as f.txt.old.2: %v", err)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			b, _, remote, journal, _, _ := newTestBisync(t, root, false)
			tc.setup(t, root, remote)
			journal.Seed(JournalEntry{Path: rel, LocalMtime: classifyT0.Add(-time.Hour),
				RemoteMtime: classifyT0.Add(-time.Hour), RemoteMD5: "m0", LastOrigin: "remote"})

			b.execute(context.Background())
			tc.check(t, root, remote)
		})
	}
}
