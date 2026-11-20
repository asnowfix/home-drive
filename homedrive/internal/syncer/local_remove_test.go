package syncer

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// osRemover is the hard-delete remover tests that do not exercise the trash
// wire explicitly; production never does (issue #155 review S2).
type osRemover struct{}

func (osRemover) Remove(path string) error { return os.Remove(path) }

// A nil remover must refuse, never fall back to a hard delete.
func TestRemoveLocalPath_NilRemoverRefuses(t *testing.T) {
	root := t.TempDir()
	createLocalFile(t, root, "f.txt", deleteT0)
	p := filepath.Join(root, "f.txt")
	if err := removeLocalPath(nil, p); !errors.Is(err, ErrNoRemover) {
		t.Fatalf("err = %v, want ErrNoRemover", err)
	}
	if _, err := os.Lstat(p); err != nil {
		t.Errorf("file removed by a nil remover: %v", err)
	}
}

func TestRemoveLocalPath_Cases(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T, root string) string
		wantErr func(error) bool // nil: no error expected
		want    string           // "gone" or "kept"
	}{
		{
			name: "file",
			setup: func(t *testing.T, root string) string {
				createLocalFile(t, root, "f.txt", deleteT0)
				return filepath.Join(root, "f.txt")
			},
			want: "gone",
		},
		{
			name: "empty_dir",
			setup: func(t *testing.T, root string) string {
				p := filepath.Join(root, "d")
				if err := os.Mkdir(p, 0o755); err != nil {
					t.Fatal(err)
				}
				return p
			},
			want: "gone",
		},
		{
			name: "non_empty_dir_refused",
			setup: func(t *testing.T, root string) string {
				createLocalFile(t, root, "d/f.txt", deleteT0)
				return filepath.Join(root, "d")
			},
			wantErr: func(err error) bool { return err != nil },
			want:    "kept",
		},
		{
			name:    "missing",
			setup:   func(_ *testing.T, root string) string { return filepath.Join(root, "nope") },
			wantErr: os.IsNotExist,
			want:    "gone",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := tt.setup(t, t.TempDir())
			err := removeLocalPath(osRemover{}, p)
			if tt.wantErr == nil && err != nil {
				t.Fatalf("removeLocalPath: %v", err)
			}
			if tt.wantErr != nil && !tt.wantErr(err) {
				t.Fatalf("removeLocalPath error = %v", err)
			}
			_, statErr := os.Lstat(p)
			if gone := os.IsNotExist(statErr); gone != (tt.want == "gone") {
				t.Errorf("path state gone=%v, want %s", gone, tt.want)
			}
		})
	}
}
