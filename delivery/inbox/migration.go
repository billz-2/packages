package inbox

import (
	"embed"
	"strings"
	"text/template"
)

//go:embed migrations/*.tmpl
var migrationFiles embed.FS

var migrationTemplates = template.Must(template.ParseFS(migrationFiles, "migrations/*.tmpl"))

// Migration renders the DDL of the table for opts. A service ships the result as its own numbered migration
// files, so that its migration tool applies it like any other.
func Migration(opts Options) (up, down string, err error) {
	opts = opts.withDefaults()
	if err = opts.check(); err != nil {
		return "", "", err
	}

	names := indexNames(opts.Table)
	data := struct{ Table, QueuedIndex, DeadIndex, ReceivedIndex, TableName string }{
		Table:         quoteIdent(names[0]),
		QueuedIndex:   quoteIdent(names[1]),
		DeadIndex:     quoteIdent(names[2]),
		ReceivedIndex: quoteIdent(names[3]),
		TableName:     opts.Table,
	}

	var upSQL, downSQL strings.Builder
	if err = migrationTemplates.ExecuteTemplate(&upSQL, "inbox.up.sql.tmpl", data); err != nil {
		return "", "", err
	}
	if err = migrationTemplates.ExecuteTemplate(&downSQL, "inbox.down.sql.tmpl", data); err != nil {
		return "", "", err
	}

	return upSQL.String(), downSQL.String(), nil
}
