package rcloneclient

import (
	"context"
	"log/slog"
	"testing"

	rclonefs "github.com/rclone/rclone/fs"
	drivev3 "google.golang.org/api/drive/v3"
)

// U1a (#152 review 3): when a change moves an item where the cache will not
// follow (outside the sync root, or into an excluded path), the old mapping
// must go, or a later Removed for that ID resolves to a live item that now
// has the same path.
func TestTranslateChange_ForgetsIDOnMoveOutOrExclusion(t *testing.T) {
	tests := []struct {
		name   string
		parent string // Parents[0] of the moving change
	}{
		{name: "moved_out_of_root_unresolved_parent", parent: "unknown-parent"},
		{name: "moved_into_excluded_path", parent: "excluded-parent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &RcloneFS{
				log: slog.Default(), pathCache: newIDPathCache(),
				exclude: []string{"**/.git/**"},
			}
			r.pathCache.put("old-A", "A")
			r.pathCache.put("old-s", "A/s")
			r.pathCache.put("excluded-parent", ".git")

			if ch, ok := r.translateChange(&drivev3.Change{
				FileId: "old-A", File: &drivev3.File{Name: "A", Parents: []string{tt.parent}},
			}); ok {
				t.Fatalf("moving change translated to %+v, want skipped", ch)
			}
			r.pathCache.put("new-A", "A") // recreated inside the root

			for _, id := range []string{"old-A", "old-s"} {
				if ch, ok := r.translateChange(&drivev3.Change{FileId: id, Removed: true}); ok {
					t.Errorf("Removed for stale ID %s resolved to %q", id, ch.Path)
				}
			}
			if p, ok := r.pathCache.get("new-A"); !ok || p != "A" {
				t.Error("the live item's ID was evicted")
			}
		})
	}
}

// The puller confirms a folder is gone with List: a directory that does not
// exist is empty, while a missing root is still an error.
func TestRcloneFS_List_MissingDirectory(t *testing.T) {
	fsys := &fakeListingFS{tree: map[string][]rclonefs.DirEntry{
		"":     {fakeDirectory{remote: "here", id: "here-id"}},
		"here": {fakeObject{remote: "here/f.txt", size: 1, id: "f"}},
	}}
	r := &RcloneFS{log: slog.Default(), fsObj: fsys, pathCache: newIDPathCache()}

	if objs, err := r.List(context.Background(), "gone"); err != nil || len(objs) != 0 {
		t.Errorf("List(missing dir) = %v, %v; want empty, nil", objs, err)
	}
	if objs, err := r.List(context.Background(), "here"); err != nil || len(objs) != 1 {
		t.Errorf("List(existing dir) = %v, %v; want 1 object", objs, err)
	}
	delete(fsys.tree, "")
	if _, err := r.List(context.Background(), ""); err == nil {
		t.Error("List of a missing root must stay an error")
	}
}
