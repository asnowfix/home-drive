package desktop

import "time"

// noAgeLimit stands for "no age-based eviction" when handed to rclone. In
// rclone v1.76.0 vfs_cache_max_age = 0 does NOT mean "no limit": cache
// cleaning calls purgeOld(0), and Item.RemoveNotInUse treats maxAge == 0 as
// "remove every item not in use" (vfs/vfscache/item.go), at every poll and at
// start-up. A very large age is the way to say "never expire by age".
const noAgeLimit = 100 * 365 * 24 * time.Hour

// EffectiveMaxSize returns the VFS cache cap in bytes: the absolute
// cache.max_size when set, otherwise cache.max_size_percent of fsSize (the
// size of the filesystem holding the cache directory).
func (c *CacheCfg) EffectiveMaxSize(fsSize int64) int64 {
	if c.MaxSize > 0 {
		return int64(c.MaxSize)
	}
	return fsSize / 100 * int64(c.MaxSizePercent)
}

// EffectiveMaxAge maps cache.max_age (0 = never expire) to the value rclone
// needs; see noAgeLimit.
func (c *CacheCfg) EffectiveMaxAge() time.Duration {
	if c.MaxAge.Duration <= 0 {
		return noAgeLimit
	}
	return c.MaxAge.Duration
}
