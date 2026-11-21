package trash

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/store"
)

var testNow = time.Date(2026, 11, 20, 12, 0, 0, 0, time.Local)

// memRecords is an in-memory Recorder.
type memRecords struct {
	mu   sync.Mutex
	recs map[string]store.TrashRecord
}

func newMemRecords() *memRecords { return &memRecords{recs: map[string]store.TrashRecord{}} }

func (m *memRecords) PutTrashRecord(r store.TrashRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recs[r.Dir+"\x00"+r.Name] = r
	return nil
}

func (m *memRecords) DeleteTrashRecord(dir, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.recs, dir+"\x00"+name)
	return nil
}

func (m *memRecords) ListTrashRecords() ([]store.TrashRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.TrashRecord
	for _, r := range m.recs {
		out = append(out, r)
	}
	return out, nil
}

// env is a temp-dir world: a sync root and a separate "home" trash, a fake
// mount table making root a mount point, and a capturing audit log.
type env struct {
	t     *testing.T
	root  string // resolved local root (a mount point in the fake table)
	home  string // home trash directory
	recs  *memRecords
	audit *bytes.Buffer
	logs  *bytes.Buffer
	cfg   Config
}

func newEnv(t *testing.T) *env {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, root: filepath.Join(base, "root"), home: filepath.Join(base, "home", "Trash"),
		recs: newMemRecords(), audit: &bytes.Buffer{}, logs: &bytes.Buffer{}}
	if err := os.MkdirAll(e.root, 0o755); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(e.logs, nil))
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	e.cfg = Config{
		HomeTrash: e.home,
		UID:       1000,
		PurgeAge:  30 * 24 * time.Hour,
		Now:       func() time.Time { return testNow },
		Mountinfo: func() ([]byte, error) {
			return []byte("1 0 8:1 / / rw - ext4 /dev/sda1 rw\n" +
				"2 1 8:1 /data " + e.root + " rw - ext4 /dev/sda1 rw\n"), nil
		},
		Records:   e.recs,
		LocalRoot: e.root,
		Audit:   store.NewAuditor(e.audit, discard),
		Log:     log,
	}
	return e
}

func (e *env) trasher() *Trasher { return New(e.cfg) }

// write creates root-relative file rel with content.
func (e *env) write(rel, content string) string {
	e.t.Helper()
	p := filepath.Join(e.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
	return p
}

func (e *env) auditLines() []store.AuditEntry {
	e.t.Helper()
	var out []store.AuditEntry
	for _, l := range strings.Split(strings.TrimSpace(e.audit.String()), "\n") {
		if l == "" {
			continue
		}
		var a store.AuditEntry
		if err := json.Unmarshal([]byte(l), &a); err != nil {
			e.t.Fatalf("audit line %q: %v", l, err)
		}
		out = append(out, a)
	}
	return out
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// failHome returns a rename that fails with errno for any destination under
// dir and otherwise really renames.
func failUnder(dir string, errno syscall.Errno) func(string, string) error {
	return func(oldp, newp string) error {
		if strings.HasPrefix(newp, dir+string(filepath.Separator)) {
			return &os.LinkError{Op: "rename", Old: oldp, New: newp, Err: errno}
		}
		return os.Rename(oldp, newp)
	}
}
