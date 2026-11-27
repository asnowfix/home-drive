package desktop

import (
	"errors"
	"strings"
	"testing"
)

func TestParseAppArmorLabel_Cases(t *testing.T) {
	tests := []struct {
		name         string
		raw          string
		wantLabel    string
		wantConfined bool
	}{
		{"unconfined", "unconfined\n", "unconfined", false},
		{"unconfined no newline", "unconfined", "unconfined", false},
		{"empty", "", "", false},
		{"newline only", "\n", "", false},
		{"nul padded empty", "\x00", "", false},
		{"snap complain", "snap.go.go (complain)\n", "snap.go.go (complain)", true},
		{"enforce profile", "/usr/bin/foo (enforce)\n", "/usr/bin/foo (enforce)", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			label, confined := parseAppArmorLabel(tc.raw)
			if label != tc.wantLabel || confined != tc.wantConfined {
				t.Errorf("parseAppArmorLabel(%q) = (%q, %v), want (%q, %v)", tc.raw, label, confined, tc.wantLabel, tc.wantConfined)
			}
		})
	}
}

func TestWithAppArmorHint_Cases(t *testing.T) {
	fuseErr := errors.New("fusermount exited with code 1\n")
	otherErr := errors.New("mountpoint is busy")
	tests := []struct {
		name     string
		err      error
		confined bool
		wantHint bool
	}{
		{"confined fusermount", fuseErr, true, true},
		{"unconfined fusermount", fuseErr, false, false},
		{"confined other error", otherErr, true, false},
		{"nil error", nil, true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := withAppArmorHint(tc.err, "snap.go.go (complain)", tc.confined)
			if tc.err == nil {
				if got != nil {
					t.Fatalf("got %v, want nil", got)
				}
				return
			}
			if !errors.Is(got, tc.err) {
				t.Errorf("original error lost from chain: %v", got)
			}
			if errors.Is(got, ErrAppArmorConfined) != tc.wantHint {
				t.Errorf("errors.Is(ErrAppArmorConfined) = %v, want %v (%v)", !tc.wantHint, tc.wantHint, got)
			}
			if tc.wantHint && (!strings.Contains(got.Error(), "AppArmor") || !strings.Contains(got.Error(), "dev-environment.md")) {
				t.Errorf("hint missing cause or doc pointer: %v", got)
			}
		})
	}
}
