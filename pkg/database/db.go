package database

import (
	"context"
	"database/sql"
	"fmt"
	"runtime"

	"github.com/billz-2/packages/pkg/logger"
	"github.com/billz-2/packages/pkg/tracing"
	"github.com/jmoiron/sqlx"
	"go.opentelemetry.io/otel/attribute"
)

type db struct {
	db  *sqlx.DB
	log logger.Logger
}

func NewDB(database *sqlx.DB, log logger.Logger) DB {
	return &db{
		db:  database,
		log: log,
	}
}

func (d *db) GetDB() *sqlx.DB {
	return d.db
}

func (d *db) BeginTx() (DB, error) {
	tx, err := d.db.BeginTxx(context.Background(), nil)
	if err != nil {
		return nil, err
	}

	return NewTx(tx, logger.Log), nil
}

func (d *db) EndTx(txFunc func() error) error {
	return nil
}

func (d *db) Rollback() error {
	return nil
}

func (d *db) Commit() error {
	return nil
}

func (d *db) Exec(query string, args ...any) (sql.Result, error) {
	return d.db.Exec(query, args...)
}

func (d *db) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	ctx, span := tracing.GetSpan(ctx, getCallingFunction())
	defer span.End()
	logger.Log.DebugWithCtx(ctx, "db ExecContext query", logger.Any("query", GetCleanQuery(query)), logger.Any("args", args))

	span.SetAttributes(attribute.String("query", query), attribute.String("args", fmt.Sprintf("%+v", args)))

	return d.db.ExecContext(ctx, query, args...)
}

func (d *db) Query(query string, args ...any) (*sql.Rows, error) {
	return d.db.Query(query, args...)
}

func (d *db) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	ctx, span := tracing.GetSpan(ctx, getCallingFunction())
	defer span.End()
	logger.Log.DebugWithCtx(ctx, "db QueryContext query", logger.Any("query", GetCleanQuery(query)), logger.Any("args", args))

	span.SetAttributes(attribute.String("query", query), attribute.String("args", fmt.Sprintf("%+v", args)))

	return d.db.QueryContext(ctx, query, args...)
}

func (d *db) QueryRow(query string, args ...any) *sql.Row {
	return d.db.QueryRow(query, args...)
}

func (d *db) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	ctx, span := tracing.GetSpan(ctx, getCallingFunction())
	defer span.End()
	logger.Log.DebugWithCtx(ctx, "db QueryRowContext query", logger.Any("query", GetCleanQuery(query)), logger.Any("args", args))

	span.SetAttributes(attribute.String("query", query), attribute.String("args", fmt.Sprintf("%+v", args)))

	return d.db.QueryRowContext(ctx, query, args...)
}

func (d *db) NamedQuery(query string, arg any) (*sqlx.Rows, error) {
	return d.db.NamedQuery(query, arg)
}

func (d *db) NamedQueryContext(ctx context.Context, query string, arg any) (*sqlx.Rows, error) {
	ctx, span := tracing.GetSpan(ctx, getCallingFunction())
	defer span.End()
	logger.Log.DebugWithCtx(ctx, "db NamedQueryContext query", logger.Any("query", GetCleanQuery(query)), logger.Any("arg", arg))

	span.SetAttributes(attribute.String("query", query), attribute.String("arg", fmt.Sprintf("%+v", arg)))

	return d.db.NamedQueryContext(ctx, query, arg)
}

func (d *db) PrepareNamedContext(ctx context.Context, query string) (*sqlx.NamedStmt, error) {
	ctx, span := tracing.GetSpan(ctx, getCallingFunction())
	defer span.End()
	logger.Log.DebugWithCtx(ctx, "db PrepareNamedContext query", logger.Any("query", GetCleanQuery(query)))

	span.SetAttributes(attribute.String("query", query))

	return d.db.PrepareNamedContext(ctx, query)
}

func (d *db) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	ctx, span := tracing.GetSpan(ctx, getCallingFunction())
	defer span.End()
	logger.Log.DebugWithCtx(ctx, "db PrepareContext query", logger.Any("query", GetCleanQuery(query)))

	span.SetAttributes(attribute.String("query", query))

	return d.db.PrepareContext(ctx, query)
}

func (d *db) NamedExec(query string, arg any) (sql.Result, error) {
	return d.db.NamedExec(query, arg)
}

func (d *db) NamedExecContext(ctx context.Context, query string, arg any) (sql.Result, error) {
	ctx, span := tracing.GetSpan(ctx, getCallingFunction())
	defer span.End()
	logger.Log.DebugWithCtx(ctx, "db NamedExecContext query", logger.Any("query", GetCleanQuery(query)), logger.Any("arg", arg))

	span.SetAttributes(attribute.String("query", query), attribute.String("arg", fmt.Sprintf("%+v", arg)))

	return d.db.NamedExecContext(ctx, query, arg)
}

func (d *db) GetContext(ctx context.Context, dest any, query string, args ...any) error {
	ctx, span := tracing.GetSpan(ctx, getCallingFunction())
	defer span.End()
	logger.Log.DebugWithCtx(ctx, "db GetContext query", logger.Any("query", GetCleanQuery(query)), logger.Any("args", args))

	span.SetAttributes(attribute.String("query", query), attribute.String("args", fmt.Sprintf("%+v", args)))

	return d.db.GetContext(ctx, dest, query, args...)
}

func (d *db) SelectContext(ctx context.Context, dest any, query string, args ...any) error {
	ctx, span := tracing.GetSpan(ctx, getCallingFunction())
	defer span.End()
	logger.Log.DebugWithCtx(ctx, "db SelectContext query", logger.Any("query", GetCleanQuery(query)), logger.Any("args", args))

	span.SetAttributes(attribute.String("query", query), attribute.String("args", fmt.Sprintf("%+v", args)))

	return d.db.SelectContext(ctx, dest, query, args...)
}

func (d *db) Prepare(query string) (*sql.Stmt, error) {
	return d.db.Prepare(query)
}

func (d *db) PrepareNamed(query string) (*sqlx.NamedStmt, error) {
	return d.db.PrepareNamed(query)
}

func (d *db) Rebind(query string) string {
	return d.db.Rebind(query)
}

func (d *db) Ping(ctx context.Context) error {
	return d.db.PingContext(ctx)
}

func getCallingFunction() string {
	pc, _, _, _ := runtime.Caller(2)
	callingFunc := runtime.FuncForPC(pc).Name()
	return fmt.Sprintf("%s db query", callingFunc)
}
