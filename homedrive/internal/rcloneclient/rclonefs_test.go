package rcloneclient

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	rclonefs "github.com/rclone/rclone/fs"
)

func TestRemoteObjectFromRclone_MapsFields(t *testing.T) {
	mtime := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	obj := fakeObject{remote: "dir/file.txt", size: 99, modTime: mtime, id: "id-1"}

	ro := remoteObjectFromRclone(obj)
	if ro.Path != "dir/file.txt" || ro.Size != 99 || !ro.ModTime.Equal(mtime) || ro.RemoteID != "id-1" {
		t.Errorf("remoteObjectFromRclone = %+v, unexpected", ro)
	}
}

func TestRcloneFS_Stat_Cases(t *testing.T) {
	fsys := &fakeListingFS{byPath: map[string]fakeObject{
		"a.txt": {remote: "a.txt", size: 3, id: "id-a"},
	}}
	r := &RcloneFS{log: slog.Default(), fsObj: fsys}

	t.Run("found", func(t *testing.T) {
		ro, err := r.Stat(context.Background(), "a.txt")
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if ro.Path != "a.txt" || ro.Size != 3 {
			t.Errorf("Stat = %+v, unexpected", ro)
		}
	})

	t.Run("not found", func(t *testing.T) {
		if _, err := r.Stat(context.Background(), "missing.txt"); err == nil {
			t.Fatal("expected an error for a missing object")
		}
	})
}

func TestRcloneFS_List(t *testing.T) {
	fsys := &fakeListingFS{tree: map[string][]rclonefs.DirEntry{
		"": {
			fakeObject{remote: "a.txt", size: 1, id: "id-a"},
			fakeDirectory{remote: "sub", id: "sub-id"},
		},
	}}
	r := &RcloneFS{log: slog.Default(), fsObj: fsys}

	objs, err := r.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(objs) != 1 || objs[0].Path != "a.txt" {
		t.Errorf("List = %+v, want single entry a.txt (directories excluded)", objs)
	}
}

func TestRcloneFS_Quota_Cases(t *testing.T) {
	used, total := int64(50), int64(200)

	t.Run("with usage", func(t *testing.T) {
		fsys := &fakeListingFS{usage: &rclonefs.Usage{Used: &used, Total: &total}}
		r := &RcloneFS{log: slog.Default(), fsObj: fsys}
		q, err := r.Quota(context.Background())
		if err != nil {
			t.Fatalf("Quota: %v", err)
		}
		if q.Used != used || q.Total != total {
			t.Errorf("Quota = %+v, want Used=%d Total=%d", q, used, total)
		}
	})

	t.Run("unlimited total", func(t *testing.T) {
		fsys := &fakeListingFS{usage: &rclonefs.Usage{Used: &used}}
		r := &RcloneFS{log: slog.Default(), fsObj: fsys}
		q, err := r.Quota(context.Background())
		if err != nil {
			t.Fatalf("Quota: %v", err)
		}
		if q.Total != -1 {
			t.Errorf("Total = %d, want -1 (unlimited)", q.Total)
		}
	})

	t.Run("not supported", func(t *testing.T) {
		fsys := &fakeListingFS{}
		r := &RcloneFS{log: slog.Default(), fsObj: fsys}
		if _, err := r.Quota(context.Background()); err == nil {
			t.Fatal("expected an error when About returns ErrorNotImplemented")
		}
	})
}

func TestRcloneFS_DownloadFile(t *testing.T) {
	mtime := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	fsys := &fakeListingFS{byPath: map[string]fakeObject{
		"remote/a.txt": {remote: "remote/a.txt", size: 5, modTime: mtime, content: "hello"},
	}}
	r := &RcloneFS{log: slog.Default(), fsObj: fsys}

	dst := filepath.Join(t.TempDir(), "sub", "a.txt")
	if err := r.DownloadFile(context.Background(), "remote/a.txt", dst); err != nil {
		t.Fatalf("DownloadFile: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("content = %q, want hello", got)
	}
}

func TestRcloneFS_DownloadFile_NotFound(t *testing.T) {
	r := &RcloneFS{log: slog.Default(), fsObj: &fakeListingFS{}}
	dst := filepath.Join(t.TempDir(), "a.txt")
	if err := r.DownloadFile(context.Background(), "missing.txt", dst); err == nil {
		t.Fatal("expected an error for a missing remote object")
	}
}
