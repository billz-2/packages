package inbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"time"
)

// Func is what the Consumer runs for a message. It returns nil once the message is recorded (done, a
// duplicate or a dead letter); an error leaves it unmarked.
type Func func(ctx context.Context, m *Message) error

const (
	statusDone   = 0
	statusQueued = 1
	statusDead   = 2
)

var errStopped = errors.New("inbox: stopped before the message was recorded")

// writeTimeout bounds one attempt at a row write; the write is tried again until it succeeds or the
// message's session ends.
const writeTimeout = 5 * time.Second

var writePauses = []time.Duration{200 * time.Millisecond, time.Second, 2 * time.Second, 5 * time.Second}

// After runs h with its own writes, then records the message as done. A message recorded before is
// skipped. Between h and the record a crash or a second delivery can run h twice.
func (in *Inbox) After(consumer string, h func(ctx context.Context, m *Message) error, p RetryPolicy) Func {
	in.register(consumer, registration{run: h, policy: p})

	return func(ctx context.Context, m *Message) error {
		seen, err := in.seen(ctx, consumer, m)
		switch {
		case unstorable(err):
			return in.deadOrSkip(ctx, consumer, m, 0, err)
		case err != nil:
			return err
		case seen:
			in.metrics.Message(consumer, resultDuplicate)

			return nil
		}

		for attempt := 1; ; attempt++ {
			writes := &rowWrites{}
			err = in.call(consumer, m, func() error { return h(context.WithValue(ctx, rowKey{}, writes), m) })
			if ctx.Err() != nil {
				return errStopped
			}
			if err == nil {
				return in.record(ctx, consumer, m, row{status: statusDone, attempts: attempt}, writes)
			}
			if again, failErr := in.failed(ctx, consumer, m, p, attempt, err, writes); !again {
				return failErr
			}
		}
	}
}

// failed pauses before the next attempt (again), or records the dead letter with the last attempt's writes.
func (in *Inbox) failed(ctx context.Context, consumer string, m *Message, p RetryPolicy, attempt int,
	cause error, writes *rowWrites,
) (again bool, err error) {
	if attempt < p.attempts() && !isPermanent(cause) {
		in.metrics.Message(consumer, resultRetry)
		in.log.Warn("inbox: the handler failed, it runs again", "consumer", consumer, "event_id", m.ID,
			"attempt", attempt, "error", cause)
		if !pause(ctx, p.pause(attempt)) {
			return false, errStopped
		}

		return true, nil
	}

	in.log.Error("inbox: the handler failed, the message is a dead letter", "consumer", consumer,
		"event_id", m.ID, "topic", m.Topic, "partition", m.Partition, "offset", m.Offset, "attempts", attempt,
		"error", cause)

	return false, in.record(ctx, consumer, m, row{status: statusDead, attempts: attempt, cause: cause}, writes)
}

// call recovers a panic of the handler as a permanent error: it would panic again on the same message.
func (in *Inbox) call(consumer string, m *Message, f func() error) (err error) {
	defer func() {
		if v := recover(); v != nil {
			in.log.Error("inbox: the handler panicked", "consumer", consumer, "event_id", m.ID, "panic", v,
				"stack", string(debug.Stack()))
			err = Permanent(fmt.Errorf("panic: %v", v))
		}
	}()

	return f()
}

type row struct {
	status   int
	attempts int
	cause    error
	// key replaces the record's key: the serialisation key of a queued message
	key *string
}

func (r row) result() string {
	if r.status == statusDead {
		return resultDead
	}

	return resultDone
}

// record writes the row with the last attempt's writes. When the table refuses them, or a write panics, the
// message is kept as a dead letter without them: trying again would hold the partition for ever.
func (in *Inbox) record(ctx context.Context, consumer string, m *Message, r row, writes *rowWrites) error {
	err := in.writeRow(ctx, consumer, m, r, writes)
	if !unstorable(err) {
		return err
	}
	in.log.Error("inbox: the message's row or its writes cannot be stored, it is a dead letter without them",
		"consumer", consumer, "event_id", m.ID, "error", err)

	return in.deadOrSkip(ctx, consumer, m, r.attempts, err)
}

// deadOrSkip keeps what it can of a message the table refuses: a dead letter, else a log line and a skip.
func (in *Inbox) deadOrSkip(ctx context.Context, consumer string, m *Message, attempts int, cause error) error {
	err := in.writeRow(ctx, consumer, m, row{status: statusDead, attempts: attempts, cause: cause}, &rowWrites{})
	if !unstorable(err) {
		return err
	}
	in.metrics.Message(consumer, resultSkipped)
	in.log.Error("inbox: the message cannot be stored, it is skipped", "consumer", consumer, "event_id", m.ID,
		"topic", m.Topic, "partition", m.Partition, "offset", m.Offset, "error", err)

	return nil
}

func (in *Inbox) writeRow(ctx context.Context, consumer string, m *Message, r row, writes *rowWrites) error {
	err := in.untilWritten(ctx, "record", consumer, m, func(wctx context.Context) error {
		tx, err := in.db.BeginTx(wctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()

		inserted, err := in.insert(wctx, tx, consumer, m, r)
		if err != nil || !inserted {
			return err
		}
		if err = in.runWrites(wctx, tx, consumer, m, writes); err != nil {
			return err
		}

		return tx.Commit()
	})
	if err == nil {
		in.metrics.Message(consumer, r.result())
	}

	return err
}

func (in *Inbox) runWrites(ctx context.Context, tx *sql.Tx, consumer string, m *Message, w *rowWrites) (err error) {
	defer func() {
		if v := recover(); v != nil {
			in.log.Error("inbox: a row write panicked", "consumer", consumer, "event_id", m.ID, "panic", v,
				"stack", string(debug.Stack()))
			err = Permanent(fmt.Errorf("panic in a row write: %v", v))
		}
	}()

	for _, fn := range w.fns {
		if err = fn(ctx, tx); err != nil {
			return err
		}
	}

	return nil
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func (in *Inbox) insert(ctx context.Context, tx execer, consumer string, m *Message, r row) (bool, error) {
	var (
		payload any
		headers any
		cause   sql.NullString
		key     sql.NullString
	)
	if r.status != statusDone {
		payload = append([]byte{}, m.Value...)
		stored := make([]Header, len(m.Headers))
		for i, h := range m.Headers {
			stored[i] = Header{Key: storableText(h.Key), Value: h.Value}
		}
		encoded, err := json.Marshal(stored)
		if err != nil {
			return false, err
		}
		headers = string(encoded)
	}
	if r.cause != nil {
		cause = sql.NullString{String: storableText(r.cause.Error()), Valid: true}
	}
	k := string(m.Key)
	if r.key != nil {
		k = *r.key
	}
	if k != "" && storableText(k) == k {
		key = sql.NullString{String: k, Valid: true}
	}

	res, err := tx.ExecContext(ctx, in.q.insert, consumer, m.ID, m.digest(), r.status, m.Topic, m.Partition,
		m.Offset, key, payload, headers, r.attempts, cause)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()

	return n == 1, err
}

// storableText is s as a TEXT or JSONB column takes it: valid UTF-8, no NUL.
func storableText(s string) string {
	return strings.ToValidUTF8(strings.ReplaceAll(s, "\x00", ""), "�")
}

func (in *Inbox) seen(ctx context.Context, consumer string, m *Message) (bool, error) {
	var seen bool
	err := in.untilWritten(ctx, "dedup check", consumer, m, func(wctx context.Context) error {
		return in.db.QueryRowContext(wctx, in.q.seen, consumer, m.ID, m.digest()).Scan(&seen)
	})

	return seen, err
}

// unstorable reports an error that a write would meet again: a permanent one, or a value the table refuses
// (SQLSTATE class 22, data exception, or 54, program limit exceeded).
func unstorable(err error) bool {
	if err == nil {
		return false
	}
	if isPermanent(err) {
		return true
	}

	var coded interface{ SQLState() string }
	if errors.As(err, &coded) {
		state := coded.SQLState()

		return strings.HasPrefix(state, "22") || strings.HasPrefix(state, "54")
	}

	return false
}

// untilWritten tries op until it succeeds or the message's session ends. It runs also once the handler's
// context is cancelled: a handler that finished must not lose its record to the shutdown.
func (in *Inbox) untilWritten(ctx context.Context, what, consumer string, m *Message,
	op func(ctx context.Context) error,
) error {
	for attempt := 0; ; attempt++ {
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
		err := op(wctx)
		cancel()
		if err == nil || unstorable(err) {
			return err
		}
		in.log.Error("inbox: "+what+" failed, trying again", "consumer", consumer, "event_id", m.ID, "error", err)
		if !pause(ctx, writePauses[min(attempt, len(writePauses)-1)]) {
			return errors.Join(errStopped, err)
		}
	}
}

type (
	stopKey struct{}
	rowKey  struct{}
)

// withStop makes the pauses of the inbox end when stop is closed: the consumer's session or the consumer
// ended, and the message is left to the next owner of its partition.
func withStop(ctx context.Context, stop <-chan struct{}) context.Context {
	return context.WithValue(ctx, stopKey{}, stop)
}

// pause reports false when ctx or its stop signal ended first.
func pause(ctx context.Context, d time.Duration) bool {
	stop, _ := ctx.Value(stopKey{}).(<-chan struct{})
	select {
	case <-stop:
		return false
	default:
	}

	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-stop:
		return false
	case <-timer.C:
		return true
	}
}

type rowWrites struct {
	fns    []func(ctx context.Context, tx *sql.Tx) error
	failed []func(ctx context.Context, tx *sql.Tx) error
}

// ifFailed are the writes of an attempt whose own work did not commit: its OnFailedRow writes, or its OnRow
// writes when it gave none.
func (w *rowWrites) ifFailed() *rowWrites {
	if len(w.failed) == 0 {
		return w
	}

	return &rowWrites{fns: w.failed}
}

// OnRow makes fn part of the transaction that records the message: its done row, or its dead letter after
// the last attempt. Only the last attempt's fns run. It reports false outside an inbox handler.
func OnRow(ctx context.Context, fn func(ctx context.Context, tx *sql.Tx) error) bool {
	w, ok := ctx.Value(rowKey{}).(*rowWrites)
	if ok {
		w.fns = append(w.fns, fn)
	}

	return ok
}

// OnFailedRow is fn in place of the OnRow writes when the handler's own work does not commit while the message
// is recorded: an InTx message whose statement aborted its transaction, or the dead letter of an InTx
// consumer. A handler that answered success gives its failure answer here. It reports false outside an inbox
// handler.
func OnFailedRow(ctx context.Context, fn func(ctx context.Context, tx *sql.Tx) error) bool {
	w, ok := ctx.Value(rowKey{}).(*rowWrites)
	if ok {
		w.failed = append(w.failed, fn)
	}

	return ok
}

type causeKey struct{}

type causeSlot struct{ err error }

// WithCause gives ctx a place for the error behind a handler's failure, for a caller that decides from it
// whether to run the handler again. The returned func reports what the handler left there (SetCause).
func WithCause(ctx context.Context) (context.Context, func() error) {
	c := &causeSlot{}

	return context.WithValue(ctx, causeKey{}, c), func() error { return c.err }
}

// SetCause leaves err in the place WithCause gave ctx; a later call replaces it. Without one it does nothing.
func SetCause(ctx context.Context, err error) {
	if c, ok := ctx.Value(causeKey{}).(*causeSlot); ok && err != nil {
		c.err = err
	}
}

// Detach drops the handler's transaction (Tx), row writes and place for a cause from ctx, for work that
// outlives the handler.
func Detach(ctx context.Context) context.Context {
	return detached{Context: ctx}
}

type detached struct{ context.Context }

func (d detached) Value(key any) any {
	switch key.(type) {
	case rowKey, causeKey, afterKey, txKey:
		return nil
	}

	return d.Context.Value(key)
}
