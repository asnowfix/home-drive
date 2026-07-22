package rcloneclient

import "sync"

// idPathCache maps Drive file/folder IDs to their last-known remote-relative
// path. Google Drive's Changes API reports changes by file ID plus a parent
// ID chain, not by path, so ListChanges needs a way to translate IDs back
// into the paths the rest of homedrive works with.
//
// The cache is seeded during the full recursive walk (every file and
// directory visited has a known ID and path) and kept warm as subsequent
// incremental changes are translated. It intentionally never evicts: at
// home-NAS scale (tens of thousands of files at most) the memory cost is
// negligible, and staleness only affects the ability to resolve a path for
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

// len returns the number of cached entries. Used by tests.
func (c *idPathCache) len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.m)
}
