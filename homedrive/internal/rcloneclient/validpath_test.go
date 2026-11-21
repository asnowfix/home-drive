package rcloneclient

import (
	"log/slog"
	"testing"

	drivev3 "google.golang.org/api/drive/v3"
)

func TestValidName_Cases(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"plain", "photo.jpg", true},
		{"dotfile", ".git", true},
		{"dots_inside", "a..b", true},
		{"empty", "", false},
		{"dot", ".", false},
		{"dotdot", "..", false},
		{"slash", "a/b", false},
		{"leading_slash", "/a", false},
		{"backslash", "a\\b", false},
		{"nul", "a\x00b", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidName(tt.in); got != tt.want {
				t.Errorf("ValidName(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestValidRemotePath_Cases(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"file", "a.txt", true},
		{"nested", "a/b/c.txt", true},
		{"empty_root", "", false},
		{"dot", ".", false},
		{"dotdot", "..", false},
		{"escape", "../outside.txt", false},
		{"inner_dotdot", "a/../b", false},
		{"inner_dot", "a/./b", false},
		{"trailing_dotdot", "Photos/2024/..", false},
		{"double_slash", "a//b", false},
		{"leading_slash", "/a", false},
		{"trailing_slash", "a/", false},
		{"backslash", "a\\b", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidRemotePath(tt.in); got != tt.want {
				t.Errorf("ValidRemotePath(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// T1 (#152 review 2): an unsafe Drive name must be skipped at the translate
// boundary. path.Join would otherwise resolve ".." to the parent folder and
// turn the delete of a trashed ".." item into a delete of its parent.
func TestTranslateChange_RejectsUnsafeNames(t *testing.T) {
	for _, name := range []string{"..", ".", "", "a/b", "a\\b", "../outside.txt"} {
		for _, trashed := range []bool{false, true} {
			r := &RcloneFS{log: slog.Default(), pathCache: newIDPathCache()}
			r.pathCache.put("parent", "Photos/2024")
			ch, ok := r.translateChange(&drivev3.Change{
				FileId: "x", File: &drivev3.File{Name: name, Parents: []string{"parent"}, Trashed: trashed},
			})
			if ok {
				t.Errorf("name %q trashed=%v translated to %+v, want it skipped", name, trashed, ch)
			}
		}
	}
}

// A cached path that is not safe (should never be cached, but is untrusted
// all the same) is not turned into a delete.
func TestTranslateChange_RejectsUnsafeCachedPath(t *testing.T) {
	r := &RcloneFS{log: slog.Default(), pathCache: newIDPathCache()}
	r.pathCache.put("id-bad", "../outside.txt")
	if ch, ok := r.translateChange(&drivev3.Change{FileId: "id-bad", Removed: true}); ok {
		t.Errorf("Removed for an unsafe cached path produced %+v", ch)
	}
	r.pathCache.put("id-root", "")
	if ch, ok := r.translateChange(&drivev3.Change{FileId: "id-root", Removed: true}); ok {
		t.Errorf("Removed for the sync root produced %+v", ch)
	}
}

// T3 (#152 review 2): trashing a folder also forgets the IDs cached beneath
// it, or a later Removed for an old descendant ID resolves to the live path
// of a recreated descendant.
func TestTranslateChange_EvictsDescendantsOfTrashedFolder(t *testing.T) {
	r := &RcloneFS{log: slog.Default(), pathCache: newIDPathCache()}
	r.pathCache.put("old-A", "A")
	r.pathCache.put("old-s", "A/s")
	r.pathCache.put("old-f", "A/s/f.txt")
	r.pathCache.put("other", "AB/x") // shares a name prefix, not a descendant

	if _, ok := r.translateChange(&drivev3.Change{
		FileId: "old-A", File: &drivev3.File{Name: "A", Trashed: true},
	}); !ok {
		t.Fatal("trashed folder change skipped")
	}
	r.pathCache.put("new-s", "A/s") // recreated

	for _, id := range []string{"old-s", "old-f"} {
		if ch, ok := r.translateChange(&drivev3.Change{FileId: id, Removed: true}); ok {
			t.Errorf("Removed for old descendant %s resolved to %q", id, ch.Path)
		}
	}
	if _, ok := r.pathCache.get("other"); !ok {
		t.Error("a sibling sharing a name prefix was evicted")
	}
	if p, ok := r.pathCache.get("new-s"); !ok || p != "A/s" {
		t.Error("the recreated descendant's ID was evicted")
	}
}
