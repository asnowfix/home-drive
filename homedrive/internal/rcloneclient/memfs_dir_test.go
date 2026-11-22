package rcloneclient

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestMemFS_RemoveEmptyDir_Cases(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(m *MemFS)
		path     string
		wantErr  error
		wantDirs int
	}{
		{name: "empty seeded dir removed", path: "d",
			setup: func(m *MemFS) { m.SeedDir("d") }, wantDirs: 0},
		{name: "file under it refuses", path: "d",
			setup:   func(m *MemFS) { m.SeedDir("d"); m.Seed("d/f", time.Now(), "m") },
			wantErr: ErrDirNotEmpty, wantDirs: 1},
		{name: "subfolder refuses", path: "d",
			setup:   func(m *MemFS) { m.SeedDir("d"); m.SeedDir("d/e") },
			wantErr: ErrDirNotEmpty, wantDirs: 2},
		{name: "missing", path: "d", setup: func(*MemFS) {}, wantErr: ErrNotFound},
		{name: "root refused", path: "", setup: func(m *MemFS) { m.SeedDir("d") },
			wantErr: errAny, wantDirs: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewMemFS()
			tt.setup(m)
			err := m.RemoveEmptyDir(context.Background(), tt.path)
			switch {
			case tt.wantErr == nil && err != nil:
				t.Fatalf("err = %v", err)
			case tt.wantErr == errAny && err == nil:
				t.Fatal("want an error")
			case tt.wantErr != nil && tt.wantErr != errAny && !errors.Is(err, tt.wantErr):
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if got := len(m.Dirs()); got != tt.wantDirs {
				t.Errorf("dirs = %v, want %d", m.Dirs(), tt.wantDirs)
			}
		})
	}
}

func TestMemFS_DeleteFile_DirIsDir(t *testing.T) {
	m := NewMemFS()
	m.SeedDir("d")
	m.Seed("e/f", time.Now(), "m")
	for _, p := range []string{"d", "e"} {
		if err := m.DeleteFile(context.Background(), p); !errors.Is(err, ErrIsDir) {
			t.Errorf("DeleteFile(%q) = %v, want ErrIsDir", p, err)
		}
	}
	if err := m.DeleteFile(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteFile(nope) = %v, want ErrNotFound", err)
	}
}

func TestDryRunFS_RemoveEmptyDir_LeavesRemote(t *testing.T) {
	m := NewMemFS()
	m.SeedDir("d")
	d := NewDryRunFS(m, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err := d.RemoveEmptyDir(context.Background(), "d"); err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(m.Dirs()) != 1 {
		t.Error("dry-run removed the folder")
	}
}

func TestFlakyFS_RemoveEmptyDir_InjectsAndPasses(t *testing.T) {
	m := NewMemFS()
	m.SeedDir("d")
	boom := errors.New("boom")
	f := NewFlakyFS(m, FlakyRule{Method: "RemoveEmptyDir", Action: FlakyAction{Err: boom}})
	if err := f.RemoveEmptyDir(context.Background(), "d"); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want injected", err)
	}
	f.ClearRules()
	if err := f.RemoveEmptyDir(context.Background(), "d"); err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(m.Dirs()) != 0 {
		t.Error("pass-through did not remove the folder")
	}
}
