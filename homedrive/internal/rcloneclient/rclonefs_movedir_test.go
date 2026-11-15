package rcloneclient

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	rclonefs "github.com/rclone/rclone/fs"
)

// newLocalRcloneFS returns an RcloneFS over a real rclone local Fs rooted
// at a temp dir. Like Drive, the local backend rejects NewObject on a
// directory with fs.ErrorIsDir (issue #136); unlike Drive it has no
// network. Its DirMove feature stands in for Drive's single Files.Update.
func newLocalRcloneFS(t *testing.T) (*RcloneFS, string) {
	t.Helper()
	root := t.TempDir()
	fsys, err := rclonefs.NewFs(context.Background(), root)
	if err != nil {
		t.Fatalf("NewFs: %v", err)
	}
	return &RcloneFS{log: slog.Default(), fsObj: fsys}, root
}

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestRcloneFS_MoveFile_Directory pins the root cause of #136: MoveFile
// cannot move a directory because NewObject reports it as a directory.
func TestRcloneFS_MoveFile_Directory(t *testing.T) {
	r, root := newLocalRcloneFS(t)
	writeTree(t, root, map[string]string{"dir1/a.txt": "a"})

	err := r.MoveFile(context.Background(), "dir1", "dir2")
	if !errors.Is(err, rclonefs.ErrorIsDir) {
		t.Fatalf("MoveFile on a directory: err = %v, want fs.ErrorIsDir", err)
	}
}

func TestRcloneFS_MoveDir_Cases(t *testing.T) {
	tests := []struct {
		name     string
		files    map[string]string
		src, dst string
		wantErr  error
		wantGone string
		wantHave []string
	}{
		{
			name:     "dir with files and nested dir",
			files:    map[string]string{"dir1/a.txt": "a", "dir1/sub/b.txt": "b"},
			src:      "dir1",
			dst:      "dir2",
			wantGone: "dir1",
			wantHave: []string{"dir2/a.txt", "dir2/sub/b.txt"},
		},
		{
			name:     "into a not yet existing parent",
			files:    map[string]string{"dir1/a.txt": "a"},
			src:      "dir1",
			dst:      "new/parent/dir2",
			wantGone: "dir1",
			wantHave: []string{"new/parent/dir2/a.txt"},
		},
		{
			name:    "missing source",
			files:   map[string]string{"other/a.txt": "a"},
			src:     "dir1",
			dst:     "dir2",
			wantErr: ErrNotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, root := newLocalRcloneFS(t)
			writeTree(t, root, tt.files)

			err := r.MoveDir(context.Background(), tt.src, tt.dst)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("MoveDir err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("MoveDir: %v", err)
			}
			if _, statErr := os.Stat(filepath.Join(root, tt.wantGone)); !os.IsNotExist(statErr) {
				t.Errorf("source %q still present (stat err = %v)", tt.wantGone, statErr)
			}
			for _, p := range tt.wantHave {
				if _, statErr := os.Stat(filepath.Join(root, filepath.FromSlash(p))); statErr != nil {
					t.Errorf("expected %q after MoveDir: %v", p, statErr)
				}
			}
		})
	}
}
