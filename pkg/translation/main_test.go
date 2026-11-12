package translation_test

import (
	"context"
	"log"
	"os"
	"testing"

	"github.com/jmoiron/sqlx"

	"github.com/billz-2/packages/pkg/test_environment"
)

var (
	ctx         = context.Background()
	db          *sqlx.DB
	databaseURL string
)

func TestMain(m *testing.M) {
	conn, url, closeFunc, err := test_environment.SetupPostgres(ctx, test_environment.Config{
		Environment:      "test",
		PostgresHost:     "localhost",
		PostgresUser:     "postgres",
		PostgresPassword: "postgres",
		PostgresDatabase: "translations",
	})
	if err != nil {
		log.Fatalf("setup postgres: %v", err)
	}

	db, databaseURL = conn, url

	schema, err := os.ReadFile("testdata/schema.sql")
	if err != nil {
		log.Fatalf("read schema: %v", err)
	}

	if _, err = db.ExecContext(ctx, string(schema)); err != nil {
		_ = closeFunc()
		log.Fatalf("apply schema: %v", err)
	}

	code := m.Run()

	_ = db.Close()
	_ = closeFunc()

	os.Exit(code)
}
