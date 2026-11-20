package rcloneclient

import (
	"context"
	"errors"
	"net/http"
	"testing"

	drivev3 "google.golang.org/api/drive/v3"
)

// fakeIDResolver is an in-memory RemoteIDResolver counting its scans.
type fakeIDResolver struct {
	paths map[string][]string
	err   error
	calls int
}

func (f *fakeIDResolver) RemoteIDPaths() (map[string][]string, error) {
	f.calls++
	return f.paths, f.err
}

// Issue #163: after a restart the path cache is empty for items deleted
// while the agent was down; the journaled ID must resolve them, through the
// same validity and exclusion checks as a cache-resolved deletion.
func TestPollChanges_RemovedUnknownIDResolvedFromJournal(t *testing.T) {
	tests := []struct {
		name     string
		resolver *fakeIDResolver // nil means no resolver configured
		cached   map[string]string
		exclude  []string
		want     []string // paths of the Deleted changes
	}{
		{
			name:     "journaled uncached ID is deleted",
			resolver: &fakeIDResolver{paths: map[string][]string{"gone": {"docs/a.txt"}}},
			want:     []string{"docs/a.txt"},
		},
		{
			name:     "ID absent from journal is dropped",
			resolver: &fakeIDResolver{paths: map[string][]string{"other": {"docs/a.txt"}}},
		},
		{
			name:     "no resolver keeps previous behaviour",
			resolver: nil,
		},
		{
			name:     "resolver error drops the change",
			resolver: &fakeIDResolver{err: errors.New("db closed")},
		},
		{
			name:     "journaled path now excluded is dropped",
			resolver: &fakeIDResolver{paths: map[string][]string{"gone": {"docs/a.tmp"}}},
			exclude:  []string{"**/*.tmp"},
		},
		{
			name: "journaled unsafe path is dropped",
			resolver: &fakeIDResolver{paths: map[string][]string{
				"gone": {"../escape.txt"},
			}},
		},
		{
			name:     "journaled empty path is dropped",
			resolver: &fakeIDResolver{paths: map[string][]string{"gone": {""}}},
		},
		{
			name:     "two journal entries sharing the ID are dropped",
			resolver: &fakeIDResolver{paths: map[string][]string{"gone": {"a.txt", "b.txt"}}},
		},
		{
			name:     "cache wins over journal",
			resolver: &fakeIDResolver{paths: map[string][]string{"gone": {"stale/old.txt"}}},
			cached:   map[string]string{"gone": "docs/fresh.txt"},
			want:     []string{"docs/fresh.txt"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := newTestDriveService(t, func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, &drivev3.ChangeList{
					NewStartPageToken: "next",
					Changes:           []*drivev3.Change{{FileId: "gone", Removed: true}},
				})
			})
			r := newTestRcloneFS(svc)
			r.rootID = "root-id"
			r.pathCache.put("root-id", "")
			r.exclude = tc.exclude
			if tc.resolver != nil {
				r.idResolver = tc.resolver
			}
			for id, p := range tc.cached {
				r.pathCache.put(id, p)
			}

			changes, err := r.pollChanges(context.Background(), "tok")
			if err != nil {
				t.Fatalf("pollChanges: %v", err)
			}
			var got []string
			for _, ch := range changes.Items {
				if !ch.Deleted || ch.ID != "gone" {
					t.Errorf("unexpected change %+v", ch)
				}
				got = append(got, ch.Path)
			}
			if len(got) != len(tc.want) || (len(got) == 1 && got[0] != tc.want[0]) {
				t.Fatalf("deleted paths = %v, want %v", got, tc.want)
			}
		})
	}
}

// A mass deletion must cost one journal scan per poll, not one per change,
// and none when every ID is in the cache.
func TestPollChanges_RemovedIDsShareOneJournalScan(t *testing.T) {
	tests := []struct {
		name      string
		cachedAll bool
		wantCalls int
		wantItems int
	}{
		{"all uncached", false, 1, 3},
		{"all cached", true, 0, 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ids := map[string]string{"i1": "a.txt", "i2": "b.txt", "i3": "c.txt"}
			var chs []*drivev3.Change
			res := &fakeIDResolver{paths: map[string][]string{}}
			for id, p := range ids {
				chs = append(chs, &drivev3.Change{FileId: id, Removed: true})
				res.paths[id] = []string{p}
			}
			svc := newTestDriveService(t, func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, &drivev3.ChangeList{NewStartPageToken: "next", Changes: chs})
			})
			r := newTestRcloneFS(svc)
			r.rootID = "root-id"
			r.pathCache.put("root-id", "")
			r.idResolver = res
			if tc.cachedAll {
				for id, p := range ids {
					r.pathCache.put(id, p)
				}
			}

			changes, err := r.pollChanges(context.Background(), "tok")
			if err != nil {
				t.Fatalf("pollChanges: %v", err)
			}
			if len(changes.Items) != tc.wantItems {
				t.Errorf("items = %d, want %d", len(changes.Items), tc.wantItems)
			}
			if res.calls != tc.wantCalls {
				t.Errorf("journal scans = %d, want %d", res.calls, tc.wantCalls)
			}
		})
	}
}
