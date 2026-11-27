package syncer

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalPathFor_Cases(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	tests := []struct {
		name    string
		rel     string
		wantErr bool
	}{
		{"plain", "a/b.txt", false},
		{"parent", "../x", true},
		{"inner_escape", "a/../../x", true},
		{"inner_dotdot", "a/../b", true},
		{"absolute", "/abs", true},
		{"empty", "", true},
		{"dot", ".", true},
		{"dotdot", "..", true},
		{"nul", "a\x00b", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := localPathFor(root, tt.rel)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && !errors.Is(err, ErrUnsafeRemotePath) {
				t.Errorf("err = %v, want ErrUnsafeRemotePath", err)
			}
			if err == nil && got != filepath.Join(root, filepath.FromSlash(tt.rel)) {
				t.Errorf("path = %q", got)
			}
		})
	}
}

// issue #191: a Drive-fed local path must never be, or go through, a symlink.
func TestLocalPathFor_ThroughSymlink_Cases(t *testing.T) {
	skipIfNoSymlinks(t)
	base := t.TempDir()
	root := filepath.Join(base, "root")
	if err := os.MkdirAll(filepath.Join(root, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{
		"dirlink":  "real",
		"filelink": "real/f.txt",
		"dangling": "nowhere",
	} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name    string
		rel     string
		wantErr bool
	}{
		{"under_real_dir", "real/new.txt", false},
		{"missing_dirs", "a/b/c.txt", false},
		{"under_dir_link", "dirlink/new.txt", true},
		{"deep_under_dir_link", "dirlink/x/y/z.txt", true},
		{"is_file_link", "filelink", true},
		{"is_dangling_link", "dangling", true},
		{"under_dangling_link", "dangling/x", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := localPathFor(root, tt.rel)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && (!errors.Is(err, ErrThroughSymlink) || !errors.Is(err, ErrUnsafeRemotePath)) {
				t.Errorf("err = %v, want ErrThroughSymlink wrapping ErrUnsafeRemotePath", err)
			}
		})
	}
}
