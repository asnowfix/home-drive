package desktop

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fixedFS(id string) fsIDFunc { return func(string) (string, error) { return id, nil } }

func TestCheckCacheDir_Cases(t *testing.T) {
	now := func() time.Time { return time.Unix(1700000000, 0) }
	tests := []struct {
		name  string
		setup func(t *testing.T, dir string)
		fsID  fsIDFunc
		want  error
	}{
		{"FirstRunCreatesMarker", func(*testing.T, string) {}, fixedFS("A"), nil},
		{
			name: "SameFilesystemAccepted",
			setup: func(t *testing.T, dir string) {
				if err := CheckCacheDir(dir, fixedFS("A"), now); err != nil {
					t.Fatal(err)
				}
			},
			fsID: fixedFS("A"),
		},
		{
			name: "OtherFilesystemRefused",
			setup: func(t *testing.T, dir string) {
				if err := CheckCacheDir(dir, fixedFS("A"), now); err != nil {
					t.Fatal(err)
				}
			},
			fsID: fixedFS("B"),
			want: ErrCacheFilesystemChanged,
		},
		{
			name: "UnidentifiableFilesystemSkipsComparison",
			setup: func(t *testing.T, dir string) {
				if err := CheckCacheDir(dir, fixedFS("A"), now); err != nil {
					t.Fatal(err)
				}
			},
			fsID: fixedFS(""),
		},
		{
			name: "NonEmptyWithoutMarkerRefused",
			setup: func(t *testing.T, dir string) {
				mustMkdir(t, filepath.Join(dir, "vfs"))
			},
			fsID: fixedFS("A"),
			want: ErrCacheUnmarked,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "cache")
			mustMkdir(t, dir)
			tc.setup(t, dir)
			err := CheckCacheDir(dir, tc.fsID, now)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestCheckCacheDir_CreatesMissingDirAndUsesRealFS(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "x", "cache")
	if err := CheckCacheDir(dir, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := CheckCacheDir(dir, nil, nil); err != nil {
		t.Fatalf("second run on the same filesystem: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, cacheMarkerName)); err != nil {
		t.Fatal(err)
	}
}

func TestCheckCacheDir_BrokenMarker(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, cacheMarkerName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckCacheDir(dir, fixedFS("A"), nil); err == nil {
		t.Fatal("corrupt marker must be refused")
	}
}

func TestCheckCacheDir_FSIDError(t *testing.T) {
	boom := errors.New("boom")
	err := CheckCacheDir(t.TempDir(), func(string) (string, error) { return "", boom }, nil)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestFilesystemInfo_Real(t *testing.T) {
	dir := t.TempDir()
	id, err := FilesystemID(dir)
	if err != nil { // id may be "" on filesystems with no stable fsid
		t.Fatalf("FilesystemID = %q, %v", id, err)
	}
	size, err := FilesystemSize(dir)
	if err != nil || size <= 0 {
		t.Fatalf("FilesystemSize = %d, %v", size, err)
	}
	if _, err := FilesystemID(filepath.Join(dir, "missing")); err == nil {
		t.Error("missing path must error")
	}
	if _, err := FilesystemSize(filepath.Join(dir, "missing")); err == nil {
		t.Error("missing path must error")
	}
}
