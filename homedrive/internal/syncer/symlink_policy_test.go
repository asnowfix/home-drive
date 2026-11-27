package syncer

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// issue #191: the copy_resolved_links opt-in.

// copyLinkPatterns is the opt-in used by the fixture: links live under
// Brain/.claude/skills, their targets under Brain/shared.
var copyLinkPatterns = []string{"Brain/.claude/skills/**", "Brain/shared/**"}

// copyFixture builds a root with links of every kind under the matched
// folder and returns it. Files are one second apart in age so mtimes differ.
func copyFixture(t *testing.T) string {
	t.Helper()
	skipIfNoSymlinks(t)
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{
		"Brain/.claude/skills", "Brain/shared/dir/deep", "private", "elsewhere",
	} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(d)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	for rel, body := range map[string]string{
		"Brain/shared/tool.md":                   "tool",
		"Brain/shared/dir/a.txt":                 "a",
		"Brain/shared/dir/deep/b.txt":            "b",
		"private/secret.md":                      "secret",
		filepath.Join("..", "outside", "out.md"): "out",
	} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	links := map[string]string{
		"Brain/.claude/skills/tool.md":   "../../shared/tool.md",
		"Brain/.claude/skills/dirlink":   "../../shared/dir",
		"Brain/.claude/skills/unmatched": "../../../private/secret.md",
		"Brain/.claude/skills/out":       filepath.Join(outside, "out.md"),
		"Brain/.claude/skills/dangling":  "../../shared/missing",
		"Brain/shared/dir/nested":        "../tool.md", // inside a copied tree
		"elsewhere/link":                 "../Brain/shared/tool.md",
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(root, filepath.FromSlash(name))); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestDecideLink_Cases(t *testing.T) {
	root := copyFixture(t)
	pol := NewLinkPolicy(copyLinkPatterns)
	tests := []struct {
		name       string
		policy     *LinkPolicy
		rel        string
		sameDev    bool
		wantCopy   bool
		wantDir    bool
		wantReason string
	}{
		{"NilPolicy_DefaultUnchanged", nil, "Brain/.claude/skills/tool.md", true, false, false, LinkInRoot},
		{"EmptyPolicy_DefaultUnchanged", NewLinkPolicy(nil), "Brain/.claude/skills/dirlink", true, false, false, LinkInRoot},
		{"MatchingFileLink", pol, "Brain/.claude/skills/tool.md", true, true, false, ""},
		{"MatchingDirLink", pol, "Brain/.claude/skills/dirlink", true, true, true, ""},
		{"LinkNotUnderMatchedFolder", pol, "elsewhere/link", true, false, false, LinkInRoot},
		{"TargetNotUnderMatchedFolder", pol, "Brain/.claude/skills/unmatched", true, false, false, LinkTargetNotMatched},
		{"TargetOutsideRoot", pol, "Brain/.claude/skills/out", true, false, false, LinkOutsideRoot},
		{"Dangling", pol, "Brain/.claude/skills/dangling", true, false, false, LinkUnresolved},
		{"CrossDevice", pol, "Brain/.claude/skills/tool.md", false, false, false, LinkCrossDevice},
		{"CrossDevice_Dir", pol, "Brain/.claude/skills/dirlink", false, false, false, LinkCrossDevice},
		{"RegularFile", pol, "Brain/shared/tool.md", true, false, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			old := deviceCheck
			deviceCheck = func(a, b os.FileInfo) bool { return tt.sameDev && old(a, b) }
			t.Cleanup(func() { deviceCheck = old })

			dec := decideLink(root, filepath.Join(root, filepath.FromSlash(tt.rel)), tt.rel, tt.policy)
			if dec.copy != tt.wantCopy || dec.dir != tt.wantDir || dec.reason != tt.wantReason {
				t.Errorf("decideLink = {copy:%v dir:%v reason:%q}, want {copy:%v dir:%v reason:%q}",
					dec.copy, dec.dir, dec.reason, tt.wantCopy, tt.wantDir, tt.wantReason)
			}
			if tt.wantCopy && dec.target == "" {
				t.Error("copied link has no resolved target")
			}
		})
	}
}

func TestLinkPolicy_Covers_Cases(t *testing.T) {
	pol := NewLinkPolicy([]string{"Brain/.claude/skills/**", "docs/*"})
	tests := []struct {
		dir  string
		want bool
	}{
		{"Brain/.claude/skills", true},
		{"Brain/.claude/skills/x/y", true},
		{"Brain/.claude", false},
		{"Brain", false},
		{"docs/a", true},
		{"docs/a/b", true}, // under a matched folder
		{"docs", false},
		{".", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := pol.covers(tt.dir); got != tt.want {
			t.Errorf("covers(%q) = %v, want %v", tt.dir, got, tt.want)
		}
	}
	if NewLinkPolicy(nil).covers("Brain/.claude/skills") {
		t.Error("an empty policy covers nothing")
	}
}

func TestLinkPolicy_WarnUnmatched_OnceEach(t *testing.T) {
	root := copyFixture(t)
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	pol := NewLinkPolicy([]string{"Brain/.claude/skills/**", "Brain/shared/*", "Nope/**", "Nope/**"})

	pol.WarnUnmatched(context.Background(), root, log)

	if got := strings.Count(buf.String(), `"level":"WARN"`); got != 2 {
		t.Fatalf("WARN lines = %d, want 2 (one per unmatched list entry): %s", got, buf.String())
	}
	if !strings.Contains(buf.String(), `"pattern":"Nope/**"`) || strings.Contains(buf.String(), `"pattern":"Brain`) {
		t.Errorf("wrong patterns reported: %s", buf.String())
	}

	buf.Reset()
	NewLinkPolicy(nil).WarnUnmatched(context.Background(), root, log)
	NewLinkPolicy(copyLinkPatterns).WarnUnmatched(context.Background(), root, log)
	if buf.Len() != 0 {
		t.Errorf("disabled or fully matched policy must be silent: %s", buf.String())
	}
}

// A link event under the policy: file link pushed as content, dir link left
// to bisync, rejected link skipped with its reason.
func TestSyncer_CopyResolvedLinks_PushEvents(t *testing.T) {
	tests := []struct {
		name       string
		rel        string
		sameDev    bool
		wantCopied bool
		wantReason string
	}{
		{"FileLinkCopied", "Brain/.claude/skills/tool.md", true, true, ""},
		{"DirLinkLeftToBisync", "Brain/.claude/skills/dirlink", true, false, ""},
		{"CrossDeviceSkipped", "Brain/.claude/skills/tool.md", false, false, LinkCrossDevice},
		{"TargetNotMatchedSkipped", "Brain/.claude/skills/unmatched", true, false, LinkTargetNotMatched},
		{"OutsideRootSkipped", "Brain/.claude/skills/out", true, false, LinkOutsideRoot},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			old := deviceCheck
			deviceCheck = func(a, b os.FileInfo) bool { return tt.sameDev && old(a, b) }
			t.Cleanup(func() { deviceCheck = old })

			root := copyFixture(t)
			remote := newMockRemoteFS()
			var logBuf bytes.Buffer
			log := slog.New(slog.NewJSONHandler(&logBuf, nil))
			tr := NewSymlinkTracker(log)
			tr.SetPolicy(NewLinkPolicy(copyLinkPatterns))
			cfg := DefaultConfig()
			cfg.LocalRoot = root
			s := New(cfg, remote, newMockStore(), newMockAuditLogger(), newMockPublisher(), log,
				WithSleepFunc(func(context.Context, time.Duration) {}), WithSymlinkTracker(tr))

			events := make(chan Event, 1)
			events <- Event{Path: tt.rel, Op: OpWrite, At: time.Now()}
			runSyncer(t, s, events, make(chan DirRename), func() {})

			if got := len(remote.getCopyCalls()) == 1; got != tt.wantCopied {
				t.Errorf("copied = %v, want %v", got, tt.wantCopied)
			}
			wantCount := 0
			if tt.wantReason != "" {
				wantCount = 1
			}
			if tr.Count() != wantCount {
				t.Errorf("skipped count = %d, want %d", tr.Count(), wantCount)
			}
			if tt.wantReason != "" && !strings.Contains(logBuf.String(), `"reason":"`+tt.wantReason+`"`) {
				t.Errorf("ERROR line lacks reason %q: %s", tt.wantReason, logBuf.String())
			}
		})
	}
}
