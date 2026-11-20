package syncer

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/asnowfix/home-drive/homedrive/internal/store"
)

// issue #156: bisync mass-deletion guard.

// guardWorld is a bisync run wired to mocks, plus helpers that build the
// three file populations the guard cares about.
type guardWorld struct {
	t       *testing.T
	root    string
	b       *Bisync
	remote  *mockRemoteFS
	journal *mockJournal
	mqtt    *mockMQTT
	audit   *threadSafeBuffer
}

func newGuardWorld(t *testing.T, dryRun bool) *guardWorld {
	t.Helper()
	root := t.TempDir()
	b, _, remote, journal, mqtt, _ := newTestBisync(t, root, dryRun)
	buf := &threadSafeBuffer{}
	b.auditor = store.NewAuditor(buf, newDiscardLogger())
	return &guardWorld{t: t, root: root, b: b, remote: remote, journal: journal, mqtt: mqtt, audit: buf}
}

func guardName(prefix string, i int) string { return fmt.Sprintf("%s%03d.txt", prefix, i) }

// synced adds n files present on both sides and journaled.
func (w *guardWorld) synced(prefix string, n int) {
	for i := 0; i < n; i++ {
		rel := guardName(prefix, i)
		createLocalFile(w.t, w.root, rel, deleteT0)
		w.remote.Seed(rel, deleteT0, "m0")
		seedBaseline(w.journal, rel)
	}
}

// goneFromDrive adds n journaled files that exist locally only: Drive
// deleted them, so bisync plans local deletions.
func (w *guardWorld) goneFromDrive(prefix string, n int) {
	for i := 0; i < n; i++ {
		rel := guardName(prefix, i)
		createLocalFile(w.t, w.root, rel, deleteT0)
		seedBaseline(w.journal, rel)
	}
}

// goneLocally adds n journaled files that exist on Drive only: they were
// deleted locally, so bisync plans Drive deletions.
func (w *guardWorld) goneLocally(prefix string, n int) {
	for i := 0; i < n; i++ {
		rel := guardName(prefix, i)
		w.remote.Seed(rel, deleteT0, "m0")
		seedBaseline(w.journal, rel)
	}
}

// seedBystander makes one file present and in sync on both sides, so neither
// side is empty: tests of single deletions must not trip the empty-side rule.
func seedBystander(t *testing.T, root string, remote *mockRemoteFS, journal *mockJournal) {
	t.Helper()
	createLocalFile(t, root, "bystander.txt", deleteT0)
	remote.Seed("bystander.txt", deleteT0, "m0")
	seedBaseline(journal, "bystander.txt")
}

func (w *guardWorld) run() { w.b.execute(context.Background()) }

func (w *guardWorld) localFiles() int {
	n := 0
	_ = filepath.WalkDir(w.root, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}

// blockedAudit returns the "delete_blocked" audit lines.
func (w *guardWorld) blockedAudit() []store.AuditEntry {
	var out []store.AuditEntry
	for _, l := range parseAuditLines(w.t, w.audit) {
		if l.Op == "delete_blocked" {
			out = append(out, l)
		}
	}
	return out
}

// blockedEvents returns the published delete_blocked MQTT payloads.
func (w *guardWorld) blockedEvents() []DeleteBlockedEvent {
	var out []DeleteBlockedEvent
	for _, p := range w.mqtt.published {
		if ev, ok := p.(DeleteBlockedEvent); ok {
			out = append(out, ev)
		}
	}
	return out
}

func TestBisyncGuard_Threshold(t *testing.T) {
	tests := []struct {
		name string
		// bystanders are synced on both sides; drive/local are the planned
		// deletions in each direction.
		bystanders, goneDrive, goneLocal int
		wantBlocked                      bool
		wantThreshold                    int
	}{
		{name: "floor_never_blocks_small_journal", bystanders: 40, goneDrive: 10},
		{name: "above_floor_on_small_journal_blocks_on_pct", bystanders: 40, goneDrive: 11, wantBlocked: true, wantThreshold: 10},
		{name: "pct_bound_allows_at_limit", bystanders: 180, goneDrive: 20},
		{name: "pct_bound_blocks_above", bystanders: 179, goneDrive: 21, wantBlocked: true, wantThreshold: 20},
		{name: "count_bound_allows_at_limit", bystanders: 1950, goneDrive: 50},
		{name: "count_bound_blocks_above", bystanders: 1949, goneDrive: 51, wantBlocked: true, wantThreshold: 50},
		{name: "drive_side_deletions_counted", bystanders: 179, goneLocal: 21, wantBlocked: true, wantThreshold: 20},
		{name: "both_directions_counted_together", bystanders: 84, goneDrive: 8, goneLocal: 8, wantBlocked: true, wantThreshold: 10},
		{name: "no_deletions_is_quiet", bystanders: 30},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := newGuardWorld(t, false)
			w.synced("keep", tc.bystanders)
			w.goneFromDrive("gd", tc.goneDrive)
			w.goneLocally("gl", tc.goneLocal)
			planned := tc.goneDrive + tc.goneLocal
			wantLocal := tc.bystanders + tc.goneDrive

			w.run()

			if tc.wantBlocked {
				assertBlocked(t, w, planned, tc.wantThreshold, BlockThreshold)
				if got := w.localFiles(); got != wantLocal {
					t.Errorf("local files = %d, want %d untouched", got, wantLocal)
				}
				if n := len(w.remote.getDeleteCalls()); n != 0 {
					t.Errorf("remote deletes = %d, want 0", n)
				}
				for i := 0; i < tc.goneDrive; i++ {
					if !w.journal.Exists(guardName("gd", i)) {
						t.Fatalf("journal entry %s dropped while blocked", guardName("gd", i))
					}
				}
				for i := 0; i < tc.goneLocal; i++ {
					if !w.journal.Exists(guardName("gl", i)) {
						t.Fatalf("journal entry %s dropped while blocked", guardName("gl", i))
					}
				}
				return
			}
			if w.b.DeleteBlock() != nil || len(w.blockedAudit()) != 0 || len(w.blockedEvents()) != 0 {
				t.Errorf("guard reported a block for %d deletions", planned)
			}
			if got := w.localFiles(); got != tc.bystanders {
				t.Errorf("local files = %d, want %d after deletions", got, tc.bystanders)
			}
			if n := len(w.remote.getDeleteCalls()); n != tc.goneLocal {
				t.Errorf("remote deletes = %d, want %d", n, tc.goneLocal)
			}
		})
	}
}

func assertBlocked(t *testing.T, w *guardWorld, planned, threshold int, reason string) {
	t.Helper()
	blk := w.b.DeleteBlock()
	if blk == nil || blk.Planned != planned || blk.Threshold != threshold || blk.Reason != reason ||
		blk.Since.IsZero() {
		t.Errorf("status block = %+v, want planned %d threshold %d reason %s with a since",
			blk, planned, threshold, reason)
	}
	lines := w.blockedAudit()
	if len(lines) != 1 || lines[0].FilesCount != planned || lines[0].Threshold != threshold ||
		lines[0].Reason != reason || len(lines[0].Paths) != min(planned, blockedSampleSize) {
		t.Errorf("audit delete_blocked lines = %+v, want one (planned %d, threshold %d, reason %s, %d paths)",
			lines, planned, threshold, reason, min(planned, blockedSampleSize))
	}
	evs := w.blockedEvents()
	if len(evs) != 1 || evs[0].Type != "bisync.delete_blocked" || evs[0].Planned != planned ||
		evs[0].Threshold != threshold || evs[0].Reason != reason {
		t.Errorf("mqtt events = %+v, want one bisync.delete_blocked (planned %d)", evs, planned)
	}
}

// Pushes, pulls and the other files of a blocked run go ahead.
func TestBisyncGuard_BlockedRunStillPushesAndPulls(t *testing.T) {
	w := newGuardWorld(t, false)
	w.synced("keep", 40)
	w.goneFromDrive("gd", 15)
	createLocalFile(t, w.root, "new-local.txt", deleteT0)
	w.remote.Seed("new-remote.txt", deleteT0, "mx")

	w.run()

	assertBlocked(t, w, 15, 10, BlockThreshold)
	if w.remote.CopyCount() != 1 || len(w.remote.downloadedFiles) != 1 {
		t.Errorf("copies = %d, downloads = %d, want 1 and 1 while blocked",
			w.remote.CopyCount(), len(w.remote.downloadedFiles))
	}
	if !w.journal.Exists("new-local.txt") || !w.journal.Exists("new-remote.txt") {
		t.Error("pushed and pulled files not journaled")
	}
}

// Mandatory case 1: an empty or unmounted local_root with a populated
// journal must not send the whole tree to the Drive trash.
func TestBisyncGuard_EmptyLocalRoot(t *testing.T) {
	for _, journaled := range []int{15, 3} { // above and below the floor
		t.Run(fmt.Sprintf("journaled_%d", journaled), func(t *testing.T) {
			w := newGuardWorld(t, false)
			w.goneLocally("f", journaled)

			w.run()

			assertBlocked(t, w, journaled, 0, BlockEmptyLocal)
			if n := len(w.remote.getDeleteCalls()); n != 0 {
				t.Errorf("remote deletes = %d, want 0 for an empty local root", n)
			}
			for i := 0; i < journaled; i++ {
				if !w.remote.HasFile(guardName("f", i)) || !w.journal.Exists(guardName("f", i)) {
					t.Fatalf("%s lost its Drive copy or journal entry", guardName("f", i))
				}
			}
		})
	}
}

// Mandatory case 2: an empty listing (root replaced) or a listing from a
// different root or account must not delete the local copies.
func TestBisyncGuard_EmptyOrDifferentRemote(t *testing.T) {
	tests := []struct {
		name       string
		journaled  int
		foreign    int // unrelated files on the other Drive
		wantReason string
		wantThresh int
	}{
		{name: "empty_listing_above_floor", journaled: 15, wantReason: BlockEmptyRemote},
		{name: "empty_listing_below_floor", journaled: 3, wantReason: BlockEmptyRemote},
		{name: "listing_of_another_account", journaled: 30, foreign: 5, wantReason: BlockThreshold, wantThresh: 10},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := newGuardWorld(t, false)
			w.goneFromDrive("f", tc.journaled)
			for i := 0; i < tc.foreign; i++ {
				w.remote.Seed(guardName("other", i), deleteT0, "mz")
			}

			w.run()

			assertBlocked(t, w, tc.journaled, tc.wantThresh, tc.wantReason)
			for i := 0; i < tc.journaled; i++ {
				if !w.journal.Exists(guardName("f", i)) {
					t.Fatalf("journal entry %s dropped", guardName("f", i))
				}
			}
			if got := w.localFiles(); got != tc.journaled+tc.foreign {
				t.Errorf("local files = %d, want %d (journaled kept, foreign files pulled)",
					got, tc.journaled+tc.foreign)
			}
		})
	}
}

// A blocked run leaves the journal alone, so the next run plans the same
// deletions again and the status keeps the original "since".
func TestBisyncGuard_NextRunPlansSameDeletions(t *testing.T) {
	w := newGuardWorld(t, false)
	w.synced("keep", 40)
	w.goneFromDrive("gd", 15)

	w.run()
	first := w.b.DeleteBlock()
	w.b.clock.(*mockClock).Advance(3600e9)
	w.run()
	second := w.b.DeleteBlock()

	if first == nil || second == nil || second.Planned != first.Planned || !second.Since.Equal(first.Since) {
		t.Errorf("blocks = %+v then %+v, want the same planned count and since", first, second)
	}
	if got := len(w.blockedAudit()); got != 2 {
		t.Errorf("audit delete_blocked lines = %d, want one per blocked run (2)", got)
	}
}

// The override allows exactly one run, then resets; the block clears.
func TestBisyncGuard_OneShotOverride(t *testing.T) {
	w := newGuardWorld(t, false)
	w.synced("keep", 100)
	w.goneFromDrive("a", 60)

	w.run()
	assertBlocked(t, w, 60, 16, BlockThreshold)

	w.b.AllowNextDeletes()
	w.run()
	if got := w.localFiles(); got != 100 {
		t.Errorf("local files after override = %d, want 100 (60 deleted)", got)
	}
	if w.b.DeleteBlock() != nil {
		t.Error("block not cleared after the overridden run")
	}

	// The override is spent: a second mass deletion is vetoed again.
	w.goneFromDrive("b", 60)
	w.run()
	if got := w.localFiles(); got != 160 {
		t.Errorf("local files after second run = %d, want 160 (override spent)", got)
	}
	if blk := w.b.DeleteBlock(); blk == nil || blk.Planned != 60 {
		t.Errorf("block = %+v, want planned 60", blk)
	}
}

// The override also lifts the empty-side rule, and a run with nothing to
// delete still consumes it (it must not linger for a later bad listing).
func TestBisyncGuard_OverrideEmptySideAndExpiry(t *testing.T) {
	t.Run("lifts_empty_remote", func(t *testing.T) {
		w := newGuardWorld(t, false)
		w.goneFromDrive("f", 3)
		w.b.AllowNextDeletes()

		w.run()

		if got := w.localFiles(); got != 0 {
			t.Errorf("local files = %d, want 0 after override", got)
		}
	})
	t.Run("consumed_by_a_run_without_deletions", func(t *testing.T) {
		w := newGuardWorld(t, false)
		w.synced("keep", 30)
		w.b.AllowNextDeletes()
		w.run() // nothing to delete: override spent here
		w.goneFromDrive("gd", 15)

		w.run()

		assertBlocked(t, w, 15, 10, BlockThreshold)
	})
}

// Dry-run reports what would be blocked, deletes nothing either way, and
// does not consume the override.
func TestBisyncGuard_DryRun(t *testing.T) {
	w := newGuardWorld(t, true)
	w.synced("keep", 40)
	w.goneFromDrive("gd", 15)

	w.run()

	assertBlocked(t, w, 15, 10, BlockThreshold)
	if ev := w.blockedEvents()[0]; !ev.DryRun {
		t.Error("dry-run event not flagged dry_run")
	}
	if l := w.blockedAudit()[0]; !l.DryRun {
		t.Error("dry-run audit line not flagged dry_run")
	}
	for _, l := range parseAuditLines(t, w.audit) {
		if l.Op == "pull_delete" {
			t.Errorf("dry-run reported a would-be deletion %s despite the block", l.Path)
		}
	}

	w.b.AllowNextDeletes()
	w.run()
	if !w.b.allowOnce {
		t.Error("dry-run consumed the override")
	}
	if len(w.blockedAudit()) != 1 {
		t.Error("dry-run with the override armed still reported a block")
	}
}

// The audit and MQTT samples are capped.
func TestBisyncGuard_SampleCapped(t *testing.T) {
	w := newGuardWorld(t, false)
	w.synced("keep", 10)
	w.goneFromDrive("gd", 70)

	w.run()

	if evs := w.blockedEvents(); len(evs) != 1 || len(evs[0].Paths) != blockedSampleSize || evs[0].Planned != 70 {
		t.Errorf("events = %+v, want planned 70 with %d paths", evs, blockedSampleSize)
	}
}

func TestBisyncGuard_DefaultsWhenUnset(t *testing.T) {
	b, _, _, _, _, _ := newTestBisync(t, t.TempDir(), false)
	if c, p := b.maxDelete(); c != DefaultMaxDeleteCount || p != DefaultMaxDeletePct {
		t.Errorf("defaults = %d, %d, want %d, %d", c, p, DefaultMaxDeleteCount, DefaultMaxDeletePct)
	}
	b.cfg.MaxDeleteCount, b.cfg.MaxDeletePct = 5, 3
	if c, p := b.maxDelete(); c != 5 || p != 3 {
		t.Errorf("configured = %d, %d, want 5, 3", c, p)
	}
}
