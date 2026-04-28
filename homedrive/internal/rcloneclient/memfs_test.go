package rcloneclient

import (
	"context"
	"errors"
	"testing"
	"time"
)

// mockClock is a simple injectable clock for tests.
type mockClock struct {
	now time.Time
}

func (c *mockClock) Now() time.Time { return c.now }

func (c *mockClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func TestMemFS_CopyFile(t *testing.T) {
	t.Parallel()

	clk := &mockClock{now: time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)}
	m := NewMemFS(WithClock(clk))
	ctx := context.Background()

	tests := []struct {
		name   string
		src    string
		dstDir string
		want   string
	}{
		{
			name:   "SimpleFile",
			src:    "/local/docs/readme.md",
			dstDir: "Documents",
			want:   "Documents/readme.md",
		},
		{
			name:   "NestedDir",
			src:    "/local/photos/vacation/img001.jpg",
			dstDir: "Photos/2026",
			want:   "Photos/2026/img001.jpg",
		},
		{
			name:   "RootDir",
			src:    "/local/file.txt",
			dstDir: "",
			want:   "file.txt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj, err := m.CopyFile(ctx, tt.src, tt.dstDir)
			if err != nil {
				t.Fatalf("CopyFile(%q, %q) error: %v", tt.src, tt.dstDir, err)
			}
			if obj.Path != tt.want {
				t.Errorf("CopyFile path = %q, want %q", obj.Path, tt.want)
			}
			if obj.ModTime != clk.Now() {
				t.Errorf("CopyFile ModTime = %v, want %v", obj.ModTime, clk.Now())
			}
			if obj.RemoteID == "" {
				t.Error("CopyFile RemoteID is empty")
			}

			// Verify the file exists via Stat.
			got, err := m.Stat(ctx, tt.want)
			if err != nil {
				t.Fatalf("Stat(%q) after copy error: %v", tt.want, err)
			}
			if got.Path != tt.want {
				t.Errorf("Stat path = %q, want %q", got.Path, tt.want)
			}
		})
	}
}

func TestMemFS_DeleteFile(t *testing.T) {
	t.Parallel()

	m := NewMemFS()
	ctx := context.Background()
	now := time.Now()

	tests := []struct {
		name    string
		seed    string
		del     string
		wantErr error
	}{
		{
			name: "ExistingFile",
			seed: "docs/readme.md",
			del:  "docs/readme.md",
		},
		{
			name:    "NonExistentFile",
			seed:    "",
			del:     "missing/file.txt",
			wantErr: ErrNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.seed != "" {
				m.Seed(tt.seed, now, "abc123")
			}
			err := m.DeleteFile(ctx, tt.del)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("DeleteFile(%q) error = %v, want %v", tt.del, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("DeleteFile(%q) error: %v", tt.del, err)
			}
			// Verify the file is gone.
			_, err = m.Stat(ctx, tt.del)
			if !errors.Is(err, ErrNotFound) {
				t.Errorf("Stat(%q) after delete: got err=%v, want ErrNotFound", tt.del, err)
			}
		})
	}
}

func TestMemFS_MoveFile(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := time.Now()

	tests := []struct {
		name    string
		src     string
		dst     string
		wantErr error
	}{
		{
			name: "SimpleMove",
			src:  "docs/old.md",
			dst:  "docs/new.md",
		},
		{
			name: "CrossDir",
			src:  "docs/file.md",
			dst:  "archive/file.md",
		},
		{
			name:    "SrcNotFound",
			src:     "missing.txt",
			dst:     "dest.txt",
			wantErr: ErrNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewMemFS()
			if tt.wantErr == nil {
				m.Seed(tt.src, now, "hash1")
			}

			err := m.MoveFile(ctx, tt.src, tt.dst)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("MoveFile(%q, %q) error = %v, want %v", tt.src, tt.dst, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("MoveFile(%q, %q) error: %v", tt.src, tt.dst, err)
			}

			// Source should be gone.
			_, err = m.Stat(ctx, tt.src)
			if !errors.Is(err, ErrNotFound) {
				t.Errorf("Stat(%q) after move: got err=%v, want ErrNotFound", tt.src, err)
			}

			// Destination should exist.
			got, err := m.Stat(ctx, tt.dst)
			if err != nil {
				t.Fatalf("Stat(%q) after move: %v", tt.dst, err)
			}
			if got.Path != tt.dst {
				t.Errorf("moved object path = %q, want %q", got.Path, tt.dst)
			}
		})
	}
}

func TestMemFS_MoveFile_DestExists(t *testing.T) {
	t.Parallel()

	m := NewMemFS()
	ctx := context.Background()
	now := time.Now()

	m.Seed("src.txt", now, "h1")
	m.Seed("dst.txt", now, "h2")

	err := m.MoveFile(ctx, "src.txt", "dst.txt")
	if !errors.Is(err, ErrAlreadyExists) {
		t.Errorf("MoveFile to existing dest: got err=%v, want ErrAlreadyExists", err)
	}
}

func TestMemFS_Stat(t *testing.T) {
	t.Parallel()

	m := NewMemFS()
	ctx := context.Background()
	now := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		path    string
		seed    bool
		wantErr error
	}{
		{
			name: "ExistingFile",
			path: "docs/readme.md",
			seed: true,
		},
		{
			name:    "MissingFile",
			path:    "missing/file.txt",
			seed:    false,
			wantErr: ErrNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.seed {
				m.Seed(tt.path, now, "checksum")
			}
			obj, err := m.Stat(ctx, tt.path)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("Stat(%q) error = %v, want %v", tt.path, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Stat(%q) error: %v", tt.path, err)
			}
			if obj.Path != tt.path {
				t.Errorf("Stat path = %q, want %q", obj.Path, tt.path)
			}
			if obj.MD5 != "checksum" {
				t.Errorf("Stat MD5 = %q, want %q", obj.MD5, "checksum")
			}
			if !obj.ModTime.Equal(now) {
				t.Errorf("Stat ModTime = %v, want %v", obj.ModTime, now)
			}
		})
	}
}

func TestMemFS_ListChanges(t *testing.T) {
	t.Parallel()

	m := NewMemFS()
	ctx := context.Background()

	// Initially empty.
	ch, err := m.ListChanges(ctx, "")
	if err != nil {
		t.Fatalf("ListChanges empty: %v", err)
	}
	if len(ch.Items) != 0 {
		t.Errorf("ListChanges empty: got %d items, want 0", len(ch.Items))
	}

	// Seed some changes.
	m.AddChange(Change{Path: "a.txt", Object: &RemoteObject{Path: "a.txt"}})
	m.AddChange(Change{Path: "b.txt", Deleted: true})

	// Read all from start.
	ch, err = m.ListChanges(ctx, "")
	if err != nil {
		t.Fatalf("ListChanges: %v", err)
	}
	if len(ch.Items) != 2 {
		t.Fatalf("ListChanges: got %d items, want 2", len(ch.Items))
	}
	if ch.Items[0].Path != "a.txt" {
		t.Errorf("first change path = %q, want %q", ch.Items[0].Path, "a.txt")
	}
	if !ch.Items[1].Deleted {
		t.Error("second change should be deleted")
	}

	// Read from the returned token -- should get nothing new.
	ch2, err := m.ListChanges(ctx, ch.NextPageToken)
	if err != nil {
		t.Fatalf("ListChanges with token: %v", err)
	}
	if len(ch2.Items) != 0 {
		t.Errorf("ListChanges after consuming: got %d items, want 0", len(ch2.Items))
	}
}

func TestMemFS_Quota(t *testing.T) {
	t.Parallel()

	m := NewMemFS()
	ctx := context.Background()

	// Default quota.
	q, err := m.Quota(ctx)
	if err != nil {
		t.Fatalf("Quota: %v", err)
	}
	if q.Total != 15*1024*1024*1024 {
		t.Errorf("default total = %d, want 15 GB", q.Total)
	}

	// Custom quota.
	m.SetQuota(5*1024*1024*1024, 15*1024*1024*1024)
	q, err = m.Quota(ctx)
	if err != nil {
		t.Fatalf("Quota: %v", err)
	}
	if q.Used != 5*1024*1024*1024 {
		t.Errorf("used = %d, want 5 GB", q.Used)
	}

	pct := q.UsedPercent()
	// 5/15 = 33.33%
	if pct < 33.0 || pct > 34.0 {
		t.Errorf("UsedPercent = %f, want ~33.33", pct)
	}
}

func TestMemFS_QuotaTracking(t *testing.T) {
	t.Parallel()

	m := NewMemFS()
	m.SetQuota(0, 100*1024)
	ctx := context.Background()

	// CopyFile should increase used quota.
	_, err := m.CopyFile(ctx, "/local/file.txt", "remote")
	if err != nil {
		t.Fatalf("CopyFile: %v", err)
	}

	q, err := m.Quota(ctx)
	if err != nil {
		t.Fatalf("Quota: %v", err)
	}
	if q.Used != 1024 {
		t.Errorf("used after copy = %d, want 1024", q.Used)
	}

	// DeleteFile should decrease used quota.
	err = m.DeleteFile(ctx, "remote/file.txt")
	if err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}

	q, err = m.Quota(ctx)
	if err != nil {
		t.Fatalf("Quota: %v", err)
	}
	if q.Used != 0 {
		t.Errorf("used after delete = %d, want 0", q.Used)
	}
}

func TestMemFS_ListChanges_InvalidToken(t *testing.T) {
	t.Parallel()

	m := NewMemFS()
	ctx := context.Background()

	_, err := m.ListChanges(ctx, "not-a-number")
	if err == nil {
		t.Error("ListChanges with invalid token: expected error")
	}
}

func TestMemFS_SeedWithSize(t *testing.T) {
	t.Parallel()

	m := NewMemFS()
	ctx := context.Background()
	now := time.Now()

	m.SeedWithSize("big.bin", now, "hash", 5*1024*1024)

	obj, err := m.Stat(ctx, "big.bin")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if obj.Size != 5*1024*1024 {
		t.Errorf("size = %d, want %d", obj.Size, 5*1024*1024)
	}
}

func TestMemFS_Files(t *testing.T) {
	t.Parallel()

	m := NewMemFS()
	now := time.Now()

	m.Seed("a.txt", now, "h1")
	m.Seed("b.txt", now, "h2")

	files := m.Files()
	if len(files) != 2 {
		t.Errorf("Files count = %d, want 2", len(files))
	}
	if _, ok := files["a.txt"]; !ok {
		t.Error("Files missing a.txt")
	}
	if _, ok := files["b.txt"]; !ok {
		t.Error("Files missing b.txt")
	}

	// Modifying the returned map should not affect MemFS.
	delete(files, "a.txt")
	files2 := m.Files()
	if len(files2) != 2 {
		t.Error("Files map leak: modification affected MemFS")
	}
}
