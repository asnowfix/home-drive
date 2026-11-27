package syncer

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// issue #191: bisync with the copy_resolved_links opt-in.

func copyBisync(t *testing.T, root string, patterns []string) (*Bisync, *mockRemoteFS, *mockJournal, *bytes.Buffer) {
	t.Helper()
	b, _, remote, journal, _, _ := newTestBisync(t, root, false)
	buf := &bytes.Buffer{}
	b.links = NewSymlinkTracker(slog.New(slog.NewJSONHandler(buf, nil)))
	b.links.SetPolicy(NewLinkPolicy(patterns))
	return b, remote, journal, buf
}

func TestBisync_CopyResolvedLinks_DefaultUnchanged(t *testing.T) {
	root := copyFixture(t)
	b, remote, _, _ := copyBisync(t, root, nil)
	b.execute(context.Background())

	for _, p := range []string{"Brain/.claude/skills/tool.md", "Brain/.claude/skills/dirlink/a.txt"} {
		if remote.HasFile(p) {
			t.Errorf("%q uploaded with an empty policy", p)
		}
	}
	// 7 links in the fixture, every one skipped and reported.
	if got := b.links.Count(); got != 7 {
		t.Errorf("skipped links = %d, want 7", got)
	}
}

func TestBisync_CopyResolvedLinks_CopiesAndSkips(t *testing.T) {
	root := copyFixture(t)
	b, remote, _, buf := copyBisync(t, root, copyLinkPatterns)
	b.execute(context.Background())

	for _, p := range []string{
		"Brain/.claude/skills/tool.md",
		"Brain/.claude/skills/dirlink/a.txt",
		"Brain/.claude/skills/dirlink/deep/b.txt",
		"Brain/shared/tool.md", // the real files still sync on their own
		// A link of its own under a matched folder, to a matched target:
		// copied in place; only the same link seen through dirlink/ is not.
		"Brain/shared/dir/nested",
	} {
		if !remote.HasFile(p) {
			t.Errorf("%q was not copied to Drive", p)
		}
	}
	for _, p := range []string{
		"Brain/.claude/skills/unmatched", "Brain/.claude/skills/out",
		"Brain/.claude/skills/dangling", "Brain/.claude/skills/dirlink/nested",
		"Brain/.claude/skills/dirlink", "elsewhere/link",
	} {
		if remote.HasFile(p) {
			t.Errorf("%q must not be uploaded", p)
		}
	}
	// The nested link seen through the copied dir link is skipped under that
	// path; copied links are never reported as skipped.
	want := map[string]string{
		"Brain/.claude/skills/unmatched":      LinkTargetNotMatched,
		"Brain/.claude/skills/out":            LinkOutsideRoot,
		"Brain/.claude/skills/dangling":       LinkUnresolved,
		"Brain/.claude/skills/dirlink/nested": LinkInRoot,
		"elsewhere/link":                      LinkInRoot,
	}
	got := skippedReasons(t, buf)
	if len(got) != len(want) {
		t.Errorf("skipped = %v, want %v", got, want)
	}
	for p, r := range want {
		if got[p] != r {
			t.Errorf("reason for %q = %q, want %q", p, got[p], r)
		}
	}
	if b.links.Count() != len(want) {
		t.Errorf("tracker count = %d, want %d", b.links.Count(), len(want))
	}
}

// skippedReasons maps path -> reason from the skipped-symlink ERROR lines.
func skippedReasons(t *testing.T, buf *bytes.Buffer) map[string]string {
	t.Helper()
	out := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if !strings.Contains(line, "symlink skipped: not synced") {
			continue
		}
		var rec struct{ Path, Reason string }
		if err := json.Unmarshal([]byte(line), &rec); err != nil || rec.Reason == "" {
			t.Fatalf("bad ERROR line (%v): %s", err, line)
		}
		path, reason := rec.Path, rec.Reason
		out[path] = reason
	}
	return out
}

// Behind a copied link everything is push-only: no download, no write
// through the link, whatever Drive holds.
func TestBisync_CopyResolvedLinks_NeverWritesThrough(t *testing.T) {
	tests := []struct {
		name  string
		setup func(root string, remote *mockRemoteFS, j *mockJournal)
	}{
		{"RemoteOnlyUnderDirLink", func(_ string, r *mockRemoteFS, _ *mockJournal) {
			r.Seed("Brain/.claude/skills/dirlink/new.txt", deleteT0, "m1")
		}},
		{"RemoteNewerThanFileLink", func(root string, r *mockRemoteFS, j *mockJournal) {
			p := filepath.Join(root, "Brain/shared/tool.md")
			old := time.Now().Add(-48 * time.Hour)
			_ = os.Chtimes(p, old, old)
			r.Seed("Brain/.claude/skills/tool.md", time.Now(), "remote-md5")
			j.Seed(JournalEntry{Path: "Brain/.claude/skills/tool.md", LocalMtime: old,
				RemoteMtime: old.Add(-time.Hour), RemoteMD5: "base-md5"})
		}},
		{"RemoteNewerConflictUnderDirLink", func(root string, r *mockRemoteFS, j *mockJournal) {
			p := filepath.Join(root, "Brain/shared/dir/a.txt")
			old := time.Now().Add(-48 * time.Hour)
			_ = os.Chtimes(p, old, old)
			r.Seed("Brain/.claude/skills/dirlink/a.txt", time.Now(), "remote-md5")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := copyFixture(t)
			b, remote, journal, _ := copyBisync(t, root, copyLinkPatterns)
			tt.setup(root, remote, journal)
			before := snapshotTree(t, filepath.Join(root, "Brain/shared"))

			b.execute(context.Background())

			for _, d := range remote.downloadedFiles {
				if strings.HasPrefix(d.RemotePath, "Brain/.claude/skills/") {
					t.Errorf("downloaded %q behind a copied link to %q", d.RemotePath, d.LocalPath)
				}
			}
			after := snapshotTree(t, filepath.Join(root, "Brain/shared"))
			if strings.Join(before, "\n") != strings.Join(after, "\n") {
				t.Errorf("link target tree changed:\nbefore %v\nafter  %v", before, after)
			}
			fi, err := os.Lstat(filepath.Join(root, "Brain/.claude/skills/tool.md"))
			if err != nil || fi.Mode()&os.ModeSymlink == 0 {
				t.Errorf("the link itself was replaced: %v %v", fi, err)
			}
		})
	}
}

// With the opt-in removed, a Drive copy that an earlier run pushed through a
// directory link is left alone: neither deleted nor pulled into the target.
func TestBisync_SkippedDirLink_ProtectsRemoteChildren(t *testing.T) {
	root := copyFixture(t)
	b, remote, journal, _ := copyBisync(t, root, nil)
	seedBystander(t, root, remote, journal)
	remote.Seed("Brain/.claude/skills/dirlink/a.txt", deleteT0, "m0")
	seedBaseline(journal, "Brain/.claude/skills/dirlink/a.txt")

	b.execute(context.Background())

	if got := remote.getDeleteCalls(); len(got) != 0 {
		t.Errorf("remote deletes = %v, want none", got)
	}
	if len(remote.downloadedFiles) != 0 {
		t.Errorf("downloads = %v, want none", remote.downloadedFiles)
	}
}

func snapshotTree(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, _ := d.Info()
		out = append(out, p+"|"+info.ModTime().String()+"|"+d.Type().String())
		return nil
	})
	sort.Strings(out)
	return out
}
