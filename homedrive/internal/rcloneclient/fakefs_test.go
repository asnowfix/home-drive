package rcloneclient

import (
	"context"
	"io"
	"strings"
	"time"

	rclonefs "github.com/rclone/rclone/fs"
)

// fakeListingFS is a minimal rclonefs.Fs fake that only implements List,
// NewObject and About, backed by an in-memory directory tree. It lets
// listRecursive, fullWalkThenResume, Stat, List, Quota and DownloadFile be
// tested without a real rclone/Drive backend, per homedrive-test-mocks
// (never call the real Google Drive API in tests).
type fakeListingFS struct {
	rclonefs.Fs
	tree   map[string][]rclonefs.DirEntry
	usage  *rclonefs.Usage
	byPath map[string]fakeObject
}

func (f *fakeListingFS) List(_ context.Context, dir string) (rclonefs.DirEntries, error) {
	entries, ok := f.tree[dir]
	if !ok {
		return nil, rclonefs.ErrorDirNotFound
	}
	return entries, nil
}

func (f *fakeListingFS) NewObject(_ context.Context, remote string) (rclonefs.Object, error) {
	obj, ok := f.byPath[remote]
	if !ok {
		return nil, rclonefs.ErrorObjectNotFound
	}
	return obj, nil
}

func (f *fakeListingFS) About(context.Context) (*rclonefs.Usage, error) {
	if f.usage == nil {
		return nil, rclonefs.ErrorNotImplemented
	}
	return f.usage, nil
}

// fakeObject is a minimal rclonefs.Object fake exposing only what
// remoteObjectFromRclone, listRecursive, Stat, List and DownloadFile read:
// Remote, Size, ModTime, Open, and (via IDer) ID.
type fakeObject struct {
	rclonefs.Object
	remote  string
	size    int64
	modTime time.Time
	id      string
	content string
}

func (o fakeObject) Remote() string                    { return o.remote }
func (o fakeObject) Size() int64                       { return o.size }
func (o fakeObject) ModTime(context.Context) time.Time { return o.modTime }
func (o fakeObject) ID() string                        { return o.id }

func (o fakeObject) Open(context.Context, ...rclonefs.OpenOption) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(o.content)), nil
}

// fakeDirectory is a minimal rclonefs.Directory fake exposing only Remote
// and ID, which is all listRecursive reads.
type fakeDirectory struct {
	rclonefs.Directory
	remote string
	id     string
}

func (d fakeDirectory) Remote() string { return d.remote }
func (d fakeDirectory) ID() string     { return d.id }
