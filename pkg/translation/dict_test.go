package translation_test

import (
	"os"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/billz-2/packages/pkg/translation"
)

func TestLoadDict(t *testing.T) {
	testSuit := []struct {
		name       string
		fileName   string
		service    string
		rawLang    string
		lang       translation.Lang
		closedDB   bool
		assertFunc func(t *testing.T, dict translation.Dict, err error)
	}{
		{
			name:     "en column",
			fileName: "dict.sql",
			service:  "svc_self",
			rawLang:  "en",
			assertFunc: func(t *testing.T, dict translation.Dict, err error) {
				require.NoError(t, err)
				assert.Equal(t, "Cash desk", dict.Get("permission.p1.name", "Касса"))
				assert.Equal(t, "Описание", dict.Get("permission.p1.description", "Описание"))
				assert.Equal(t, "Приход", dict.Get("permission.p2.name", "Приход"))
			},
		},
		{
			name:     "uz with spaces and upper case",
			fileName: "dict.sql",
			service:  "svc_self",
			rawLang:  " UZ ",
			assertFunc: func(t *testing.T, dict translation.Dict, err error) {
				require.NoError(t, err)
				assert.Equal(t, "Kassa", dict.Get("permission.p1.name", "Касса"))
			},
		},
		{
			// A closed connection fails on any query, so success proves that no query was made.
			name:     "unknown lang is ru without query",
			fileName: "dict.sql",
			service:  "svc_self",
			rawLang:  "fr",
			closedDB: true,
			assertFunc: func(t *testing.T, dict translation.Dict, err error) {
				require.NoError(t, err)
				assert.Nil(t, dict)
				assert.Equal(t, "Касса", dict.Get("permission.p1.name", "Касса"))
			},
		},
		{
			name:     "empty lang is ru without query",
			fileName: "dict.sql",
			service:  "svc_self",
			rawLang:  "",
			closedDB: true,
			assertFunc: func(t *testing.T, dict translation.Dict, err error) {
				require.NoError(t, err)
				assert.Nil(t, dict)
				assert.Equal(t, "Касса", dict.Get("permission.p1.name", "Касса"))
			},
		},
		{
			name:     "unsupported lang returns error",
			fileName: "dict.sql",
			service:  "svc_self",
			lang:     translation.Lang("fr"),
			assertFunc: func(t *testing.T, dict translation.Dict, err error) {
				assert.ErrorIs(t, err, translation.ErrUnsupportedLang)
				assert.Nil(t, dict)
			},
		},
		{
			name:     "no row for service",
			fileName: "dict.sql",
			service:  "svc_missing",
			rawLang:  "en",
			assertFunc: func(t *testing.T, dict translation.Dict, err error) {
				require.NoError(t, err)
				assert.NotNil(t, dict)
				assert.Len(t, dict, 0)
			},
		},
	}

	for _, ts := range testSuit {
		t.Run(ts.name, func(t *testing.T) {
			content, err := os.ReadFile("testdata/" + ts.fileName)
			require.NoError(t, err)

			_, err = db.ExecContext(ctx, string(content))
			require.NoError(t, err)

			querier := db

			if ts.closedDB {
				closed, openErr := sqlx.Open("postgres", databaseURL)
				require.NoError(t, openErr)
				require.NoError(t, closed.Close())

				querier = closed
			}

			lang := ts.lang
			if lang == "" {
				lang = translation.NormalizeLang(ts.rawLang)
			}

			dict, err := translation.LoadDict(ctx, querier, ts.service, lang)
			ts.assertFunc(t, dict, err)
		})
	}
}
