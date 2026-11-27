package outbox_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/billz-2/packages/delivery/outbox"
)

func TestMigration_RendersTableNames(t *testing.T) {
	up, down, err := outbox.Migration(outbox.Options{Table: "outbox_x", FailedTable: "outbox_x_failed"})
	require.NoError(t, err)

	assert.Contains(t, up, `CREATE TABLE IF NOT EXISTS "outbox_x" (`)
	assert.Contains(t, up, `CREATE INDEX IF NOT EXISTS "outbox_x_topic_id" ON "outbox_x" (topic, id);`)
	assert.Contains(t, up, `CREATE TABLE IF NOT EXISTS "outbox_x_failed" (`)
	assert.Contains(t, down, `'outbox_x still has rows: they would be lost'`)
	assert.Contains(t, down, `DROP TABLE IF EXISTS "outbox_x_failed";`)
	for _, sql := range []string{up, down} {
		assert.NotContains(t, sql, `"outbox"`)
		assert.NotContains(t, sql, "{{")
	}
}

func TestMigration_RefusesBadNames(t *testing.T) {
	for _, opts := range []outbox.Options{
		{Table: "Outbox"},
		{Table: "outbox; DROP TABLE x"},
		{Table: `out"box`},
		{Table: strings.Repeat("o", 55)}, // its index name is 64 bytes
		{Table: "same", FailedTable: "same"},
	} {
		_, _, err := outbox.Migration(opts)
		require.Error(t, err, "%+v", opts)
	}

	_, _, err := outbox.Migration(outbox.Options{Table: strings.Repeat("o", 54)})
	assert.NoError(t, err)
}
