package pathfilter

import "testing"

func TestExcluded_Cases(t *testing.T) {
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
		{"glob star suffix no match nested", []string{"*.tmp"}, "dir/scratch.tmp", false},
		{"doublestar dir pattern nested file", []string{"**/.git/**"}, ".git/HEAD", true},
		{"doublestar dir itself", []string{"**/.git/**"}, ".git", true},
		{"doublestar deeply nested", []string{"**/.git/**"}, "a/b/.git/config", true},
		{"leading slash stripped", []string{"foo.txt"}, "/foo.txt", true},
		{"unrelated nested path", []string{"**/.git/**"}, "src/main.go", false},
		{"ds_store", []string{"**/.DS_Store"}, "Documents/.DS_Store", true},
		{"vim swap", []string{"**/*.swp"}, "docs/.readme.swp", true},
		{"node_modules", []string{"**/node_modules/**"}, "app/node_modules/lodash/index.js", true},
		{"normal file not excluded", []string{"**/.git/**", "**/*.swp"}, "Documents/notes.md", false},
		{"tilde backup", []string{"**/*~"}, "file.txt~", true},
		{"idea directory", []string{"**/.idea/**"}, "project/.idea/workspace.xml", true},
		{"multiple patterns second matches", []string{"*.tmp", "**/.git/**"}, ".git/HEAD", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Excluded(tc.patterns, tc.path)
			if got != tc.want {
				t.Errorf("Excluded(%v, %q) = %v, want %v", tc.patterns, tc.path, got, tc.want)
			}
		})
	}
}
