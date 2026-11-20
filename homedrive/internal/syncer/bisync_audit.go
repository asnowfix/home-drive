// bisync_audit.go holds the bisync audit-log writers: the per-pass summary
// line and the per-file push/pull/conflict lines (issue #146: a run summary
// alone could not say which files a pass touched).
package syncer

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/store"
)

// writeAudit appends a JSONL line to the audit log.
func (b *Bisync) writeAudit(
	start time.Time,
	pushed, pulled, conflicts int,
	syncErr error,
) {
	if b.audit == nil {
		return
	}

	elapsed := b.clock.Now().Sub(start)
	entry := BisyncAuditEntry{
		Timestamp:    start.UTC().Format(time.RFC3339),
		Op:           "bisync",
		Duration:     fmt.Sprintf("%d", elapsed.Milliseconds()),
		FilesChanged: pushed + pulled + b.deleted,
		FilesPushed:  pushed,
		FilesPulled:  pulled,
		Conflicts:    conflicts,
		FilesDeleted: b.deleted,
		DryRun:       b.cfg.DryRun,
	}
	if syncErr != nil {
		entry.Error = syncErr.Error()
	}

	data, err := json.Marshal(entry)
	if err != nil {
		b.log.Error("failed to marshal audit entry", "error", err)
		return
	}

	line := string(data) + "\n"
	if _, err := b.audit.Write([]byte(line)); err != nil {
		b.log.Error("failed to write audit entry", "error", err)
	}
}

// auditFile appends one per-file line (op "push", "pull" or "conflict") to
// the audit log. No-op when no Auditor is configured. oldPath and resolution
// are set for conflicts only.
func (b *Bisync) auditFile(op, path, resolution, oldPath string) {
	if b.auditor == nil {
		return
	}
	b.auditor.Log(store.AuditEntry{
		Timestamp:  b.clock.Now(),
		Op:         op,
		Path:       path,
		Resolution: resolution,
		OldPath:    oldPath,
		DryRun:     b.cfg.DryRun,
	})
}
