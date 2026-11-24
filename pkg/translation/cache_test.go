package translation_test

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/billz-2/packages/pkg/translation"
)

// countingQuerier counts the queries that read a whole dictionary column.
type countingQuerier struct {
	translation.Querier
	dictReads atomic.Int64
}

func (c *countingQuerier) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if strings.HasPrefix(query, "SELECT en") || strings.HasPrefix(query, "SELECT uz") {
		c.dictReads.Add(1)
	}

	return c.Querier.QueryRowContext(ctx, query, args...)
}

func TestLoadDictCache(t *testing.T) {
	testSuit := []struct {
		name       string
		fileName   string
		ttl        time.Duration
		assertFunc func(t *testing.T)
	}{
		{
			name:     "dictionary is served from memory within ttl",
			fileName: "cache_v1.sql",
			ttl:      translation.DefaultDictCacheTTL,
			assertFunc: func(t *testing.T) {
				dict, err := translation.LoadDict(ctx, db, "svc_cache", translation.LangEn)
				require.NoError(t, err)
				assert.Equal(t, "old", dict.Get("k", ""))

				_, err = db.ExecContext(ctx, `UPDATE translations SET en = '{"k":"new"}' WHERE service_name = 'svc_cache'`)
				require.NoError(t, err)

				dict, err = translation.LoadDict(ctx, db, "svc_cache", translation.LangEn)
				require.NoError(t, err)
				assert.Equal(t, "old", dict.Get("k", ""))

				// A closed connection fails on any query, so success proves that no query was made.
				closed, err := sqlx.Open("postgres", databaseURL)
				require.NoError(t, err)
				require.NoError(t, closed.Close())

				dict, err = translation.LoadDict(ctx, closed, "svc_cache", translation.LangEn)
				require.NoError(t, err)
				assert.Equal(t, "old", dict.Get("k", ""))
			},
		},
		{
			name:     "dictionary is reloaded only when the version changes",
			fileName: "cache_v1.sql",
			ttl:      0,
			assertFunc: func(t *testing.T) {
				dict, err := translation.LoadDict(ctx, db, "svc_cache", translation.LangEn)
				require.NoError(t, err)
				assert.Equal(t, "old", dict.Get("k", ""))

				_, err = db.ExecContext(ctx,
					`UPDATE translations SET en = '{"k":"same version"}' WHERE service_name = 'svc_cache'`)
				require.NoError(t, err)

				dict, err = translation.LoadDict(ctx, db, "svc_cache", translation.LangEn)
				require.NoError(t, err)
				assert.Equal(t, "old", dict.Get("k", ""))

				_, err = db.ExecContext(ctx, `UPDATE translations SET version = 2 WHERE service_name = 'svc_cache'`)
				require.NoError(t, err)

				dict, err = translation.LoadDict(ctx, db, "svc_cache", translation.LangEn)
				require.NoError(t, err)
				assert.Equal(t, "same version", dict.Get("k", ""))
			},
		},
		{
			name:     "cached dictionary is served when the database fails",
			fileName: "cache_v1.sql",
			ttl:      0,
			assertFunc: func(t *testing.T) {
				_, err := translation.LoadDict(ctx, db, "svc_cache", translation.LangEn)
				require.NoError(t, err)

				closed, err := sqlx.Open("postgres", databaseURL)
				require.NoError(t, err)
				require.NoError(t, closed.Close())

				dict, err := translation.LoadDict(ctx, closed, "svc_cache", translation.LangEn)
				require.NoError(t, err)
				assert.Equal(t, "old", dict.Get("k", ""))
			},
		},
		{
			name:     "cold cache returns the database error",
			fileName: "cache_v1.sql",
			ttl:      0,
			assertFunc: func(t *testing.T) {
				closed, err := sqlx.Open("postgres", databaseURL)
				require.NoError(t, err)
				require.NoError(t, closed.Close())

				dict, err := translation.LoadDict(ctx, closed, "svc_cache", translation.LangEn)
				require.Error(t, err)
				assert.Nil(t, dict)
			},
		},
		{
			name:     "missing row is cached as an empty dictionary",
			fileName: "cache_missing.sql",
			ttl:      translation.DefaultDictCacheTTL,
			assertFunc: func(t *testing.T) {
				dict, err := translation.LoadDict(ctx, db, "svc_cache", translation.LangEn)
				require.NoError(t, err)
				assert.NotNil(t, dict)
				assert.Empty(t, dict)

				closed, err := sqlx.Open("postgres", databaseURL)
				require.NoError(t, err)
				require.NoError(t, closed.Close())

				dict, err = translation.LoadDict(ctx, closed, "svc_cache", translation.LangEn)
				require.NoError(t, err)
				assert.NotNil(t, dict)
				assert.Empty(t, dict)
			},
		},
		{
			name:     "row that appears later is picked up after ttl",
			fileName: "cache_missing.sql",
			ttl:      0,
			assertFunc: func(t *testing.T) {
				dict, err := translation.LoadDict(ctx, db, "svc_cache", translation.LangEn)
				require.NoError(t, err)
				assert.Empty(t, dict)

				_, err = db.ExecContext(ctx, `
					INSERT INTO translations (service_name, en, version) VALUES ('svc_cache', '{"k":"appeared"}', 1)`)
				require.NoError(t, err)

				dict, err = translation.LoadDict(ctx, db, "svc_cache", translation.LangEn)
				require.NoError(t, err)
				assert.Equal(t, "appeared", dict.Get("k", ""))
			},
		},
		{
			name:     "invalidated dictionary is reloaded even if the caller context is canceled",
			fileName: "cache_v1.sql",
			ttl:      translation.DefaultDictCacheTTL,
			assertFunc: func(t *testing.T) {
				_, err := translation.LoadDict(ctx, db, "svc_cache", translation.LangEn)
				require.NoError(t, err)

				_, err = db.ExecContext(ctx,
					`UPDATE translations SET en = '{"k":"new"}', version = 2 WHERE service_name = 'svc_cache'`)
				require.NoError(t, err)

				translation.InvalidateDict("svc_cache")

				canceled, cancel := context.WithCancel(ctx)
				cancel()

				dict, err := translation.LoadDict(canceled, db, "svc_cache", translation.LangEn)
				require.NoError(t, err)
				assert.Equal(t, "new", dict.Get("k", ""))
			},
		},
		{
			name:     "invalidated dictionary is served while the database fails",
			fileName: "cache_v1.sql",
			ttl:      translation.DefaultDictCacheTTL,
			assertFunc: func(t *testing.T) {
				_, err := translation.LoadDict(ctx, db, "svc_cache", translation.LangEn)
				require.NoError(t, err)

				translation.InvalidateDict("svc_cache")

				closed, err := sqlx.Open("postgres", databaseURL)
				require.NoError(t, err)
				require.NoError(t, closed.Close())

				dict, err := translation.LoadDict(ctx, closed, "svc_cache", translation.LangEn)
				require.NoError(t, err)
				assert.Equal(t, "old", dict.Get("k", ""))
			},
		},
		{
			name:     "concurrent loads on a cold cache read the dictionary once",
			fileName: "cache_v1.sql",
			ttl:      translation.DefaultDictCacheTTL,
			assertFunc: func(t *testing.T) {
				querier := &countingQuerier{Querier: db}

				var wg sync.WaitGroup

				for range 50 {
					wg.Add(1)

					go func() {
						defer wg.Done()

						dict, err := translation.LoadDict(ctx, querier, "svc_cache", translation.LangEn)
						assert.NoError(t, err)
						assert.Equal(t, "old", dict.Get("k", ""))
					}()
				}

				wg.Wait()

				assert.EqualValues(t, 1, querier.dictReads.Load())
			},
		},
	}

	for _, ts := range testSuit {
		t.Run(ts.name, func(t *testing.T) {
			translation.ResetDictCache()
			translation.SetDictCacheTTL(ts.ttl)

			content, err := os.ReadFile("testdata/" + ts.fileName)
			require.NoError(t, err)

			_, err = db.ExecContext(ctx, string(content))
			require.NoError(t, err)

			ts.assertFunc(t)
		})
	}
}
