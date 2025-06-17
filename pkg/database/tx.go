package database

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/billz-2/packages/pkg/logger"
	"github.com/billz-2/packages/pkg/tracing"
	"github.com/jmoiron/sqlx"
	"go.opentelemetry.io/otel/attribute"
)

type tx struct {
	tx  *sqlx.Tx
	log logger.Logger
}

func NewTx(transaction *sqlx.Tx, log logger.Logger) DB {
	return &tx{
		tx:  transaction,
		log: log,
	}
}

func (t *tx) GetDB() *sqlx.DB {
	return nil
}

func (t *tx) BeginTx() (DB, error) {
	return nil, nil
}

func (t *tx) EndTx(txFunc func() error) error {
	var err error
	tx := t.tx
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback() // nolint
			panic(p)
		} else if err != nil {
			err = tx.Rollback()
		} else {
			err = tx.Commit()
		}
	}()
	if txFunc != nil {
		err = txFunc()
	}

	return err
}

func (t *tx) Rollback() error {
	return t.tx.Rollback()
}

func (t *tx) Commit() error {
	return t.tx.Commit()
}

func (t *tx) Exec(query string, args ...any) (sql.Result, error) {
	return t.tx.Exec(query, args...)
}

func (t *tx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	ctx, span := tracing.GetSpan(ctx, getCallingFunction())
	defer span.End()
	logger.Log.DebugWithCtx(ctx, "db ExecContext query", logger.Any("query", GetCleanQuery(query)), logger.Any("args", args))

	span.SetAttributes(attribute.String("query", query), attribute.String("args", fmt.Sprintf("%+v", args)))

	return t.tx.ExecContext(ctx, query, args...)
}

func (t *tx) Query(query string, args ...any) (*sql.Rows, error) {
	return t.tx.Query(query, args...)
}

func (t *tx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	ctx, span := tracing.GetSpan(ctx, getCallingFunction())
	defer span.End()
	logger.Log.DebugWithCtx(ctx, "db QueryContext query", logger.Any("query", GetCleanQuery(query)), logger.Any("args", args))

	span.SetAttributes(attribute.String("query", query), attribute.String("args", fmt.Sprintf("%+v", args)))

	return t.tx.QueryContext(ctx, query, args...)
}

func (t *tx) QueryRow(query string, args ...any) *sql.Row {
	return t.tx.QueryRow(query, args...)
}

func (t *tx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	ctx, span := tracing.GetSpan(ctx, getCallingFunction())
	defer span.End()
	logger.Log.DebugWithCtx(ctx, "db QueryRowContext query", logger.Any("query", GetCleanQuery(query)), logger.Any("args", args))

	span.SetAttributes(attribute.String("query", query), attribute.String("args", fmt.Sprintf("%+v", args)))
	return t.tx.QueryRowContext(ctx, query, args...)
}

func (t *tx) PrepareNamedContext(ctx context.Context, query string) (*sqlx.NamedStmt, error) {
	ctx, span := tracing.GetSpan(ctx, getCallingFunction())
	defer span.End()
	logger.Log.DebugWithCtx(ctx, "db PrepareNamedContext query", logger.Any("query", GetCleanQuery(query)))

	span.SetAttributes(attribute.String("query", query))

	return t.tx.PrepareNamedContext(ctx, query)
}

func (t *tx) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	ctx, span := tracing.GetSpan(ctx, getCallingFunction())
	defer span.End()
	logger.Log.DebugWithCtx(ctx, "db PrepareContext query", logger.Any("query", GetCleanQuery(query)))

	span.SetAttributes(attribute.String("query", query))

	return t.tx.PrepareContext(ctx, query)
}

func (t *tx) GetContext(ctx context.Context, dest any, query string, args ...any) error {
	ctx, span := tracing.GetSpan(ctx, getCallingFunction())
	defer span.End()
	logger.Log.DebugWithCtx(ctx, "db GetContext query", logger.Any("query", GetCleanQuery(query)), logger.Any("args", args))

	span.SetAttributes(attribute.String("query", query))

	return t.tx.GetContext(ctx, dest, query, args...)
}

func (t *tx) NamedExecContext(ctx context.Context, query string, arg any) (sql.Result, error) {
	ctx, span := tracing.GetSpan(ctx, getCallingFunction())
	defer span.End()
	logger.Log.DebugWithCtx(ctx, "db NamedExecContext query", logger.Any("query", GetCleanQuery(query)), logger.Any("args", arg))

	span.SetAttributes(attribute.String("query", query))

	return t.tx.NamedExecContext(ctx, query, arg)
}

func (t *tx) NamedQuery(query string, arg any) (*sqlx.Rows, error) {
	return t.tx.NamedQuery(query, arg)
}

func (t *tx) NamedQueryContext(ctx context.Context, query string, arg any) (*sqlx.Rows, error) {
	ctx, span := tracing.GetSpan(ctx, getCallingFunction())
	defer span.End()
	logger.Log.DebugWithCtx(ctx, "db NamedQueryContext query", logger.Any("query", GetCleanQuery(query)), logger.Any("args", arg))

	span.SetAttributes(attribute.String("query", query))

	return t.tx.NamedQuery(query, arg)
}

func (t *tx) SelectContext(ctx context.Context, dest any, query string, args ...any) error {
	ctx, span := tracing.GetSpan(ctx, getCallingFunction())
	defer span.End()
	logger.Log.DebugWithCtx(ctx, "db SelectContext query", logger.Any("query", GetCleanQuery(query)), logger.Any("args", args))

	span.SetAttributes(attribute.String("query", query))

	return t.tx.SelectContext(ctx, dest, query, args...)
}

func (t *tx) Ping(ctx context.Context) error {
	return nil
}

func (t *tx) Rebind(query string) string {
	return t.tx.Rebind(query)
}

func (t *tx) Prepare(query string) (*sql.Stmt, error) {
	return t.tx.Prepare(query)
}

func (t *tx) PrepareNamed(query string) (*sqlx.NamedStmt, error) {
	return t.tx.PrepareNamed(query)
}
