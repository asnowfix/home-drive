// root_guard.go is the local_root presence and identity guard (issue #170).
// The puller, the push syncer and bisync act on local_root: they download
// into it, trash from it and journal against it. If the data disk holding it
// is absent they would act on whatever sits at that path (the root
// filesystem, an empty mount point), so before each unit of work they ask
// the guard whether local_root is still the tree this agent has been syncing.
//
// Identity is a random id kept in two places: a marker file at the top of
// local_root (never synced, see pathfilter.RootMarker) and the state DB. The
// tree is ours only when both agree. Missing/dangling roots, a root replaced
// by an empty or foreign directory (marker absent or different) and an
// unreadable state all pause the sync; the pause lifts by itself on the next
// check that passes. A first install (no stored id, empty journal) records
// the identity instead of blocking, and an install that predates the guard
// adopts a tree that holds at least one journaled path (the same "empty side
// against a populated journal" reading as the bisync guard of #156).
package syncer

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/pathfilter"
	"github.com/asnowfix/home-drive/homedrive/internal/store"
)

// RootMarkerName is the identity file at the top of local_root.
const RootMarkerName = pathfilter.RootMarker

// keyRootID is the state DB meta key holding the recorded local_root id.
var keyRootID = []byte("local_root_id")

// ErrRootBlocked is returned by RootGuard.Check (and by the gated pull
// cycle) while local_root is not the tree being synced.
var ErrRootBlocked = errors.New("local_root is not the synced tree")

// Reasons local_root is blocked.
const (
	RootBlockMissing    = "missing"           // does not resolve to a directory
	RootBlockNoMarker   = "marker_missing"    // a directory, but not marked as ours
	RootBlockMismatch   = "identity_mismatch" // marked, with another id
	RootBlockUnreadable = "unreadable"        // marker or state could not be read/written
)

// RootGate is what the sync paths consult before acting. Check returns nil
// to proceed, or an error wrapping ErrRootBlocked to do nothing.
type RootGate interface {
	Check() error
}

// RootIdentityStore is the part of the state DB the guard uses; satisfied by
// *store.Journal.
type RootIdentityStore interface {
	GetMeta(key []byte) (string, error)
	SetMeta(key []byte, val string) error
	ForEach(fn func(JournalEntry) error) error
}

// RootBlock describes why local_root is blocked.
type RootBlock struct {
	Reason string
	Detail string
	Since  time.Time
}

// RootEvent is the MQTT payload on events/root/blocked and events/root/resumed.
type RootEvent struct {
	Timestamp string `json:"ts"`
	Type      string `json:"type"` // "root.blocked" or "root.resumed"
	Reason    string `json:"reason,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

// RootGuardConfig configures a RootGuard. Auditor, MQTT, Log and Clock are
// optional.
type RootGuardConfig struct {
	Root     string
	Identity RootIdentityStore
	Auditor  *Auditor
	MQTT     Publisher
	Log      *slog.Logger
	Clock    func() time.Time
	// DryRun records nothing (no marker, no stored id) but still evaluates.
	DryRun bool
}

// RootGuard is the local_root guard. Safe for concurrent use.
type RootGuard struct {
	cfg   RootGuardConfig
	mu    sync.Mutex
	block *RootBlock
}

// NewRootGuard creates a guard for cfg.Root.
func NewRootGuard(cfg RootGuardConfig) *RootGuard {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	return &RootGuard{cfg: cfg}
}

// Block returns the current block, or nil when local_root is the synced tree
// as of the last Check.
func (g *RootGuard) Block() *RootBlock {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.block == nil {
		return nil
	}
	cp := *g.block
	return &cp
}

// Check evaluates local_root now. It returns nil when the sync may act, or
// an error wrapping ErrRootBlocked. Block, log, audit line and MQTT event are
// emitted on transitions only, not on every blocked check.
func (g *RootGuard) Check() error {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	reason, detail := g.evaluate()
	g.transition(reason, detail)
	if reason == "" {
		return nil
	}
	return fmt.Errorf("%w: %s (%s)", ErrRootBlocked, reason, detail)
}

// evaluate returns an empty reason when local_root is the synced tree.
func (g *RootGuard) evaluate() (reason, detail string) {
	resolved, err := filepath.EvalSymlinks(g.cfg.Root)
	if err != nil {
		return RootBlockMissing, err.Error()
	}
	if fi, err := os.Stat(resolved); err != nil || !fi.IsDir() {
		return RootBlockMissing, "local_root does not resolve to a directory"
	}
	marker, err := readRootMarker(resolved)
	if err != nil {
		return RootBlockUnreadable, "reading marker: " + err.Error()
	}
	stored, err := g.cfg.Identity.GetMeta(keyRootID)
	if err != nil {
		return RootBlockUnreadable, "reading state: " + err.Error()
	}
	switch {
	case marker != "" && stored == "": // state DB reset, tree kept: adopt
		return g.record(marker)
	case marker != "" && marker != stored:
		return RootBlockMismatch, "marker id differs from the id recorded in the state DB"
	case marker != "":
		return "", ""
	case stored != "":
		return RootBlockNoMarker, "the marker is gone: local_root is empty, foreign or was not mounted"
	}
	return g.establish(resolved)
}

// record stores id in the state DB (unless dry-run).
func (g *RootGuard) record(id string) (reason, detail string) {
	if g.cfg.DryRun {
		return "", ""
	}
	if err := g.cfg.Identity.SetMeta(keyRootID, id); err != nil {
		return RootBlockUnreadable, "recording identity: " + err.Error()
	}
	return "", ""
}

// establish handles a root with neither marker nor stored id: a first install
// (empty journal) or an install that predates the guard (journaled paths must
// be present). Anything else is an empty or foreign tree.
func (g *RootGuard) establish(resolved string) (reason, detail string) {
	entries, present, err := g.scanJournal(resolved)
	if err != nil {
		return RootBlockUnreadable, "reading journal: " + err.Error()
	}
	if entries > 0 && !present {
		return RootBlockNoMarker, fmt.Sprintf(
			"the journal has %d entries but none exists under local_root", entries)
	}
	if g.cfg.DryRun {
		return "", ""
	}
	id, err := newRootID()
	if err != nil {
		return RootBlockUnreadable, "generating identity: " + err.Error()
	}
	if err := writeRootMarker(resolved, id); err != nil {
		return RootBlockUnreadable, "writing marker: " + err.Error()
	}
	return g.record(id)
}

// errScanDone stops the journal scan at the first journaled path found locally.
var errScanDone = errors.New("scan done")

// scanJournal counts journal entries until one exists under resolved.
func (g *RootGuard) scanJournal(resolved string) (entries int, present bool, err error) {
	err = g.cfg.Identity.ForEach(func(e JournalEntry) error {
		entries++
		if _, statErr := os.Lstat(filepath.Join(resolved, filepath.FromSlash(e.Path))); statErr == nil {
			present = true
			return errScanDone
		}
		return nil
	})
	if errors.Is(err, errScanDone) {
		err = nil
	}
	return entries, present, err
}

func readRootMarker(root string) (string, error) {
	b, err := os.ReadFile(filepath.Join(root, RootMarkerName))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return strings.TrimSpace(string(b)), err
}

func writeRootMarker(root, id string) error {
	f, err := os.OpenFile(filepath.Join(root, RootMarkerName), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(id + "\n"); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func newRootID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// transition reports a change of state: a new or changed block, or a
// recovery. Called with g.mu held.
func (g *RootGuard) transition(reason, detail string) {
	now := g.cfg.Clock()
	switch {
	case reason == "" && g.block != nil:
		g.cfg.Log.Info("local_root is the synced tree again, resuming pulls and pushes",
			"op", "root_guard", "was", g.block.Reason)
		g.report("root_resumed", "root.resumed", "events/root/resumed", g.block.Reason, "", now)
		g.block = nil
	case reason != "" && (g.block == nil || g.block.Reason != reason):
		since := now
		if g.block != nil {
			since = g.block.Since
		}
		g.block = &RootBlock{Reason: reason, Detail: detail, Since: since}
		g.cfg.Log.Error("local_root is not the synced tree: pulls and pushes paused",
			"op", "root_guard", "reason", reason, "detail", detail, "local_root", g.cfg.Root)
		g.report("root_blocked", "root.blocked", "events/root/blocked", reason, detail, now)
	}
}

// report writes the audit line and publishes the MQTT event.
func (g *RootGuard) report(auditOp, evType, topic, reason, detail string, now time.Time) {
	if g.cfg.Auditor != nil {
		g.cfg.Auditor.Log(store.AuditEntry{Timestamp: now, Op: auditOp, Reason: reason, Error: detail})
	}
	if g.cfg.MQTT == nil {
		return
	}
	ev := RootEvent{Timestamp: now.UTC().Format(time.RFC3339), Type: evType, Reason: reason, Detail: detail}
	if err := g.cfg.MQTT.PublishJSON(g.cfg.MQTT.Topic(strings.Split(topic, "/")...), ev); err != nil {
		g.cfg.Log.Error("failed to publish root guard event", "type", evType, "error", err)
	}
}
