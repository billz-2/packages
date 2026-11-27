package inbox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"
)

// slowTx is the length of an InTx transaction that is logged: it holds its row locks, and a connection, for
// all of the handler, HTTP calls to other services included.
const slowTx = 10 * time.Second

// TxStart begins the transaction of an InTx message on the service's database. end, if not nil, is called once
// the transaction committed or rolled back, and may be called again after that.
type TxStart func(ctx context.Context) (tx *sql.Tx, end func(), err error)

type txKey struct{}

// Tx is the transaction of the InTx message whose handler ctx belongs to; false elsewhere. The handler's
// statements that must commit with the message run on it. The inbox swaps no pool or connection for it: a
// statement that goes elsewhere runs outside the message's transaction.
func Tx(ctx context.Context) (*sql.Tx, bool) {
	tx, ok := ctx.Value(txKey{}).(*sql.Tx)

	return tx, ok
}

type abortError struct{ err error }

func (e abortError) Error() string { return e.err.Error() }
func (e abortError) Unwrap() error { return e.err }

// Abort marks the error of an InTx handler whose work must not commit, whatever it answered: the attempt's
// work is rolled back and the message is done at once with its failure writes (OnFailedRow, else OnRow), as
// when one of its statements aborted the transaction. Another run would fail the same way. Elsewhere it is
// the error it wraps.
func Abort(err error) error {
	if err == nil {
		return nil
	}

	return abortError{err: err}
}

// commitError marks a failure of the transaction itself, not of the handler: the message is run again by its
// consumer, without counting an attempt.
type commitError struct{ err error }

func (e commitError) Error() string { return e.err.Error() }
func (e commitError) Unwrap() error { return e.err }

// InTx runs h, its row writes and the message's done row in one transaction, begun by begin: all of it commits,
// or none of it. h gets the transaction as Tx(ctx); its statements on it commit with the message. The row is
// claimed before h runs, so a second delivery waits for the first one's commit and is then skipped, and a run
// cut before the commit leaves nothing. The functions h hands to AfterCommit run once the transaction
// committed; those of a rolled-back attempt are dropped.
//
// An attempt that h fails, with an error that is not Permanent, runs again by p in a new transaction, and the
// last one is a dead letter with its failure writes (OnFailedRow); return the error of a transient cause
// (Transient), and answer any other failure with nil and failure writes. When h returns nil after one of its
// statements aborted the transaction, or returns an Abort error, its work is rolled back and the message is
// done with its failure writes, at once: another run would fail the same way.
func (in *Inbox) InTx(consumer string, begin TxStart, h func(ctx context.Context, m *Message) error,
	p RetryPolicy,
) Func {
	in.register(consumer, registration{run: in.inOwnTx(consumer, begin, h), policy: p})

	return func(ctx context.Context, m *Message) error {
		for attempt := 1; ; attempt++ {
			writes, err := in.attemptInTx(ctx, consumer, begin, h, m, attempt)
			var commitErr commitError
			switch {
			case err == nil:
				return nil
			case unstorable(err):
				return in.deadOrSkip(ctx, consumer, m, attempt, err)
			case errors.As(err, &commitErr):
				return commitErr.err
			case ctx.Err() != nil:
				return errStopped
			}
			if again, failErr := in.failed(ctx, consumer, m, p, attempt, err, writes); !again {
				return failErr
			}
		}
	}
}

// attemptInTx returns no error once the message is committed or found done; a commitError when the transaction
// could not be begun, claimed or committed; else the handler's error with the attempt's failure writes.
func (in *Inbox) attemptInTx(ctx context.Context, consumer string, begin TxStart,
	h func(ctx context.Context, m *Message) error, m *Message, attempt int,
) (*rowWrites, error) {
	release, err := in.txSlot(ctx, consumer)
	if err != nil {
		return nil, commitError{err}
	}
	tx, end, err := begin(ctx)
	if err != nil {
		release()

		return nil, commitError{err}
	}
	mtx := &messageTx{in: in, tx: tx, end: end, release: release, consumer: consumer, eventID: m.ID,
		start: time.Now()}
	defer mtx.close()

	claimed, err := in.insert(ctx, tx, consumer, m, row{status: statusDone, attempts: attempt})
	switch {
	case unstorable(err):
		return nil, err
	case err != nil:
		return nil, commitError{err}
	case !claimed:
		in.metrics.Message(consumer, resultDuplicate)

		return &rowWrites{}, nil
	}

	if _, err = tx.ExecContext(ctx, "SAVEPOINT "+handlerSavepoint); err != nil {
		return nil, commitError{err}
	}
	writes := &rowWrites{}
	after := &afterCommits{}
	hctx := context.WithValue(context.WithValue(context.WithValue(ctx, txKey{}, tx), rowKey{}, writes), afterKey{},
		after)
	err = in.call(consumer, m, func() error { return h(hctx, m) })
	var abort abortError
	if err != nil && !errors.As(err, &abort) {
		return writes.ifFailed(), err
	}
	if err == nil {
		if err = usable(ctx, tx); err != nil && !aborted(err) {
			return nil, commitError{err}
		}
	}
	if err != nil {
		// The handler aborted its work, or answered after one of its statements failed: another run would fail
		// again, so its work is rolled back and the message is done with its failure answer, as when that
		// statement ended the handler on its own connection.
		in.log.Warn("inbox: the handler's work is rolled back", "consumer", consumer, "event_id", m.ID,
			"error", err)
		if _, err = tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+handlerSavepoint); err != nil {
			return nil, commitError{fmt.Errorf("inbox: roll back the handler's work: %w", err)}
		}
		writes, after = writes.ifFailed(), &afterCommits{}
	}
	if err = in.runWrites(ctx, tx, consumer, m, writes); err != nil {
		if unstorable(err) {
			return nil, err
		}

		return nil, commitError{err}
	}
	if err = tx.Commit(); err != nil {
		return nil, commitError{err}
	}
	mtx.committed = true
	mtx.close()
	in.metrics.Message(consumer, resultDone)
	after.run()

	return &rowWrites{}, nil
}

// messageTx is the transaction of one InTx attempt.
type messageTx struct {
	in        *Inbox
	tx        *sql.Tx
	end       func()
	release   func()
	consumer  string
	eventID   string
	start     time.Time
	committed bool
	closed    bool
}

// close ends the transaction before the AfterCommit work, which neither holds the connection nor counts in its
// duration. It may be called again.
func (t *messageTx) close() {
	if t.closed {
		return
	}
	t.closed = true
	if !t.committed {
		_ = t.tx.Rollback()
	}
	if t.end != nil {
		t.end()
	}
	t.release()
	took := time.Since(t.start)
	t.in.metrics.TxDuration(t.consumer, took.Seconds())
	if took > slowTx {
		t.in.log.Warn("inbox: a message's transaction was long", "consumer", t.consumer, "event_id", t.eventID,
			"took", took.String(), "committed", t.committed)
	}
}

// inOwnTx is h for the worker, which runs a replayed dead letter: h's writes commit in a transaction of their
// own, before the worker records the row.
func (in *Inbox) inOwnTx(consumer string, begin TxStart, h func(ctx context.Context, m *Message) error,
) func(ctx context.Context, m *Message) error {
	return func(ctx context.Context, m *Message) error {
		release, err := in.txSlot(ctx, consumer)
		if err != nil {
			return err
		}
		defer release()
		tx, end, err := begin(ctx)
		if err != nil {
			return err
		}
		if end == nil {
			end = func() {}
		}
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback()
			}
			end()
		}()

		after := &afterCommits{}
		if err = h(context.WithValue(context.WithValue(ctx, txKey{}, tx), afterKey{}, after), m); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		committed = true
		end()
		release()
		after.run()

		return nil
	}
}

// LimitTx lets at most n InTx transactions be open at once, n > 0; the others wait for a slot. Each holds a
// connection of the pool its TxStart begins in for the whole handler, so n below that pool's size keeps
// connections for the service's other work. It is set before the consumers run.
func (in *Inbox) LimitTx(n int) {
	if n > 0 {
		in.txSlots = make(chan struct{}, n)
	}
}

// TxLimit is the number of transaction slots of LimitTx, 0 without a limit.
func (in *Inbox) TxLimit() int {
	return cap(in.txSlots)
}

// txSlot waits for a transaction slot of LimitTx, or until ctx or its stop signal ends: a revoked or stopped
// message waits no more and is left unmarked. release frees the slot and may be called again.
func (in *Inbox) txSlot(ctx context.Context, consumer string) (release func(), err error) {
	if in.txSlots == nil {
		return func() {}, nil
	}
	stop, _ := ctx.Value(stopKey{}).(<-chan struct{})
	select {
	case <-stop:
		return nil, errStopped
	default:
	}
	start := time.Now()
	select {
	case in.txSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-stop:
		return nil, errStopped
	}
	in.metrics.TxSlotWait(consumer, time.Since(start).Seconds())
	var once sync.Once

	return func() { once.Do(func() { <-in.txSlots }) }, nil
}

type afterKey struct{}

type afterCommits struct{ fns []func() }

func (a *afterCommits) run() {
	for _, fn := range a.fns {
		fn()
	}
}

// AfterCommit runs fn once the transaction of the InTx message in ctx committed, and never if it rolls back.
// It reports false outside such a message: the caller runs fn itself.
func AfterCommit(ctx context.Context, fn func()) bool {
	a, ok := ctx.Value(afterKey{}).(*afterCommits)
	if ok {
		a.fns = append(a.fns, fn)
	}

	return ok
}

// handlerSavepoint follows the claim of the row, so that the handler's work can be rolled back alone.
const handlerSavepoint = "inbox_handler"

// usable fails when a statement of the handler failed and PostgreSQL aborted the transaction: nothing the
// handler did can commit.
func usable(ctx context.Context, tx *sql.Tx) error {
	var one int

	return tx.QueryRowContext(ctx, `SELECT 1`).Scan(&one)
}

// aborted is in_failed_sql_transaction: the transaction refuses every statement until its end.
func aborted(err error) bool {
	var coded interface{ SQLState() string }

	return errors.As(err, &coded) && coded.SQLState() == "25P02"
}
