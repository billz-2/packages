package translation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// DefaultDictCacheTTL is how long a loaded dictionary is served without asking the database.
const DefaultDictCacheTTL = 60 * time.Second

// dictLoadTimeout limits a dictionary load that is shared by all concurrent callers and does not depend
// on the context of any single one of them.
const dictLoadTimeout = 5 * time.Second

const queryDictVersion = `SELECT version FROM translations WHERE service_name = $1`

var (
	dictCacheTTL = DefaultDictCacheTTL
	dictCache    = &dictCacheStore{entries: map[string]dictCacheEntry{}}
)

type dictCacheEntry struct {
	dict      Dict
	version   int64
	checkedAt time.Time
}

// dictCacheStore keeps decoded dictionaries in process memory by "service|lang".
type dictCacheStore struct {
	mu      sync.RWMutex
	entries map[string]dictCacheEntry
	group   singleflight.Group
}

func cacheKey(serviceName string, lang Lang) string {
	return serviceName + "|" + string(lang)
}

func (s *dictCacheStore) get(key string) (dictCacheEntry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.entries[key]

	return entry, ok
}

func (s *dictCacheStore) set(key string, entry dictCacheEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.entries[key] = entry
}

func (s *dictCacheStore) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.entries = map[string]dictCacheEntry{}
}

// InvalidateDict marks the cached dictionaries of the service as outdated in this process: the next call
// reads the version and the dictionary again, but while the database is failing the old dictionary is still served.
// The other replicas pick the new version up when their TTL expires.
func InvalidateDict(serviceName string) {
	dictCache.mu.Lock()
	defer dictCache.mu.Unlock()

	for _, lang := range []Lang{LangEn, LangUz} {
		key := cacheKey(serviceName, lang)

		if entry, ok := dictCache.entries[key]; ok {
			entry.checkedAt = time.Time{}
			entry.version = -1
			dictCache.entries[key] = entry
		}
	}
}

// loadCached returns the dictionary from memory while the entry is fresh. After the TTL only the version of
// the row is read; the dictionary itself is read and decoded again only if the version changed.
// If the database fails, an already cached dictionary is still returned.
func loadCached(ctx context.Context, q Querier, query, serviceName string, lang Lang) (Dict, error) {
	key := cacheKey(serviceName, lang)

	if entry, ok := dictCache.get(key); ok && time.Since(entry.checkedAt) < dictCacheTTL {
		return entry.dict, nil
	}

	res, err, _ := dictCache.group.Do(key, func() (any, error) {
		stale, hasStale := dictCache.get(key)

		// A concurrent caller may have refreshed the entry while this one was waiting for the group.
		if hasStale && time.Since(stale.checkedAt) < dictCacheTTL {
			return stale.dict, nil
		}

		// The load is shared by every waiting caller, so it must not die with the context of the first one.
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dictLoadTimeout)
		defer cancel()

		entry, loadErr := refresh(loadCtx, q, query, serviceName, lang, stale, hasStale)
		if loadErr != nil {
			if hasStale {
				return stale.dict, nil
			}

			return nil, loadErr
		}

		dictCache.set(key, entry)

		return entry.dict, nil
	})
	if err != nil {
		return nil, err
	}

	dict, _ := res.(Dict)

	return dict, nil
}

func refresh(
	ctx context.Context,
	q Querier,
	query, serviceName string,
	lang Lang,
	stale dictCacheEntry,
	hasStale bool,
) (dictCacheEntry, error) {
	var version int64

	err := q.QueryRowContext(ctx, queryDictVersion, serviceName).Scan(&version)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return dictCacheEntry{dict: Dict{}, checkedAt: time.Now()}, nil
	case err != nil:
		return dictCacheEntry{}, fmt.Errorf("translation: load version %s/%s: %w", serviceName, lang, err)
	}

	if hasStale && stale.version == version {
		stale.checkedAt = time.Now()

		return stale, nil
	}

	var raw []byte
	if err = q.QueryRowContext(ctx, query, serviceName).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return dictCacheEntry{dict: Dict{}, checkedAt: time.Now()}, nil
		}

		return dictCacheEntry{}, fmt.Errorf("translation: load dict %s/%s: %w", serviceName, lang, err)
	}

	dict := Dict{}
	if err = json.Unmarshal(raw, &dict); err != nil {
		return dictCacheEntry{}, fmt.Errorf("translation: decode dict %s/%s: %w", serviceName, lang, err)
	}

	return dictCacheEntry{dict: dict, version: version, checkedAt: time.Now()}, nil
}
