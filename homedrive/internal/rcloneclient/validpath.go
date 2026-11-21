package rcloneclient

import "strings"

// ValidName reports whether name is acceptable as one Drive item name when
// building a local path from it: not empty, not "." or "..", and free of
// path separators and NUL. Drive names are untrusted input (anyone with edit
// access to a shared folder chooses them), and path.Join would silently
// resolve a ".." name to the parent folder (issue #152).
func ValidName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	return !strings.ContainsAny(name, "/\\\x00")
}

// ValidRemotePath reports whether p is a non-empty slash-separated
// remote-relative path made only of ValidName components (no leading or
// trailing slash, no empty, "." or ".." component, no backslash).
func ValidRemotePath(p string) bool {
	if p == "" || strings.ContainsAny(p, "\\\x00") {
		return false
	}
	for _, c := range strings.Split(p, "/") {
		if c == "" || c == "." || c == ".." {
			return false
		}
	}
	return true
}
