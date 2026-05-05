package rcloneclient

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// logCapture creates an slog.Logger that writes JSON to the returned buffer.
func logCapture(t *testing.T) (*slog.Logger, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	return logger, &buf
}

func TestDryRunFS_CopyFile(t *testing.T) {
	t.Parallel()

	inner := NewMemFS()
	logger, buf := logCapture(t)
	dry := NewDryRunFS(inner, logger)
	ctx := context.Background()

	obj, err := dry.CopyFile(ctx, "/local/docs/readme.md", "Documents")
	if err != nil {
		t.Fatalf("CopyFile error: %v", err)
	}
	if !obj.DryRun {
		t.Error("CopyFile: DryRun flag should be true")
	}
	if obj.Path != "Documents/readme.md" {
		t.Errorf("CopyFile path = %q, want %q", obj.Path, "Documents/readme.md")
	}

	// Verify the file was NOT created in the inner MemFS.
	files := inner.Files()
	if len(files) != 0 {
		t.Errorf("inner MemFS should be empty, got %d files", len(files))
	}

	// Verify the log contains the expected message.
	assertLogContains(t, buf, "would copy file")
	assertLogContains(t, buf, "CopyFile")
}

func TestDryRunFS_DeleteFile(t *testing.T) {
	t.Parallel()

	inner := NewMemFS()
	inner.Seed("file.txt", time.Now(), "hash")
	logger, buf := logCapture(t)
	dry := NewDryRunFS(inner, logger)
	ctx := context.Background()

	err := dry.DeleteFile(ctx, "file.txt")
	if err != nil {
		t.Fatalf("DeleteFile error: %v", err)
	}

	// Verify the file still exists in the inner MemFS.
	_, err = inner.Stat(ctx, "file.txt")
	if err != nil {
		t.Error("inner MemFS file should still exist after dry-run delete")
	}

	assertLogContains(t, buf, "would delete file")
}

func TestDryRunFS_MoveFile(t *testing.T) {
	t.Parallel()

	inner := NewMemFS()
	inner.Seed("src.txt", time.Now(), "hash")
	logger, buf := logCapture(t)
	dry := NewDryRunFS(inner, logger)
	ctx := context.Background()

	err := dry.MoveFile(ctx, "src.txt", "dst.txt")
	if err != nil {
		t.Fatalf("MoveFile error: %v", err)
	}

	// Verify source still exists (no actual move).
	_, err = inner.Stat(ctx, "src.txt")
	if err != nil {
		t.Error("inner MemFS src should still exist after dry-run move")
	}

	assertLogContains(t, buf, "would move file")
}

func TestDryRunFS_StatPassthrough(t *testing.T) {
	t.Parallel()

	inner := NewMemFS()
	now := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)
	inner.Seed("file.txt", now, "hash")
	logger, _ := logCapture(t)
	dry := NewDryRunFS(inner, logger)
	ctx := context.Background()

	obj, err := dry.Stat(ctx, "file.txt")
	if err != nil {
		t.Fatalf("Stat error: %v", err)
	}
	if obj.Path != "file.txt" {
		t.Errorf("Stat path = %q, want %q", obj.Path, "file.txt")
	}
	if !obj.ModTime.Equal(now) {
		t.Errorf("Stat ModTime = %v, want %v", obj.ModTime, now)
	}
}

func TestDryRunFS_ListChangesPassthrough(t *testing.T) {
	t.Parallel()

	inner := NewMemFS()
	inner.AddChange(Change{Path: "a.txt", Object: &RemoteObject{Path: "a.txt"}})
	logger, _ := logCapture(t)
	dry := NewDryRunFS(inner, logger)
	ctx := context.Background()

	ch, err := dry.ListChanges(ctx, "")
	if err != nil {
		t.Fatalf("ListChanges error: %v", err)
	}
	if len(ch.Items) != 1 {
		t.Errorf("ListChanges items = %d, want 1", len(ch.Items))
	}
}

func TestDryRunFS_QuotaPassthrough(t *testing.T) {
	t.Parallel()

	inner := NewMemFS()
	inner.SetQuota(5*1024*1024*1024, 15*1024*1024*1024)
	logger, _ := logCapture(t)
	dry := NewDryRunFS(inner, logger)
	ctx := context.Background()

	q, err := dry.Quota(ctx)
	if err != nil {
		t.Fatalf("Quota error: %v", err)
	}
	if q.Used != 5*1024*1024*1024 {
		t.Errorf("Quota used = %d, want 5 GB", q.Used)
	}
}

func TestDryRunFS_LogFormat(t *testing.T) {
	t.Parallel()

	inner := NewMemFS()
	logger, buf := logCapture(t)
	dry := NewDryRunFS(inner, logger)
	ctx := context.Background()

	_, _ = dry.CopyFile(ctx, "/local/file.txt", "remote")
	_ = dry.DeleteFile(ctx, "remote/file.txt")
	_ = dry.MoveFile(ctx, "src.txt", "dst.txt")

	// Verify all log lines are valid JSON.
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 log lines, got %d:\n%s", len(lines), buf.String())
	}
	for i, line := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Errorf("line %d is not valid JSON: %v\nline: %s", i, err, line)
		}
		// Every line should have dry_run=true.
		if dr, ok := m["dry_run"]; !ok || dr != true {
			t.Errorf("line %d missing dry_run=true: %s", i, line)
		}
	}
}

// assertLogContains is a test helper that checks the log buffer contains
// a substring.
func assertLogContains(t *testing.T, buf *bytes.Buffer, substr string) {
	t.Helper()
	if !strings.Contains(buf.String(), substr) {
		t.Errorf("log output does not contain %q:\n%s", substr, buf.String())
	}
}
