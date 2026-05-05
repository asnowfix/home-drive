package watcher

import (
	"path/filepath"
	"testing"
)

func TestFilter_Excluded(t *testing.T) {
	root := "/mnt/external/gdrive"

	tests := []struct {
		name     string
		patterns []string
		path     string
		want     bool
	}{
		{
			name:     "GitDirectory",
			patterns: []string{"**/.git/**"},
			path:     filepath.Join(root, "project/.git/HEAD"),
			want:     true,
		},
		{
			name:     "GitDirectoryItself",
			patterns: []string{"**/.git/**"},
			path:     filepath.Join(root, "project/.git"),
			want:     true,
		},
		{
			name:     "DSStore",
			patterns: []string{"**/.DS_Store"},
			path:     filepath.Join(root, "Documents/.DS_Store"),
			want:     true,
		},
		{
			name:     "VimSwap",
			patterns: []string{"**/*.swp"},
			path:     filepath.Join(root, "docs/.readme.swp"),
			want:     true,
		},
		{
			name:     "NodeModules",
			patterns: []string{"**/node_modules/**"},
			path:     filepath.Join(root, "app/node_modules/lodash/index.js"),
			want:     true,
		},
		{
			name:     "NormalFile",
			patterns: []string{"**/.git/**", "**/*.swp"},
			path:     filepath.Join(root, "Documents/notes.md"),
			want:     false,
		},
		{
			name:     "NoPatterns",
			patterns: nil,
			path:     filepath.Join(root, "anything.txt"),
			want:     false,
		},
		{
			name:     "TmpFile",
			patterns: []string{"**/*.tmp"},
			path:     filepath.Join(root, "download.tmp"),
			want:     true,
		},
		{
			name:     "TildeBackup",
			patterns: []string{"**/*~"},
			path:     filepath.Join(root, "file.txt~"),
			want:     true,
		},
		{
			name:     "IdeaDirectory",
			patterns: []string{"**/.idea/**"},
			path:     filepath.Join(root, "project/.idea/workspace.xml"),
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFilter(root, tt.patterns)
			got := f.excluded(tt.path)
			if got != tt.want {
				t.Errorf("excluded(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}
