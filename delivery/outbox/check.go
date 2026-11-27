package outbox

import (
	"context"
	"errors"
	"fmt"
)

const (
	// undefinedTable is the SQLSTATE of a query on a table that does not exist.
	undefinedTable = "42P01"
	// minServerVersion is PostgreSQL 12, the first with plan_cache_mode, which the relay sets per round.
	minServerVersion = 120000
)

// Check fails when a table of the migration is missing or PostgreSQL is older than 12. A service should not
// start then: every Publish would fail, the first one inside a business transaction.
func (o *Outbox) Check(ctx context.Context) error {
	for _, table := range []string{o.opts.Table, o.opts.FailedTable} {
		err := o.checkTable(ctx, table)

		var coded interface{ SQLState() string }
		switch {
		case err == nil:
		case errors.As(err, &coded) && coded.SQLState() == undefinedTable:
			return fmt.Errorf("outbox: table %s does not exist, its migration is not applied: %w", table, err)
		default:
			return fmt.Errorf("outbox: check of table %s: %w", table, err)
		}
	}

	version, err := o.serverVersion(ctx)
	if err != nil {
		return fmt.Errorf("outbox: check of the PostgreSQL version: %w", err)
	}
	if version < minServerVersion {
		return fmt.Errorf("outbox: PostgreSQL %d is older than 12: the relay needs plan_cache_mode", version)
	}

	return nil
}

func (o *Outbox) checkTable(ctx context.Context, table string) error {
	rows, err := o.db.QueryContext(ctx, `SELECT 1 FROM `+quoteIdent(table)+` LIMIT 0`)
	if err != nil {
		return err
	}
	defer rows.Close()

	return rows.Err()
}

func (o *Outbox) serverVersion(ctx context.Context) (int, error) {
	rows, err := o.db.QueryContext(ctx, `SELECT current_setting('server_version_num')::int`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	if !rows.Next() {
		return 0, errors.Join(errors.New("the version query returned no row"), rows.Err())
	}

	var version int
	if err = rows.Scan(&version); err != nil {
		return 0, err
	}

	return version, rows.Err()
}
