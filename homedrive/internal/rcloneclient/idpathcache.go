package rcloneclient

import (
	"strings"
	"sync"
)

// idPathCache maps Drive file/folder IDs to their last-known remote-relative
// path. Google Drive's Changes API reports changes by file ID plus a parent
// ID chain, not by path, so ListChanges needs a way to translate IDs back
// into the paths the rest of homedrive works with.
//
// The cache is seeded during the full recursive walk (every file and
// directory visited has a known ID and path) and kept warm as subsequent
// incremental changes are translated. It does not otherwise evict: at
// home-NAS scale (tens of thousands of files at most) the memory cost is
// negligible (entries are evicted only when Drive reports the ID trashed or
// removed), and staleness only affects the ability to resolve a path for
// very recently created parents (see resolvePath's fallback to the bisync
// safety net, PLAN.md §7.2).
type idPathCache struct {
	mu sync.RWMutex
	m  map[string]string
}

// newIDPathCache creates an empty cache.
func newIDPathCache() *idPathCache {
	return &idPathCache{m: make(map[string]string)}
}

// get returns the cached path for id, if known.
func (c *idPathCache) get(id string) (string, bool) {
	if id == "" {
		return "", false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	p, ok := c.m[id]
	return p, ok
}

// put records the path for id, overwriting any previous entry.
func (c *idPathCache) put(id, path string) {
	if id == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[id] = path
}

// delete forgets id. A trashed or removed file's ID must not keep resolving
// to its old path: the path may since belong to a different, live file.
func (c *idPathCache) delete(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, id)
}

// evictTree forgets every cached ID whose path is path or lies beneath it.
// Used when a folder is trashed or removed: its descendants' IDs would
// otherwise keep resolving to paths that may by then belong to new items.
func (c *idPathCache) evictTree(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, p := range c.m {
		if p == path || strings.HasPrefix(p, path+"/") {
			delete(c.m, id)
		}
	}
}

// len returns the number of cached entries. Used by tests.
func (c *idPathCache) len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.m)
}
