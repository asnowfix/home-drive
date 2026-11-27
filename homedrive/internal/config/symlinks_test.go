package config

import (
	"strings"
	"testing"
)

func TestLoad_SymlinksCopyResolvedLinks_Cases(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		want    []string
		wantErr string // substring; "" = must load
	}{
		{"omitted_is_empty", "local_root: /x\n", nil, ""},
		{"empty_list", "symlinks:\n  copy_resolved_links: []\n", nil, ""},
		{"two_patterns", "symlinks:\n  copy_resolved_links:\n    - Brain/.claude/skills/**\n    - docs/*\n",
			[]string{"Brain/.claude/skills/**", "docs/*"}, ""},
		{"absolute", "symlinks:\n  copy_resolved_links: [\"/etc/**\"]\n", nil, "absolute"},
		{"windows_drive", "symlinks:\n  copy_resolved_links: [\"C:/x\"]\n", nil, "absolute"},
		{"dotdot_leading", "symlinks:\n  copy_resolved_links: [\"../x/**\"]\n", nil, ".."},
		{"dotdot_inner", "symlinks:\n  copy_resolved_links: [\"a/../../x\"]\n", nil, ".."},
		{"empty_pattern", "symlinks:\n  copy_resolved_links: [\"\"]\n", nil, "empty"},
		{"backslash", "symlinks:\n  copy_resolved_links: [\"a\\\\b\"]\n", nil, "separators"},
		{"bad_glob", "symlinks:\n  copy_resolved_links: [\"a/[\"]\n", nil, "glob"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(writeConfig(t, tt.yaml))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) ||
					!strings.Contains(err.Error(), "symlinks.copy_resolved_links") {
					t.Fatalf("Load error = %v, want one naming the key and %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			got := cfg.Symlinks.CopyResolvedLinks
			if len(got) != len(tt.want) {
				t.Fatalf("CopyResolvedLinks = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("CopyResolvedLinks[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}
