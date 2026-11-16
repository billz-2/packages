// Package translation provides translations of texts stored in a service database:
// the user language (ru|en|uz), the dictionary, and its synchronization between services via Kafka.
// See README.md for details.
package translation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Lang is a user language from the whitelist.
type Lang string

const (
	LangRu Lang = "ru"
	LangEn Lang = "en"
	LangUz Lang = "uz"
)

const (
	queryDictEn = `SELECT en FROM translations WHERE service_name = $1`
	queryDictUz = `SELECT uz FROM translations WHERE service_name = $1`
)

// NormalizeLang casts a raw value to the whitelist; anything else is treated as LangRu.
func NormalizeLang(raw string) Lang {
	switch lang := Lang(strings.ToLower(strings.TrimSpace(raw))); lang {
	case LangEn, LangUz:
		return lang
	default:
		return LangRu
	}
}

// Dict is a flat "key -> translation" dictionary.
type Dict map[string]string

// Get returns the translation or fallback if the key is missing or the value is empty. Safe for nil.
func (d Dict) Get(key, fallback string) string {
	if v := d[key]; v != "" {
		return v
	}

	return fallback
}

// Querier is the minimal data source interface (*sqlx.DB, *sqlx.Tx, etc.).
type Querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// LoadDict loads the dictionary of a single language column for a service.
// For LangRu no query is made and nil is returned. If the row is missing, an empty Dict is returned.
func LoadDict(ctx context.Context, q Querier, serviceName string, lang Lang) (Dict, error) {
	var query string

	switch lang {
	case LangRu:
		return nil, nil
	case LangEn:
		query = queryDictEn
	case LangUz:
		query = queryDictUz
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedLang, lang)
	}

	var raw []byte
	if err := q.QueryRowContext(ctx, query, serviceName).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Dict{}, nil
		}

		return nil, fmt.Errorf("translation: load dict %s/%s: %w", serviceName, lang, err)
	}

	dict := Dict{}
	if err := json.Unmarshal(raw, &dict); err != nil {
		return nil, fmt.Errorf("translation: decode dict %s/%s: %w", serviceName, lang, err)
	}

	return dict, nil
}
