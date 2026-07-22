package rcloneclient

import "testing"

func TestMatchesExclude_Cases(t *testing.T) {
	cases := []struct {
		name     string
		patterns []string
		path     string
		want     bool
	}{
		{"no patterns", nil, "foo.txt", false},
		{"exact match", []string{"foo.txt"}, "foo.txt", true},
		{"no match", []string{"foo.txt"}, "bar.txt", false},
		{"glob star suffix", []string{"*.tmp"}, "scratch.tmp", true},
		{"glob star suffix no match", []string{"*.tmp"}, "dir/scratch.tmp", false},
		{"doublestar dir pattern", []string{"**/.git/**"}, ".git/HEAD", true},
		{"doublestar dir itself", []string{"**/.git/**"}, ".git", true},
		{"doublestar nested", []string{"**/.git/**"}, "a/b/.git/config", true},
		{"leading slash stripped", []string{"foo.txt"}, "/foo.txt", true},
		{"unrelated nested path", []string{"**/.git/**"}, "src/main.go", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := matchesExclude(tc.patterns, tc.path)
			if got != tc.want {
				t.Errorf("matchesExclude(%v, %q) = %v, want %v", tc.patterns, tc.path, got, tc.want)
			}
		})
	}
}
