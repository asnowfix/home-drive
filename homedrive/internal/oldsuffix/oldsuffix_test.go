package oldsuffix

import (
	"strings"
	"testing"
)

func TestNew_InvalidFormat(t *testing.T) {
	tests := []struct {
		name    string
		format  string
		wantErr bool
	}{
		{name: "empty uses default", format: "", wantErr: false},
		{name: "valid default", format: ".old.%d", wantErr: false},
		{name: "valid custom", format: ".conflict-%d", wantErr: false},
		{name: "no verb", format: ".old", wantErr: true},
		{name: "two verbs", format: ".old.%d.%d", wantErr: true},
		{name: "empty literal prefix", format: "%d", wantErr: true},
		{name: "wrong verb", format: ".old.%s", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, err := New(tc.format)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("New(%q) = nil error, want error", tc.format)
				}
				if !strings.Contains(err.Error(), "oldsuffix:") {
					t.Errorf("error = %q, want wrapped ErrBadFormat", err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("New(%q) unexpected error: %v", tc.format, err)
			}
			if m == nil {
				t.Fatalf("New(%q) returned nil matcher with nil error", tc.format)
			}
		})
	}
}

func TestMatcher_TrimOne_Cases(t *testing.T) {
	m, err := New(".old.%d")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	tests := []struct {
		name     string
		path     string
		wantBase string
		wantN    int
		wantOK   bool
	}{
		{name: "no suffix", path: "f.md", wantOK: false},
		{name: "simple", path: "f.md.old.1", wantBase: "f.md", wantN: 1, wantOK: true},
		{name: "two digits", path: "f.md.old.12", wantBase: "f.md", wantN: 12, wantOK: true},
		{name: "trailing dot no digits", path: "f.md.old.", wantOK: false},
		{name: "zero", path: "f.md.old.0", wantOK: false},
		{name: "leading zero", path: "f.md.old.007", wantOK: false},
		{name: "negative", path: "f.md.old.-1", wantOK: false},
		{name: "trailing letter", path: "f.md.old.1a", wantOK: false},
		{name: "no base", path: ".old.1", wantOK: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base, n, ok := m.TrimOne(tc.path)
			if ok != tc.wantOK {
				t.Fatalf("TrimOne(%q) ok = %v, want %v", tc.path, ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if base != tc.wantBase || n != tc.wantN {
				t.Errorf("TrimOne(%q) = (%q, %d), want (%q, %d)", tc.path, base, n, tc.wantBase, tc.wantN)
			}
		})
	}
}

func TestMatcher_TrimOne_CustomFormat(t *testing.T) {
	m, err := New(".conflict-%d")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	base, n, ok := m.TrimOne("notes.md.conflict-3")
	if !ok || base != "notes.md" || n != 3 {
		t.Fatalf("TrimOne = (%q, %d, %v), want (\"notes.md\", 3, true)", base, n, ok)
	}
	// The default-format literal must not be recognized by a custom matcher.
	if m.IsOld("notes.md.old.1") {
		t.Errorf("IsOld(%q) = true with custom format, want false", "notes.md.old.1")
	}
}

func TestMatcher_Base_Cases(t *testing.T) {
	m, err := New(".old.%d")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// (b) regression: the literal production string from issue #65 --
	// 13 chained ".old.1" suffixes must all collapse onto the true base.
	chained := "myhome-kiosk.md" + strings.Repeat(".old.1", 13)
	base, depth := m.Base(chained)
	if base != "myhome-kiosk.md" || depth != 13 {
		t.Errorf("Base(chained) = (%q, %d), want (\"myhome-kiosk.md\", 13)", base, depth)
	}

	tests := []struct {
		name      string
		path      string
		wantBase  string
		wantDepth int
	}{
		{name: "no suffix", path: "f.md", wantBase: "f.md", wantDepth: 0},
		{name: "mixed depth two", path: "f.md.old.1.old.2", wantBase: "f.md", wantDepth: 2},
		{name: "stops at malformed", path: "f.md.old.1.old.x", wantBase: "f.md.old.1.old.x", wantDepth: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base, depth := m.Base(tc.path)
			if base != tc.wantBase || depth != tc.wantDepth {
				t.Errorf("Base(%q) = (%q, %d), want (%q, %d)", tc.path, base, depth, tc.wantBase, tc.wantDepth)
			}
		})
	}
}

func TestMatcher_Base_DepthCapped(t *testing.T) {
	m, err := New(".old.%d")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Build a pathological chain deeper than maxDepth and verify Base
	// terminates rather than looping forever.
	deep := "f.md" + strings.Repeat(".old.1", maxDepth+10)
	base, depth := m.Base(deep)
	if depth != maxDepth {
		t.Errorf("Base(deep) depth = %d, want capped at %d", depth, maxDepth)
	}
	if base == "f.md" {
		t.Errorf("Base(deep) fully stripped a chain longer than maxDepth, want partial strip")
	}
}

func TestMatcher_IsOld(t *testing.T) {
	m, err := New(".old.%d")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if m.IsOld("plain.txt") {
		t.Error("IsOld(plain.txt) = true, want false")
	}
	if !m.IsOld("plain.txt.old.1") {
		t.Error("IsOld(plain.txt.old.1) = false, want true")
	}
}

func TestMatcher_Format(t *testing.T) {
	m, err := New(".old.%d")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := m.Format("notes.md", 3); got != "notes.md.old.3" {
		t.Errorf("Format = %q, want %q", got, "notes.md.old.3")
	}
}

func TestNextOldN_Cases(t *testing.T) {
	m, err := New(".old.%d")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	t.Run("collapses onto known base", func(t *testing.T) {
		// (a) regression: exists = {f.md, f.md.old.1}; a conflict on
		// f.md.old.1 must collapse onto f.md and yield N=2, never nest
		// as f.md.old.1.old.1.
		set := map[string]bool{"f.md": true, "f.md.old.1": true}
		exists := func(p string) bool { return set[p] }

		base, n := NextOldN(m, "f.md.old.1", exists)
		if base != "f.md" || n != 2 {
			t.Errorf("NextOldN = (%q, %d), want (\"f.md\", 2)", base, n)
		}
	})

	t.Run("no siblings", func(t *testing.T) {
		set := map[string]bool{"f.md": true}
		exists := func(p string) bool { return set[p] }

		base, n := NextOldN(m, "f.md", exists)
		if base != "f.md" || n != 1 {
			t.Errorf("NextOldN = (%q, %d), want (\"f.md\", 1)", base, n)
		}
	})

	t.Run("hole reuse", func(t *testing.T) {
		// f.md.old.2 was GC'd, leaving a hole; the next conflict must
		// reuse N=2, not jump to N=3.
		set := map[string]bool{"f.md": true, "f.md.old.1": true, "f.md.old.3": true}
		exists := func(p string) bool { return set[p] }

		base, n := NextOldN(m, "f.md", exists)
		if base != "f.md" || n != 2 {
			t.Errorf("NextOldN = (%q, %d), want (\"f.md\", 2)", base, n)
		}
	})

	t.Run("no-collapse guard for a user's real old-looking file", func(t *testing.T) {
		set := map[string]bool{"budget.old.2": true} // "budget" itself absent
		exists := func(p string) bool { return set[p] }

		base, n := NextOldN(m, "budget.old.2", exists)
		if base != "budget.old.2" || n != 1 {
			t.Errorf("NextOldN = (%q, %d), want (\"budget.old.2\", 1)", base, n)
		}
	})

	t.Run("maxProbe exhaustion returns the cap", func(t *testing.T) {
		exists := func(string) bool { return true } // every candidate "exists"

		base, n := NextOldN(m, "f.md", exists)
		if base != "f.md" || n != maxProbe {
			t.Errorf("NextOldN = (%q, %d), want (\"f.md\", %d)", base, n, maxProbe)
		}
	})
}
