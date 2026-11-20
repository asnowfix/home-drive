package syncer

import (
	"errors"
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
