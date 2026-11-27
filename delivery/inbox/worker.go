package inbox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"time"
)

// WorkerOptions configure the worker of an inbox. Their zero values are the defaults.
type WorkerOptions struct {
	Slots int           // 10: queued messages that run at once on this pod
	Lease time.Duration // 2m: a queued message whose pod is lost runs again this long after its last heartbeat
	// Heartbeat extends the lease of a running message; at most Lease/3, so that a failed heartbeat is tried
	// again before the run is cut. Default Lease/3.
	Heartbeat time.Duration
	// HeartbeatDB runs the heartbeats; required. Give it a small pool of its own: a heartbeat waiting for a
	// connection of a busy pool cannot extend its lease, and its healthy run is cut.
	HeartbeatDB DB
	Poll        time.Duration // 200ms: how often free slots claim queued rows

	// RetentionEvery is how often expired done rows are deleted, RetentionBatch rows per transaction.
	RetentionEvery time.Duration // 10m
	RetentionBatch int           // 5000
	DeadRowsEvery  time.Duration // 60s: the dead-rows and queued-age gauges
}

const (
	defaultSlots          = 10
	defaultLease          = 2 * time.Minute
	defaultPoll           = 200 * time.Millisecond
	heartbeatsPerLease    = 3
	defaultRetentionEvery = 10 * time.Minute
	defaultRetentionBatch = 5000
	defaultDeadRowsEvery  = time.Minute
)

func (o WorkerOptions) withDefaults() WorkerOptions {
	if o.Slots <= 0 {
		o.Slots = defaultSlots
	}
	if o.Lease <= 0 {
		o.Lease = defaultLease
	}
	if o.Heartbeat <= 0 {
		o.Heartbeat = o.Lease / heartbeatsPerLease
	}
	if o.Poll <= 0 {
		o.Poll = defaultPoll
	}
	if o.RetentionEvery <= 0 {
		o.RetentionEvery = defaultRetentionEvery
	}
	if o.RetentionBatch <= 0 {
		o.RetentionBatch = defaultRetentionBatch
	}
	if o.DeadRowsEvery <= 0 {
		o.DeadRowsEvery = defaultDeadRowsEvery
	}

	return o
}

func (o WorkerOptions) check() error {
	if isNil(o.HeartbeatDB) {
		return errors.New("inbox: WorkerOptions.HeartbeatDB is required")
	}
	if o.Heartbeat > o.Lease/heartbeatsPerLease {
		return fmt.Errorf("inbox: Heartbeat %s is more than a third of Lease %s", o.Heartbeat, o.Lease)
	}

	return nil
}

// isNil also catches a typed nil, such as a (*sql.DB)(nil), which would panic at the first heartbeat.
func isNil(db DB) bool {
	if db == nil {
		return true
	}
	v := reflect.ValueOf(db)
	nilable := []reflect.Kind{
		reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan,
	}

	return slices.Contains(nilable, v.Kind()) && v.IsNil()
}

// Worker runs the queued rows of the consumers registered with its inbox (deferred messages, replayed dead
// letters), deletes the rows older than their retention, one pod at a time, and reports the dead rows.
// Every pod runs one.
type Worker struct {
	in   *Inbox
	opts WorkerOptions

	startOnce sync.Once
	stopOnce  sync.Once
	stop      chan struct{}
	done      chan struct{}

	running sync.WaitGroup
	free    chan struct{}
	beatDB  DB
}

// Worker checks opts; it does not touch the database.
func (in *Inbox) Worker(opts WorkerOptions) (*Worker, error) {
	opts = opts.withDefaults()
	if err := opts.check(); err != nil {
		return nil, err
	}

	return &Worker{in: in, opts: opts, stop: make(chan struct{}), done: make(chan struct{}),
		free: make(chan struct{}, opts.Slots), beatDB: opts.HeartbeatDB}, nil
}

// Start runs the worker until Stop. The queued messages run on ctx: the cancel of ctx cuts them, and they
// run again after their lease.
func (w *Worker) Start(ctx context.Context) {
	w.startOnce.Do(func() { go w.run(ctx) })
}

// Stop claims no more rows and waits for the running messages until ctx ends. A message still running then
// goes on until it ends or ctx of Start is cancelled.
func (w *Worker) Stop(ctx context.Context) error {
	w.stopOnce.Do(func() { close(w.stop) })

	started := true
	w.startOnce.Do(func() {
		started = false
		close(w.done)
	})
	if !started {
		return nil
	}

	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return errors.New("inbox: the worker did not stop in time: a queued message still running runs " +
			"again after its lease")
	}
}

func (w *Worker) run(ctx context.Context) {
	defer close(w.done)
	defer w.running.Wait()
	loop, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-w.stop:
			cancel()
		case <-loop.Done():
		}
	}()
	for range w.opts.Slots {
		w.free <- struct{}{}
	}

	poll := time.NewTicker(w.opts.Poll)
	defer poll.Stop()
	retention := time.NewTicker(w.opts.RetentionEvery)
	defer retention.Stop()
	gauges := time.NewTicker(w.opts.DeadRowsEvery)
	defer gauges.Stop()

	w.reportDeadRows(loop)
	w.reportQueuedAge(loop)
	for {
		select {
		case <-loop.Done():
			return
		case <-poll.C:
			w.claim(ctx, loop)
		case <-retention.C:
			if _, err := w.in.deleteExpired(loop, w.opts.RetentionBatch); err != nil && loop.Err() == nil {
				w.in.log.Error("inbox: the retention delete failed", "error", err)
			}
		case <-gauges.C:
			w.reportDeadRows(loop)
			w.reportQueuedAge(loop)
		}
	}
}

// claim fills the free slots; the claimed rows run on ctx, the claim itself on loop.
func (w *Worker) claim(ctx, loop context.Context) {
	n := len(w.free)
	if n == 0 {
		return
	}
	rows, err := w.in.claim(loop, n, w.opts.Lease)
	if err != nil {
		if loop.Err() == nil {
			w.in.log.Error("inbox: could not claim queued messages", "error", err)
		}

		return
	}
	for _, c := range rows {
		<-w.free
		w.running.Add(1)
		go func() {
			defer w.running.Done()
			defer func() { w.free <- struct{}{} }()
			w.runClaimed(ctx, c)
		}()
	}
}

func (w *Worker) reportDeadRows(ctx context.Context) {
	var n int
	err := w.in.db.QueryRowContext(ctx, w.in.q.deadRows, statusDead).Scan(&n)
	if err != nil {
		if ctx.Err() == nil {
			w.in.log.Error("inbox: could not count the dead rows", "error", err)
		}

		return
	}
	w.in.metrics.DeadRows(n)
}

// deleteExpired deletes the expired done rows, then the expired dead letters when DeadRetention is set, in
// batches, each in a transaction of its own under the lock, so that it neither holds long locks nor runs on
// two pods at once.
func (in *Inbox) deleteExpired(ctx context.Context, batch int) (int64, error) {
	total, err := in.deleteAll(ctx, in.q.deleteExpired, statusDone, in.opts.Retention, batch)
	if err != nil || in.opts.DeadRetention == 0 {
		return total, err
	}
	dead, err := in.deleteAll(ctx, in.q.deleteExpiredDead, statusDead, in.opts.DeadRetention, batch)

	return total + dead, err
}

func (in *Inbox) deleteAll(ctx context.Context, query string, status int, age time.Duration, batch int) (int64,
	error,
) {
	var total int64
	for ctx.Err() == nil {
		n, locked, err := in.deleteBatch(ctx, query, status, age, batch)
		total += n
		if err != nil || !locked || n < int64(batch) {
			return total, err
		}
	}

	return total, ctx.Err()
}

func (in *Inbox) deleteBatch(ctx context.Context, query string, status int, age time.Duration, batch int) (
	n int64, locked bool, err error,
) {
	tx, err := in.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = tx.Rollback() }()

	err = tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock($1)`, in.opts.LockKey).Scan(&locked)
	if err != nil || !locked {
		return 0, false, err
	}

	var res sql.Result
	res, err = tx.ExecContext(ctx, query, status, age.Seconds(), batch)
	if err != nil {
		return 0, true, err
	}
	if n, err = res.RowsAffected(); err != nil {
		return 0, true, err
	}

	return n, true, tx.Commit()
}
