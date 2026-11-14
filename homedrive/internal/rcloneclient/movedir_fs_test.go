package rcloneclient

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemFS_MoveDir(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := time.Now()

	tests := []struct {
		name     string
		seed     []string
		src, dst string
		wantErr  error
		wantHave []string
		wantGone []string
	}{
		{
			name:     "SubtreeMoves",
			seed:     []string{"d1/a.txt", "d1/sub/b.txt", "d10/c.txt"},
			src:      "d1",
			dst:      "d2",
			wantHave: []string{"d2/a.txt", "d2/sub/b.txt", "d10/c.txt"},
			wantGone: []string{"d1/a.txt", "d1/sub/b.txt"},
		},
		{
			name:    "MissingSource",
			seed:    []string{"other/a.txt"},
			src:     "d1",
			dst:     "d2",
			wantErr: ErrNotFound,
		},
		{
			name:     "DestCollisionMovesNothing",
			seed:     []string{"d1/a.txt", "d2/a.txt"},
			src:      "d1",
			dst:      "d2",
			wantErr:  ErrAlreadyExists,
			wantHave: []string{"d1/a.txt", "d2/a.txt"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := NewMemFS()
			for _, p := range tt.seed {
				m.Seed(p, now, "h")
			}
			err := m.MoveDir(ctx, tt.src, tt.dst)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("MoveDir err = %v, want %v", err, tt.wantErr)
			}
			for _, p := range tt.wantHave {
				if _, err := m.Stat(ctx, p); err != nil {
					t.Errorf("Stat(%q) after MoveDir: %v", p, err)
				}
			}
			for _, p := range tt.wantGone {
				if _, err := m.Stat(ctx, p); !errors.Is(err, ErrNotFound) {
					t.Errorf("Stat(%q) after MoveDir: err = %v, want ErrNotFound", p, err)
				}
			}
		})
	}
}

func TestDryRunFS_MoveDir(t *testing.T) {
	t.Parallel()

	inner := NewMemFS()
	inner.Seed("d1/a.txt", time.Now(), "hash")
	logger, buf := logCapture(t)
	dry := NewDryRunFS(inner, logger)
	ctx := context.Background()

	if err := dry.MoveDir(ctx, "d1", "d2"); err != nil {
		t.Fatalf("MoveDir error: %v", err)
	}
	if _, err := inner.Stat(ctx, "d1/a.txt"); err != nil {
		t.Error("inner MemFS src should still exist after dry-run dir move")
	}
	assertLogContains(t, buf, "would move directory")
}

func TestFlakyFS_MoveDir(t *testing.T) {
	t.Parallel()

	inner := NewMemFS()
	inner.Seed("d1/a.txt", time.Now(), "hash")
	f := NewFlakyFS(inner)
	ctx := context.Background()

	if err := f.MoveDir(ctx, "d1", "d2"); err != nil {
		t.Fatalf("MoveDir passthrough: %v", err)
	}
	if _, err := inner.Stat(ctx, "d2/a.txt"); err != nil {
		t.Errorf("d2/a.txt after passthrough MoveDir: %v", err)
	}
}
