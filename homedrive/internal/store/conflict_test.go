package store

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestResolver(t *testing.T, policy ConflictPolicy) (*ConflictResolver, *Journal, *bytes.Buffer) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	j, err := OpenJournal(dbPath, logger)
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	t.Cleanup(func() { j.Close() })

	var auditBuf bytes.Buffer
	auditor := NewAuditor(&auditBuf, logger)

	resolver := NewConflictResolver(j, auditor, logger, policy)
	return resolver, j, &auditBuf
}

func TestConflictResolver_LocalNewer(t *testing.T) {
	resolver, _, auditBuf := newTestResolver(t, PolicyNewerWins)

	now := time.Date(2026, 4, 28, 14, 32, 0, 0, time.UTC)
	input := ConflictInput{
		Path:        "Documents/notes.md",
		LocalMtime:  now,
		RemoteMtime: now.Add(-15 * time.Second),
		LocalMD5:    "local-md5",
		RemoteMD5:   "remote-md5",
	}

	result, err := resolver.Resolve(input)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if result.Winner != SideLocal {
		t.Errorf("Winner = %q, want %q", result.Winner, SideLocal)
	}
	if result.LoserSide != SideRemote {
		t.Errorf("LoserSide = %q, want %q", result.LoserSide, SideRemote)
	}
	if result.OldPath != "Documents/notes.md.old.1" {
		t.Errorf("OldPath = %q, want %q", result.OldPath, "Documents/notes.md.old.1")
	}
	if result.Warning != "" {
		t.Errorf("Warning should be empty, got %q", result.Warning)
	}

	// Verify audit log was written.
	if !strings.Contains(auditBuf.String(), `"op":"conflict"`) {
		t.Errorf("audit log missing conflict entry: %s", auditBuf.String())
	}
}

func TestConflictResolver_RemoteNewer(t *testing.T) {
	resolver, _, _ := newTestResolver(t, PolicyNewerWins)

	now := time.Date(2026, 4, 28, 14, 32, 0, 0, time.UTC)
	input := ConflictInput{
		Path:        "Documents/report.pdf",
		LocalMtime:  now.Add(-30 * time.Second),
		RemoteMtime: now,
		LocalMD5:    "local-md5",
		RemoteMD5:   "remote-md5",
	}

	result, err := resolver.Resolve(input)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if result.Winner != SideRemote {
		t.Errorf("Winner = %q, want %q", result.Winner, SideRemote)
	}
	if result.LoserSide != SideLocal {
		t.Errorf("LoserSide = %q, want %q", result.LoserSide, SideLocal)
	}
	if result.OldPath != "Documents/report.pdf.old.1" {
		t.Errorf("OldPath = %q, want %q", result.OldPath, "Documents/report.pdf.old.1")
	}
}

func TestConflictResolver_EqualMtimeDiffChecksum(t *testing.T) {
	resolver, _, _ := newTestResolver(t, PolicyNewerWins)

	now := time.Date(2026, 4, 28, 14, 32, 0, 0, time.UTC)
	input := ConflictInput{
		Path:        "config.yaml",
		LocalMtime:  now,
		RemoteMtime: now,
		LocalMD5:    "aaa111",
		RemoteMD5:   "bbb222",
	}

	result, err := resolver.Resolve(input)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// Default: local wins on equal mtime.
	if result.Winner != SideLocal {
		t.Errorf("Winner = %q, want %q (local wins on equal mtime by default)", result.Winner, SideLocal)
	}
	if result.Warning == "" {
		t.Error("Warning should be non-empty for equal-mtime case")
	}
	if result.OldPath != "config.yaml.old.1" {
		t.Errorf("OldPath = %q, want %q", result.OldPath, "config.yaml.old.1")
	}
}

func TestConflictResolver_OldNIncrementing(t *testing.T) {
	resolver, j, _ := newTestResolver(t, PolicyNewerWins)

	now := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)
	basePath := "shared/data.csv"

	// Simulate previous conflicts: .old.1 and .old.2 already exist.
	for _, n := range []int{1, 2} {
		oldPath := basePath + ".old." + strings.Replace("N", "N", string(rune('0'+n)), 1)
		// Use the proper format.
		oldPath = basePath + ".old." + itoa(n)
		if err := j.Put(JournalEntry{Path: oldPath, LastOrigin: "local"}); err != nil {
			t.Fatalf("seed .old.%d: %v", n, err)
		}
	}

	input := ConflictInput{
		Path:        basePath,
		LocalMtime:  now,
		RemoteMtime: now.Add(-5 * time.Second),
		LocalMD5:    "local",
		RemoteMD5:   "remote",
	}

	result, err := resolver.Resolve(input)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if result.OldPath != "shared/data.csv.old.3" {
		t.Errorf("OldPath = %q, want %q", result.OldPath, "shared/data.csv.old.3")
	}
}

func TestConflictResolver_OldNEdge_MultipleExisting(t *testing.T) {
	resolver, j, _ := newTestResolver(t, PolicyNewerWins)

	basePath := "photos/sunset.jpg"
	now := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)

	// Pre-populate .old.1 through .old.5 in the journal.
	for n := 1; n <= 5; n++ {
		oldPath := basePath + ".old." + itoa(n)
		if err := j.Put(JournalEntry{Path: oldPath, LastOrigin: "remote"}); err != nil {
			t.Fatalf("seed .old.%d: %v", n, err)
		}
	}

	input := ConflictInput{
		Path:        basePath,
		LocalMtime:  now,
		RemoteMtime: now.Add(-1 * time.Second),
	}

	result, err := resolver.Resolve(input)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if result.OldPath != "photos/sunset.jpg.old.6" {
		t.Errorf("OldPath = %q, want %q", result.OldPath, "photos/sunset.jpg.old.6")
	}
}

func TestConflictResolver_PolicyLocalWins(t *testing.T) {
	resolver, _, _ := newTestResolver(t, PolicyLocalWins)

	now := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)
	input := ConflictInput{
		Path:        "file.txt",
		LocalMtime:  now.Add(-10 * time.Second), // local is older
		RemoteMtime: now,                         // remote is newer
	}

	result, err := resolver.Resolve(input)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// Despite remote being newer, local wins because of policy.
	if result.Winner != SideLocal {
		t.Errorf("Winner = %q, want %q", result.Winner, SideLocal)
	}
}

func TestConflictResolver_PolicyRemoteWins(t *testing.T) {
	resolver, _, _ := newTestResolver(t, PolicyRemoteWins)

	now := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)
	input := ConflictInput{
		Path:        "file.txt",
		LocalMtime:  now,                          // local is newer
		RemoteMtime: now.Add(-10 * time.Second),   // remote is older
	}

	result, err := resolver.Resolve(input)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// Despite local being newer, remote wins because of policy.
	if result.Winner != SideRemote {
		t.Errorf("Winner = %q, want %q", result.Winner, SideRemote)
	}
}

func TestConflictResolver_UnknownPolicy(t *testing.T) {
	resolver, _, _ := newTestResolver(t, "invalid_policy")

	input := ConflictInput{
		Path:        "file.txt",
		LocalMtime:  time.Now(),
		RemoteMtime: time.Now(),
	}

	_, err := resolver.Resolve(input)
	if err == nil {
		t.Fatal("expected error for unknown policy")
	}
	if !strings.Contains(err.Error(), "unknown conflict policy") {
		t.Errorf("error = %q, want to contain 'unknown conflict policy'", err.Error())
	}
}

func TestConflictResolver_AuditLogContents(t *testing.T) {
	resolver, _, auditBuf := newTestResolver(t, PolicyNewerWins)

	now := time.Date(2026, 4, 28, 14, 32, 0, 0, time.UTC)
	input := ConflictInput{
		Path:        "Documents/notes.md",
		LocalMtime:  now,
		RemoteMtime: now.Add(-15 * time.Second),
	}

	_, err := resolver.Resolve(input)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// Parse the audit entry.
	var auditEntry AuditEntry
	if err := json.Unmarshal([]byte(strings.TrimSpace(auditBuf.String())), &auditEntry); err != nil {
		t.Fatalf("parse audit entry: %v\nraw: %s", err, auditBuf.String())
	}

	if auditEntry.Op != "conflict" {
		t.Errorf("audit Op = %q, want %q", auditEntry.Op, "conflict")
	}
	if auditEntry.Path != "Documents/notes.md" {
		t.Errorf("audit Path = %q, want %q", auditEntry.Path, "Documents/notes.md")
	}
	if auditEntry.Resolution != "newer_wins:local" {
		t.Errorf("audit Resolution = %q, want %q", auditEntry.Resolution, "newer_wins:local")
	}
	if auditEntry.OldPath != "Documents/notes.md.old.1" {
		t.Errorf("audit OldPath = %q, want %q", auditEntry.OldPath, "Documents/notes.md.old.1")
	}
}

func TestConflictResolver_SequentialConflicts(t *testing.T) {
	resolver, _, _ := newTestResolver(t, PolicyNewerWins)

	now := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)
	basePath := "evolving.txt"

	// Simulate 3 sequential conflicts on the same file.
	for i := 1; i <= 3; i++ {
		input := ConflictInput{
			Path:        basePath,
			LocalMtime:  now.Add(time.Duration(i) * time.Minute),
			RemoteMtime: now,
		}

		result, err := resolver.Resolve(input)
		if err != nil {
			t.Fatalf("Resolve #%d: %v", i, err)
		}

		expected := basePath + ".old." + itoa(i)
		if result.OldPath != expected {
			t.Errorf("conflict #%d: OldPath = %q, want %q", i, result.OldPath, expected)
		}
	}
}

// itoa converts a small integer to a string without importing strconv.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
