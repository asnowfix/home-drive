// Package oldsuffix parses and generates the conflict-loser suffix
// (default ".old.<N>") used by the newer-wins policy in PLAN.md §11.2.
//
// Before this package existed, "compute the next N" was implemented three
// times (store.Journal.NextOldN, store.ConflictResolver.nextOldN,
// syncer.Bisync.nextOldN) and the suffix format was hardcoded in four more
// places, none of which checked whether the path they were handed already
// carried a suffix. That produced the file.old.1.old.1.old.1... chains in
// issue #65. Centralizing here means every call site collapses identically.
package oldsuffix

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// DefaultFormat is used when no format is configured.
const DefaultFormat = ".old.%d"

// maxDepth bounds how many suffixes Base will strip from a single path,
// protecting against a pathological or adversarial filename.
const maxDepth = 64

// maxProbe bounds how many candidate N values NextOldN will try before
// giving up, replacing the previously-unbounded "for {}" loops in the
// three call sites this package replaces. A corrupt journal would
// otherwise hang the pull loop forever; with the cap the caller can log
// at ERROR and fail the single conflict instead.
const maxProbe = 1 << 16

// ErrBadFormat is returned by New for a format string that cannot be used
// for both parsing and formatting.
var ErrBadFormat = errors.New("oldsuffix: invalid suffix format")

// Matcher parses and formats one suffix layout, compiled from an
// fmt-style format such as ".old.%d".
type Matcher struct {
	pre  string // literal text before %d, e.g. ".old."
	post string // literal text after %d, usually ""
}

// New compiles format (e.g. ".old.%d"). format must contain exactly one
// "%d" verb, no other verbs, and a non-empty literal before the verb
// (otherwise ".old.1" could not be distinguished from a digit-suffixed
// filename). An empty format yields the default ".old.%d".
func New(format string) (*Matcher, error) {
	if format == "" {
		format = DefaultFormat
	}
	if strings.Count(format, "%") != 1 {
		return nil, fmt.Errorf("%w: %q: must contain exactly one %%-verb", ErrBadFormat, format)
	}
	pre, post, ok := strings.Cut(format, "%d")
	if !ok {
		return nil, fmt.Errorf("%w: %q: the one %%-verb must be %%d", ErrBadFormat, format)
	}
	if pre == "" {
		return nil, fmt.Errorf("%w: %q: must have a non-empty literal before %%d", ErrBadFormat, format)
	}
	return &Matcher{pre: pre, post: post}, nil
}

// Format returns base with a single suffix for n appended.
func (m *Matcher) Format(base string, n int) string {
	return base + m.pre + strconv.Itoa(n) + m.post
}

// TrimOne strips exactly one trailing suffix. ok is false if path does
// not end in a well-formed suffix.
//
// Deliberately strict -- a false positive here renames a user's real
// file. Digits are validated rune-by-rune rather than via strconv.Atoi
// alone, which would silently accept "-1" or "+1"; a leading zero
// (".old.007") is rejected too, since that pattern is far more likely to
// be a user's own filename than something this package generated.
func (m *Matcher) TrimOne(path string) (base string, n int, ok bool) {
	if !strings.HasSuffix(path, m.post) {
		return "", 0, false
	}
	rest := strings.TrimSuffix(path, m.post)

	i := strings.LastIndex(rest, m.pre)
	if i < 0 {
		return "", 0, false
	}
	digits := rest[i+len(m.pre):]
	if digits == "" {
		return "", 0, false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return "", 0, false
		}
	}
	if len(digits) > 1 && digits[0] == '0' {
		return "", 0, false
	}
	num, err := strconv.Atoi(digits)
	if err != nil || num < 1 {
		return "", 0, false
	}
	base = rest[:i]
	if base == "" {
		return "", 0, false
	}
	return base, num, true
}

// Base strips every trailing suffix, so "f.md.old.1.old.1.old.1" ->
// "f.md" with depth 3. depth is how many suffixes were stripped; depth
// 0 means path did not carry a well-formed suffix at all.
func (m *Matcher) Base(path string) (base string, depth int) {
	base = path
	for depth < maxDepth {
		next, _, ok := m.TrimOne(base)
		if !ok {
			return base, depth
		}
		base = next
		depth++
	}
	return base, depth
}

// IsOld reports whether path carries at least one suffix.
func (m *Matcher) IsOld(path string) bool {
	_, _, ok := m.TrimOne(path)
	return ok
}

// Pre returns the literal text before the %d verb (e.g. ".old."). Used
// by the retention GC (internal/store/prune.go) to bound a journal scan
// to base+Pre() instead of walking the full journal.
func (m *Matcher) Pre() string {
	return m.pre
}

// NextOldN returns the base path that a new conflict loser should hang
// off, and the smallest N >= 1 for which exists(base+suffix(N)) is
// false.
//
// If path already carries one or more suffixes AND its fully-stripped
// base is a known path (per exists), the suffixes are collapsed: a
// repeat conflict on "f.md.old.1" yields ("f.md", 2), not
// ("f.md.old.1.old.1", 1). If the base is unknown, path is treated
// literally -- a user file that merely happens to look like ".old.<N>"
// keeps its own numbering space, so a real "budget.old.2" never gets
// silently renumbered onto a "budget" it has nothing to do with.
func NextOldN(m *Matcher, path string, exists func(string) bool) (base string, n int) {
	base, depth := m.Base(path)
	if depth > 0 && !exists(base) {
		base = path // not our artifact; do not collapse
	}
	for n = 1; n <= maxProbe; n++ {
		if !exists(m.Format(base, n)) {
			return base, n
		}
	}
	return base, maxProbe
}
