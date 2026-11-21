package syncer

import (
	"errors"
	"testing"
	"time"
)

// bisyncCompleted returns the FilesChanged of the last bisync.completed event.
func bisyncCompleted(t *testing.T, w *guardWorld) int {
	t.Helper()
	for i := len(w.mqtt.published) - 1; i >= 0; i-- {
		if ev, ok := w.mqtt.published[i].(BisyncEvent); ok && ev.Type == "bisync.completed" {
			return ev.FilesChanged
		}
	}
	t.Fatal("no bisync.completed event published")
	return 0
}

// TestBisyncRecorder_DeletesConflictsAndPass: deletions, delete-vs-modify
// conflicts and the pass file count reach the recorder consistently with the
// bisync.completed event.
func TestBisyncRecorder_DeletesConflictsAndPass(t *testing.T) {
	w := newGuardWorld(t, false)
	rec := &fakeRecorder{}
	w.b.rec = rec
	seedBystander(t, w.root, w.remote, w.journal)
	w.goneFromDrive("a", 1) // pull-side delete
	w.goneLocally("b", 1)   // push-side delete
	// Deleted on Drive but modified locally: delete-vs-modify conflict,
	// settled by a push.
	createLocalFile(t, w.root, "mod.txt", deleteT0.Add(time.Minute))
	seedBaseline(w.journal, "mod.txt")

	w.run()

	if got := rec.byOp(OutPullDelete); len(got) != 1 || got[0].Err != nil || got[0].Dir != DirDown {
		t.Errorf("pull_delete outcomes = %+v", got)
	}
	if got := rec.byOp(OutPushDelete); len(got) != 1 || got[0].Err != nil || got[0].Dir != DirUp {
		t.Errorf("push_delete outcomes = %+v", got)
	}
	if got := rec.byOp(OutConflict); len(got) != 1 || got[0].Path != "mod.txt" {
		t.Errorf("conflict outcomes = %+v", got)
	}
	if got := rec.byOp(OutBisyncPush); len(got) != 0 {
		t.Errorf("a push settling a delete conflict must count in conflicts only: %+v", got)
	}
	pass := rec.byOp(OutBisync)
	if len(pass) != 1 {
		t.Fatalf("pass outcomes = %+v", pass)
	}
	if ev := bisyncCompleted(t, w); pass[0].Files != ev || ev != 3 {
		t.Errorf("pass files = %d, event files_changed = %d, want both 3", pass[0].Files, ev)
	}
}

func TestBisyncRecorder_FailedDeleteIsRecorded(t *testing.T) {
	w := newGuardWorld(t, false)
	rec := &fakeRecorder{}
	w.b.rec = rec
	seedBystander(t, w.root, w.remote, w.journal)
	w.goneLocally("b", 1)
	w.remote.delErr = func(string) error { return errors.New("drive 503") }

	w.run()

	got := rec.byOp(OutPushDelete)
	if len(got) != 1 || got[0].Err == nil {
		t.Errorf("push_delete outcomes = %+v, want one failure", got)
	}
}

func TestBisyncRecorder_DryRunRecordsNothing(t *testing.T) {
	w := newGuardWorld(t, true)
	rec := &fakeRecorder{}
	w.b.rec = rec
	seedBystander(t, w.root, w.remote, w.journal)
	w.goneFromDrive("a", 1)
	w.goneLocally("b", 1)

	w.run()

	for _, op := range []string{OutPullDelete, OutPushDelete, OutConflict} {
		if got := rec.byOp(op); len(got) != 0 {
			t.Errorf("dry run recorded %s: %+v", op, got)
		}
	}
}
