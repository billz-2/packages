package inbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Defer queues the message and marks it at once; a Worker runs h later, one message of a key (key(m)) at a
// time across the pods, under a lease. For handlers that run longer than a partition may wait.
func (in *Inbox) Defer(consumer string, key func(m *Message) string, h func(ctx context.Context, m *Message) error,
	p RetryPolicy,
) Func {
	in.register(consumer, registration{run: h, policy: p, queued: true})

	return func(ctx context.Context, m *Message) error {
		k := key(m)
		var queued bool
		err := in.untilWritten(ctx, "queue", consumer, m, func(wctx context.Context) error {
			var err error
			queued, err = in.insert(wctx, in.db, consumer, m, row{status: statusQueued, key: &k})

			return err
		})
		switch {
		case unstorable(err):
			return in.deadOrSkip(ctx, consumer, m, 0, err)
		case err != nil:
			return err
		}
		if queued {
			in.metrics.Message(consumer, resultQueued)
		} else {
			in.metrics.Message(consumer, resultDuplicate)
		}

		return nil
	}
}

// Replay queues a dead letter again: the worker of a pod that registered its consumer runs it. It reports
// how many rows it queued (0 when there is no such dead row).
func (in *Inbox) Replay(ctx context.Context, consumer, eventID string) (int64, error) {
	res, err := in.db.ExecContext(ctx, in.q.replay, consumer, eventID)
	if err != nil {
		return 0, err
	}

	return res.RowsAffected()
}

// claimed is a queued row under this worker's lease; attempts fences its writes.
type claimed struct {
	consumer string
	m        *Message
	digest   []byte
	attempts int
	// leased is a local time no later than the claim's now() in the table: the lease ends Lease after it at
	// the earliest.
	leased time.Time
}

func (c claimed) pk() []any {
	return []any{c.consumer, c.m.ID, c.digest, c.attempts}
}

// claim leases up to n queued rows of the consumers registered here, under the lock: one claimer at a time.
func (in *Inbox) claim(ctx context.Context, n int, lease time.Duration) ([]claimed, error) {
	consumers, _ := in.consumers()
	if n <= 0 || len(consumers) == 0 {
		return nil, nil
	}
	names, err := json.Marshal(consumers)
	if err != nil {
		return nil, err
	}

	leased := time.Now()
	tx, err := in.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var locked bool
	if err = tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock($1)`, in.opts.LockKey).
		Scan(&locked); err != nil || !locked {
		return nil, err
	}

	rows, err := tx.QueryContext(ctx, in.q.claim, string(names), lease.Seconds(), n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []claimed
	for rows.Next() {
		var (
			c       claimed
			key     sql.NullString
			headers []byte
		)
		c.m = &Message{}
		c.leased = leased
		if err = rows.Scan(&c.consumer, &c.m.ID, &c.digest, &c.m.Topic, &c.m.Partition, &c.m.Offset, &key,
			&c.m.Value, &headers, &c.attempts); err != nil {
			return nil, err
		}
		if key.Valid {
			c.m.Key = []byte(key.String)
		}
		if len(headers) > 0 {
			if err = json.Unmarshal(headers, &c.m.Headers); err != nil {
				return nil, err
			}
		}
		out = append(out, c)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}

	return out, tx.Commit()
}

// errLeaseLost: another run took the row over, or this run could not extend its lease in time. The run is
// cut and its result dropped.
var errLeaseLost = errors.New("inbox: the lease of the row is lost")

// runClaimed runs a leased row and records its result under the fence of its attempt. A run cut by ctx, or
// by the loss of its lease, records nothing: another run takes the row.
func (w *Worker) runClaimed(ctx context.Context, c claimed) {
	in := w.in
	reg, ok := in.registration(c.consumer)
	if !ok {
		return
	}

	run, lose := context.WithCancelCause(ctx)
	defer lose(nil)
	beat, stopBeat := context.WithCancel(run)
	beating := make(chan struct{})
	go func() {
		defer close(beating)
		w.heartbeat(beat, c, lose)
	}()

	writes := &rowWrites{}
	err := in.call(c.consumer, c.m, func() error { return reg.run(context.WithValue(run, rowKey{}, writes), c.m) })
	stopBeat()
	<-beating
	if ctx.Err() != nil || errors.Is(context.Cause(run), errLeaseLost) {
		return
	}

	switch {
	case err == nil:
		w.finish(ctx, c, in.q.finishDone, resultDone, writes)
	case c.attempts < reg.policy.attempts() && !isPermanent(err):
		in.log.Warn("inbox: a queued message failed, it runs again", "consumer", c.consumer, "event_id", c.m.ID,
			"attempt", c.attempts, "error", err)
		w.finish(ctx, c, in.q.finishRetry, resultRetry, &rowWrites{}, reg.policy.pause(c.attempts).Seconds(),
			storableText(err.Error()))
	default:
		in.log.Error("inbox: a queued message failed, it is a dead letter", "consumer", c.consumer,
			"event_id", c.m.ID, "attempts", c.attempts, "error", err)
		w.finish(ctx, c, in.q.finishDead, resultDead, writes, storableText(err.Error()))
	}
}

// cutShare: a heartbeat waits at most half a heartbeat, and the run is cut half a heartbeat before its lease
// can end.
const cutShare = 2

// heartbeat extends the lease while the run lasts. It cuts the run when another run took the row over, and
// before the lease can end when it could not extend it. The lease is counted from when the heartbeat was
// sent, which is no later than its now() in the table, so a slow heartbeat does not stretch it.
func (w *Worker) heartbeat(ctx context.Context, c claimed, lose context.CancelCauseFunc) {
	ticker := time.NewTicker(w.opts.Heartbeat)
	defer ticker.Stop()
	extended := c.leased
	cutAt := func() time.Time { return extended.Add(w.opts.Lease - w.opts.Heartbeat/cutShare) }
	cut := time.NewTimer(time.Until(cutAt()))
	defer cut.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-cut.C:
			w.in.log.Error("inbox: the lease of a queued message may end before it is extended, the run is cut",
				"consumer", c.consumer, "event_id", c.m.ID)
			lose(errLeaseLost)

			return
		case <-ticker.C:
		}

		sent := time.Now()
		deadline := sent.Add(w.opts.Heartbeat / cutShare)
		if deadline.After(cutAt()) {
			deadline = cutAt()
		}
		bctx, cancel := context.WithDeadline(ctx, deadline)
		res, err := w.beatDB.ExecContext(bctx, w.in.q.heartbeat, append(c.pk(), w.opts.Lease.Seconds())...)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			var n int64
			if n, err = res.RowsAffected(); err == nil && n == 0 {
				w.in.log.Warn("inbox: another run took a queued message over, this run is cut", "consumer",
					c.consumer, "event_id", c.m.ID)
				lose(errLeaseLost)

				return
			}
		}
		if err == nil {
			extended = sent
			cut.Reset(time.Until(cutAt()))

			continue
		}
		w.in.log.Error("inbox: could not extend the lease of a queued message", "consumer", c.consumer,
			"event_id", c.m.ID, "error", err)
	}
}

// finish writes the result of a run with its writes, tried again until it is written or ctx ends. Writes
// the table refuses, or that panic, are dropped and the row becomes a dead letter.
func (w *Worker) finish(ctx context.Context, c claimed, query, result string, writes *rowWrites, extra ...any) {
	in := w.in
	lost, err := w.writeResult(ctx, c, query, writes, extra...)
	if unstorable(err) {
		in.log.Error("inbox: the result of a queued message or its writes cannot be stored, it is a dead letter "+
			"without them", "consumer", c.consumer, "event_id", c.m.ID, "error", err)
		result = resultDead
		lost, err = w.writeResult(ctx, c, in.q.finishDead, &rowWrites{}, storableText(err.Error()))
	}
	switch {
	case err != nil:
		in.log.Error("inbox: the result of a queued message is not recorded, it runs again after its lease",
			"consumer", c.consumer, "event_id", c.m.ID, "error", err)
	case lost:
		in.log.Warn("inbox: a queued message ran past its lease, another run took it over", "consumer",
			c.consumer, "event_id", c.m.ID)
	default:
		in.metrics.Message(c.consumer, result)
	}
}

func (w *Worker) writeResult(ctx context.Context, c claimed, query string, writes *rowWrites, extra ...any) (
	lost bool, err error,
) {
	in := w.in
	err = in.untilWritten(ctx, "record", c.consumer, c.m, func(wctx context.Context) error {
		tx, beginErr := in.db.BeginTx(wctx, nil)
		if beginErr != nil {
			return beginErr
		}
		defer func() { _ = tx.Rollback() }()

		res, execErr := tx.ExecContext(wctx, query, append(c.pk(), extra...)...)
		if execErr != nil {
			return execErr
		}
		if n, _ := res.RowsAffected(); n == 0 {
			lost = true

			return nil
		}
		if writeErr := in.runWrites(wctx, tx, c.consumer, c.m, writes); writeErr != nil {
			return writeErr
		}

		return tx.Commit()
	})

	return lost, err
}

func (w *Worker) reportQueuedAge(ctx context.Context) {
	_, deferred := w.in.consumers()
	if len(deferred) == 0 {
		return
	}

	rows, err := w.in.db.QueryContext(ctx, w.in.q.queuedAge)
	if err != nil {
		if ctx.Err() == nil {
			w.in.log.Error("inbox: could not read the age of the queued rows", "error", err)
		}

		return
	}
	defer rows.Close()

	ages := make(map[string]float64, len(deferred))
	for rows.Next() {
		var (
			consumer string
			age      float64
		)
		if err = rows.Scan(&consumer, &age); err != nil {
			return
		}
		ages[consumer] = age
	}
	if rows.Err() != nil {
		return
	}
	for _, consumer := range deferred {
		w.in.metrics.QueuedOldestAge(consumer, ages[consumer])
	}
}
