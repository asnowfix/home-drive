package syncer

import (
	"os"
	"path/filepath"
	"testing"
)

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
			err := removeLocalPath(p)
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
