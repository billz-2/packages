package outbox

import (
	"embed"
	"fmt"
	"regexp"
	"strings"
	"text/template"
)

//go:embed migrations/*.tmpl
var migrationFiles embed.FS

var migrationTemplates = template.Must(template.ParseFS(migrationFiles, "migrations/*.tmpl"))

// Migration renders the DDL of the tables for opts. A service ships the result as its own numbered migration
// files, so that its migration tool applies it like any other.
func Migration(opts Options) (up, down string, err error) {
	opts = opts.withDefaults()
	if err = opts.checkNames(); err != nil {
		return "", "", err
	}

	data := struct{ Table, FailedTable, TopicIndex, TableName, FailedTableName string }{
		Table:           quoteIdent(opts.Table),
		FailedTable:     quoteIdent(opts.FailedTable),
		TopicIndex:      quoteIdent(opts.Table + "_topic_id"),
		TableName:       opts.Table,
		FailedTableName: opts.FailedTable,
	}

	var upSQL, downSQL strings.Builder
	if err = migrationTemplates.ExecuteTemplate(&upSQL, "outbox.up.sql.tmpl", data); err != nil {
		return "", "", err
	}
	if err = migrationTemplates.ExecuteTemplate(&downSQL, "outbox.down.sql.tmpl", data); err != nil {
		return "", "", err
	}

	return upSQL.String(), downSQL.String(), nil
}

// maxIdentifier is NAMEDATALEN - 1: PostgreSQL cuts a longer name without an error.
const maxIdentifier = 63

var identifier = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

func (o Options) checkNames() error {
	for _, name := range []string{o.Table, o.FailedTable, o.Table + "_topic_id"} {
		if !identifier.MatchString(name) || len(name) > maxIdentifier {
			return fmt.Errorf("outbox: table name %q: want a lower-case identifier of at most %d bytes", name,
				maxIdentifier)
		}
	}
	if o.Table == o.FailedTable {
		return fmt.Errorf("outbox: Table and FailedTable are both %q", o.Table)
	}

	return nil
}

// quoteIdent quotes a name that checkNames accepted: a reserved word is a valid table name too.
func quoteIdent(name string) string {
	return `"` + name + `"`
}
