package store

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"
)

// AuditEntry represents a single line in the JSONL audit log.
type AuditEntry struct {
	Timestamp  time.Time `json:"ts"`
	Op         string    `json:"op"`
	Path       string    `json:"path"`
	Resolution string    `json:"resolution,omitempty"`
	OldPath    string    `json:"old_path,omitempty"`
	NewPath    string    `json:"new_path,omitempty"`
	FilesCount int       `json:"files_count,omitempty"`
	DryRun     bool      `json:"dry_run,omitempty"`
	Error      string    `json:"error,omitempty"`
}

// Auditor appends JSONL lines to a writer (typically a file).
// It is safe for concurrent use.
type Auditor struct {
	mu     sync.Mutex
	w      io.Writer
	logger *slog.Logger
	now    func() time.Time // injectable clock
}

// AuditorOption configures the Auditor.
type AuditorOption func(*Auditor)

// WithClock sets a custom clock function for the auditor.
func WithClock(now func() time.Time) AuditorOption {
	return func(a *Auditor) {
		a.now = now
	}
}

// NewAuditor creates an auditor that writes JSONL to the given writer.
func NewAuditor(w io.Writer, logger *slog.Logger, opts ...AuditorOption) *Auditor {
	a := &Auditor{
		w:      w,
		logger: logger,
		now:    time.Now,
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// Log appends an audit entry to the JSONL output. If the entry has no
// timestamp set, the current time is used.
func (a *Auditor) Log(entry AuditEntry) {
	if entry.Timestamp.IsZero() {
		entry.Timestamp = a.now()
	}

	data, err := json.Marshal(entry)
	if err != nil {
		a.logger.Error("audit: marshal failed",
			"op", entry.Op,
			"path", entry.Path,
			"error", err,
		)
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	// Append newline to make it valid JSONL.
	data = append(data, '\n')
	if _, err := a.w.Write(data); err != nil {
		a.logger.Error("audit: write failed",
			"op", entry.Op,
			"path", entry.Path,
			"error", err,
		)
	}
}

// LogOp is a convenience method that logs a simple operation entry.
func (a *Auditor) LogOp(op, path string) {
	a.Log(AuditEntry{Op: op, Path: path})
}

// LogError logs an operation that failed.
func (a *Auditor) LogError(op, path string, err error) {
	a.Log(AuditEntry{
		Op:    op,
		Path:  path,
		Error: fmt.Sprintf("%v", err),
	})
}
