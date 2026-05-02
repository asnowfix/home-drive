package store

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAuditor_LogBasic(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	fixedTime := time.Date(2026, 4, 28, 14, 32, 0, 0, time.UTC)
	auditor := NewAuditor(&buf, logger, WithClock(func() time.Time { return fixedTime }))

	auditor.Log(AuditEntry{
		Op:   "push",
		Path: "docs/readme.md",
	})

	var entry AuditEntry
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &entry); err != nil {
		t.Fatalf("parse JSONL: %v\nraw: %s", err, buf.String())
	}

	if entry.Op != "push" {
		t.Errorf("Op = %q, want %q", entry.Op, "push")
	}
	if entry.Path != "docs/readme.md" {
		t.Errorf("Path = %q, want %q", entry.Path, "docs/readme.md")
	}
	if !entry.Timestamp.Equal(fixedTime) {
		t.Errorf("Timestamp = %v, want %v", entry.Timestamp, fixedTime)
	}
}

func TestAuditor_LogPreservesTimestamp(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	auditor := NewAuditor(&buf, logger)

	customTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	auditor.Log(AuditEntry{
		Timestamp: customTime,
		Op:        "pull",
		Path:      "file.txt",
	})

	var entry AuditEntry
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &entry); err != nil {
		t.Fatalf("parse JSONL: %v", err)
	}

	if !entry.Timestamp.Equal(customTime) {
		t.Errorf("Timestamp = %v, want custom time %v", entry.Timestamp, customTime)
	}
}

func TestAuditor_LogMultipleEntries(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	auditor := NewAuditor(&buf, logger)

	ops := []string{"push", "pull", "conflict", "delete", "dir_rename"}
	for _, op := range ops {
		auditor.Log(AuditEntry{Op: op, Path: "file.txt"})
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != len(ops) {
		t.Fatalf("got %d lines, want %d", len(lines), len(ops))
	}

	for i, line := range lines {
		var entry AuditEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("parse line %d: %v", i, err)
		}
		if entry.Op != ops[i] {
			t.Errorf("line %d: Op = %q, want %q", i, entry.Op, ops[i])
		}
	}
}

func TestAuditor_LogOp(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	auditor := NewAuditor(&buf, logger)

	auditor.LogOp("push", "data.csv")

	var entry AuditEntry
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &entry); err != nil {
		t.Fatalf("parse: %v", err)
	}

	if entry.Op != "push" {
		t.Errorf("Op = %q, want %q", entry.Op, "push")
	}
	if entry.Path != "data.csv" {
		t.Errorf("Path = %q, want %q", entry.Path, "data.csv")
	}
}

func TestAuditor_LogError(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	auditor := NewAuditor(&buf, logger)

	auditor.LogError("push", "fail.txt", ErrNotFound)

	var entry AuditEntry
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &entry); err != nil {
		t.Fatalf("parse: %v", err)
	}

	if entry.Op != "push" {
		t.Errorf("Op = %q, want %q", entry.Op, "push")
	}
	if entry.Error == "" {
		t.Error("Error field should be non-empty")
	}
}

func TestAuditor_ConflictFields(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	auditor := NewAuditor(&buf, logger)

	auditor.Log(AuditEntry{
		Op:         "conflict",
		Path:       "docs/notes.md",
		Resolution: "newer_wins:local",
		OldPath:    "docs/notes.md.old.3",
	})

	var entry AuditEntry
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &entry); err != nil {
		t.Fatalf("parse: %v", err)
	}

	if entry.Resolution != "newer_wins:local" {
		t.Errorf("Resolution = %q, want %q", entry.Resolution, "newer_wins:local")
	}
	if entry.OldPath != "docs/notes.md.old.3" {
		t.Errorf("OldPath = %q, want %q", entry.OldPath, "docs/notes.md.old.3")
	}
}

func TestAuditor_DirRenameFields(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	auditor := NewAuditor(&buf, logger)

	auditor.Log(AuditEntry{
		Op:         "dir_rename",
		Path:       "old_dir/",
		NewPath:    "new_dir/",
		FilesCount: 42,
	})

	var entry AuditEntry
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &entry); err != nil {
		t.Fatalf("parse: %v", err)
	}

	if entry.NewPath != "new_dir/" {
		t.Errorf("NewPath = %q, want %q", entry.NewPath, "new_dir/")
	}
	if entry.FilesCount != 42 {
		t.Errorf("FilesCount = %d, want 42", entry.FilesCount)
	}
}

func TestAuditor_ConcurrentWrites(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	auditor := NewAuditor(&buf, logger)

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(n int) {
			defer wg.Done()
			auditor.LogOp("push", "concurrent.txt")
		}(i)
	}

	wg.Wait()

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != goroutines {
		t.Errorf("got %d lines, want %d", len(lines), goroutines)
	}

	// Each line should be valid JSON.
	for i, line := range lines {
		var entry AuditEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Errorf("line %d is not valid JSON: %v\nraw: %s", i, err, line)
		}
	}
}

func TestAuditor_DryRunField(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	auditor := NewAuditor(&buf, logger)

	auditor.Log(AuditEntry{
		Op:     "push",
		Path:   "dryrun.txt",
		DryRun: true,
	})

	var entry AuditEntry
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &entry); err != nil {
		t.Fatalf("parse: %v", err)
	}

	if !entry.DryRun {
		t.Error("DryRun should be true")
	}
}

func TestAuditor_JSONLFormat(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	auditor := NewAuditor(&buf, logger)

	auditor.LogOp("push", "a.txt")
	auditor.LogOp("pull", "b.txt")

	raw := buf.String()

	// Must end with newline.
	if !strings.HasSuffix(raw, "\n") {
		t.Error("output should end with newline")
	}

	// Each line must be valid JSON (JSONL format).
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	for i, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Errorf("line %d is not valid JSON: %s", i, line)
		}
	}
}
