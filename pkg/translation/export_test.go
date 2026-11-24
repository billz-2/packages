package translation

import "time"

// SetDictCacheTTL sets the dictionary cache TTL; ResetDictCache puts the default back.
func SetDictCacheTTL(d time.Duration) { dictCacheTTL = d }

// ResetDictCache drops every cached dictionary and restores the default TTL.
func ResetDictCache() {
	dictCache.reset()

	dictCacheTTL = DefaultDictCacheTTL
}
