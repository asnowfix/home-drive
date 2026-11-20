package main

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/syncer"
)

// Issue #154 wiring: the trasher handed to the puller and bisync is wrapped
// by the delete guard, and the same guard is wired into the push syncer.
func TestAgent_LocalRemover_Wiring(t *testing.T) {
	a := &Agent{}
	if r := a.localRemover(); r != nil {
		t.Errorf("no trasher: localRemover = %v, want a true nil (deletes must be refused)", r)
	}
	a, _ = newIntegrationTestAgent(t)
	if a.deleteGuard == nil {
		t.Fatal("agent with a trasher has no delete guard")
	}
	if r, ok := a.localRemover().(*syncer.DeleteGuard); !ok || r != a.deleteGuard {
		t.Errorf("localRemover = %v, want the agent's delete guard", a.localRemover())
	}
	// The puller and bisync must delete through the guard, not the bare
	// trasher (fails if buildPuller/buildBisync revert to a.trasher).
	if got := a.puller.LocalRemover(); got != syncer.LocalRemover(a.deleteGuard) {
		t.Errorf("puller remover = %T, want the delete guard", got)
	}
	if got := a.bisync.LocalRemover(); got != syncer.LocalRemover(a.deleteGuard) {
		t.Errorf("bisync remover = %T, want the delete guard", got)
	}
}

// captureAudit records the push syncer's audit entries.
type captureAudit struct {
	mu      sync.Mutex
	entries []syncer.AuditEntry
}

func (c *captureAudit) Log(e syncer.AuditEntry) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = append(c.entries, e)
	return nil
}

func (c *captureAudit) count(op string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, e := range c.entries {
		if e.Op == op {
			n++
		}
	}
	return n
}

// End to end with the real watcher, the real trasher wrapped by the guard and
// the real push syncer: a directory the agent removes is not echoed to the
// remote, a directory the user removes is. Fails if buildPushSyncer drops
// WithDeleteGuard.
func TestAgent_AgentDirRemoval_NotEchoed(t *testing.T) {
	audit := &captureAudit{}
	a, remote := newIntegrationTestAgentAudit(t, audit)
	root := a.cfg.LocalRoot
	for _, d := range []string{"agent/sub", "user"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	runAgentInBackground(t, a)

	// Wait until the watcher is live (and has walked the directories above).
	probe := filepath.Join(root, "probe.txt")
	deadline := time.After(5 * time.Second)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for !remote.HasCopiedFrom(probe) {
		select {
		case <-tick.C:
			if err := os.WriteFile(probe, []byte("p"), 0o644); err != nil {
				t.Fatal(err)
			}
		case <-deadline:
			t.Fatal("watcher never became live")
		}
	}

	// The agent removes the leaf then its parent, as the puller does.
	for _, d := range []string{"agent/sub", "agent"} {
		if err := a.deleteGuard.Remove(filepath.Join(root, d)); err != nil {
			t.Fatalf("agent remove %s: %v", d, err)
		}
	}
	if err := os.Remove(filepath.Join(root, "user")); err != nil {
		t.Fatal(err)
	}

	// Positive waits: the user's removal is pushed, and both of the agent's
	// removals were seen by the push side and suppressed (audit lines).
	deadline = time.After(5 * time.Second)
	for !remote.hasDeleted("user") || audit.count("remove_suppressed") < 2 {
		select {
		case <-tick.C:
		case <-deadline:
			t.Fatalf("timed out: user push=%v, remove_suppressed=%d (want 2)",
				remote.hasDeleted("user"), audit.count("remove_suppressed"))
		}
	}
	for _, d := range []string{"agent/sub", "agent"} {
		if remote.hasDeleted(d) {
			t.Errorf("agent's own removal of %s was echoed to Drive", d)
		}
	}
}
