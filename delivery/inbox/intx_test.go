package inbox //nolint:testpackage // the transaction tests share the PostgreSQL helpers of inbox_test.go

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// txStarts is the TxStart of the tests: every begin is ended. most is the
// largest number of its transactions open at once.
type txStarts struct {
	db     *sql.DB
	begun  atomic.Int32
	ended  atomic.Int32
	active atomic.Bool
	open   atomic.Int32
	most   atomic.Int32
	// fail makes the next begin fail
	fail atomic.Bool
}

func (s *txStarts) begin(ctx context.Context) (*sql.Tx, func(), error) {
	if s.fail.CompareAndSwap(true, false) {
		return nil, nil, errors.New("connection refused")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	s.begun.Add(1)
	s.active.Store(true)
	if n := s.open.Add(1); n > s.most.Load() {
		s.most.Store(n)
	}
	var once sync.Once

	return tx, func() {
		once.Do(func() {
			s.open.Add(-1)
			s.active.Store(false)
			s.ended.Add(1)
		})
	}, nil
}

// effectInTx is the handler's own write, in the message's transaction.
func effectInTx(ctx context.Context, note string) error {
	tx, ok := Tx(ctx)
	if !ok {
		return errors.New("no transaction in ctx")
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO effects (note) VALUES ($1)`, note)

	return err
}

func TestInTx_TheHandlerItsWritesAndTheRowCommitTogether(t *testing.T) {
	env := testDB(t)
	starts := &txStarts{db: env.db}
	var seenAfterCommit []string
	f := env.in.InTx("t", starts.begin, func(ctx context.Context, _ *Message) error {
		OnRow(ctx, effect("response"))
		require.True(t, AfterCommit(ctx, func() { seenAfterCommit = env.effects(t) }))

		return effectInTx(ctx, "stock")
	}, quickRetry)

	require.NoError(t, f(context.Background(), message("id-1", "v")))
	assert.Equal(t, []string{"response", "stock"}, env.effects(t))
	assert.Equal(t, 1, env.count(t, "status = 0 AND attempts = 1"))
	assert.Equal(t, []string{"response", "stock"}, seenAfterCommit, "AfterCommit runs once it is committed")
	assert.Equal(t, starts.begun.Load(), starts.ended.Load())

	require.NoError(t, f(context.Background(), message("id-1", "v")), "delivered again")
	assert.Equal(t, []string{"response", "stock"}, env.effects(t), "a duplicate runs nothing")
	assert.Equal(t, starts.begun.Load(), starts.ended.Load(), "a duplicate's transaction is ended too")
}

func TestInTx_AFailedAttemptLeavesNothingAndTheNextOneCommits(t *testing.T) {
	env := testDB(t)
	starts := &txStarts{db: env.db}
	var runs atomic.Int32
	var afterRuns []int32
	f := env.in.InTx("t", starts.begin, func(ctx context.Context, _ *Message) error {
		n := runs.Add(1)
		AfterCommit(ctx, func() { afterRuns = append(afterRuns, n) })
		if err := effectInTx(ctx, fmt.Sprintf("stock %d", n)); err != nil {
			return err
		}
		OnRow(ctx, effect(fmt.Sprintf("response %d", n)))
		if n < 3 {
			return errors.New("deadlock")
		}

		return nil
	}, quickRetry)

	require.NoError(t, f(context.Background(), message("id-1", "v")))
	assert.Equal(t, int32(3), runs.Load())
	assert.Equal(t, []string{"response 3", "stock 3"}, env.effects(t), "one movement, of the attempt that committed")
	assert.Equal(t, []int32{3}, afterRuns, "the AfterCommit of a rolled-back attempt is dropped")
	assert.Equal(t, 1, env.count(t, "status = 0 AND attempts = 3"))
	assert.Equal(t, int32(3), starts.ended.Load())
}

func TestInTx_TheLastFailedAttemptIsADeadLetterWithItsWritesOnly(t *testing.T) {
	env := testDB(t)
	starts := &txStarts{db: env.db}
	f := env.in.InTx("t", starts.begin, func(ctx context.Context, _ *Message) error {
		OnRow(ctx, effect("failure response"))
		if err := effectInTx(ctx, "stock"); err != nil {
			return err
		}

		return errors.New("deadlock")
	}, quickRetry)

	require.NoError(t, f(context.Background(), message("id-1", "v")))
	assert.Equal(t, []string{"failure response"}, env.effects(t), "the stock of no attempt is kept")
	assert.Equal(t, 1, env.count(t, "status = 2 AND attempts = 4"))
	assert.Equal(t, starts.begun.Load(), starts.ended.Load())
}

func TestInTx_APanicRollsBackAndIsADeadLetter(t *testing.T) {
	env := testDB(t)
	starts := &txStarts{db: env.db}
	var runs atomic.Int32
	f := env.in.InTx("t", starts.begin, func(ctx context.Context, _ *Message) error {
		runs.Add(1)
		require.NoError(t, effectInTx(ctx, "stock"))
		panic("boom")
	}, quickRetry)

	require.NoError(t, f(context.Background(), message("id-1", "v")))
	assert.Equal(t, int32(1), runs.Load(), "a panic is not tried again")
	assert.Empty(t, env.effects(t))
	assert.Zero(t, env.count(t, "status = 0"), "the claimed row is rolled back")
	assert.Equal(t, 1, env.count(t, "status = 2"))
	assert.False(t, starts.active.Load(), "the transaction is ended")
	assert.Equal(t, starts.begun.Load(), starts.ended.Load())
}

func TestInTx_ACutRunLeavesNothingAndTheMessageUnmarked(t *testing.T) {
	env := testDB(t)
	starts := &txStarts{db: env.db}
	ctx, cancel := context.WithCancel(context.Background())
	f := env.in.InTx("t", starts.begin, func(ctx context.Context, _ *Message) error {
		require.NoError(t, effectInTx(ctx, "stock"))
		cancel()

		return ctx.Err()
	}, quickRetry)

	require.Error(t, f(ctx, message("id-1", "v")))
	assert.Empty(t, env.effects(t))
	assert.Zero(t, env.count(t, "true"))
	assert.Equal(t, starts.begun.Load(), starts.ended.Load())
}

func TestInTx_AFailedCommitLeavesNothingAndTheMessageUnmarked(t *testing.T) {
	env := testDB(t)
	_, err := env.db.Exec(`CREATE FUNCTION refuse() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'refused at commit'; END $$;
		CREATE CONSTRAINT TRIGGER refuse AFTER INSERT ON effects DEFERRABLE INITIALLY DEFERRED
		FOR EACH ROW EXECUTE FUNCTION refuse()`)
	require.NoError(t, err)
	starts := &txStarts{db: env.db}
	var runs atomic.Int32
	f := env.in.InTx("t", starts.begin, func(ctx context.Context, _ *Message) error {
		runs.Add(1)

		return effectInTx(ctx, "stock")
	}, quickRetry)

	require.Error(t, f(context.Background(), message("id-1", "v")), "the consumer runs it again")
	assert.Equal(t, int32(1), runs.Load(), "a failed commit is not an attempt of the handler")
	assert.Zero(t, env.count(t, "true"))
	assert.Equal(t, starts.begun.Load(), starts.ended.Load())
}

func TestInTx_TwoDeliveriesAtOnceRunTheHandlerOnce(t *testing.T) {
	env := testDB(t)
	starts := &txStarts{db: env.db}
	var runs atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	f := env.in.InTx("t", starts.begin, func(ctx context.Context, _ *Message) error {
		if runs.Add(1) == 1 {
			close(entered)
			<-release
		}

		return effectInTx(ctx, "stock")
	}, quickRetry)

	first := make(chan error, 1)
	go func() { first <- f(context.Background(), message("id-1", "v")) }()
	<-entered
	second := make(chan error, 1)
	go func() { second <- f(context.Background(), message("id-1", "v")) }()

	select {
	case err := <-second:
		t.Fatalf("the second delivery ended (%v) while the first held the row", err)
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-first)
	require.NoError(t, <-second)
	assert.Equal(t, int32(1), runs.Load())
	assert.Equal(t, []string{"stock"}, env.effects(t), "one movement")
}

func TestAfterCommit_OutsideAnInTxMessageIsTheCallersToRun(t *testing.T) {
	ran := false
	assert.False(t, AfterCommit(context.Background(), func() { ran = true }))
	assert.False(t, ran)
	assert.False(t, AfterCommit(Detach(context.WithValue(context.Background(), afterKey{}, &afterCommits{})),
		func() {}), "work that outlives the message does not reach its AfterCommit")
}

func TestInTx_TheTxDurationEndsAtTheCommit(t *testing.T) {
	metrics := &countingMetrics{}
	env := testDB(t, func(o *Options) { o.Metrics = metrics })
	starts := &txStarts{db: env.db}
	const afterWork = 300 * time.Millisecond
	f := env.in.InTx("t", starts.begin, func(ctx context.Context, _ *Message) error {
		AfterCommit(ctx, func() { time.Sleep(afterWork) })

		return effectInTx(ctx, "stock")
	}, quickRetry)

	require.NoError(t, f(context.Background(), message("id-1", "v")))
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	require.Len(t, metrics.txTook, 1)
	assert.Less(t, metrics.txTook[0], afterWork.Seconds(), "the AfterCommit work is not the transaction's")
}

// holdSlot runs a message whose handler holds the only slot of LimitTx(1) until the returned release.
func holdSlot(t *testing.T, env *testEnv, starts *txStarts) (release func()) {
	t.Helper()
	entered := make(chan struct{})
	held := make(chan struct{})
	releaseHeld := sync.OnceFunc(func() { close(held) })
	t.Cleanup(releaseHeld) // a failed check must not leave the handler holding its transaction
	f := env.in.InTx("holder", starts.begin, func(context.Context, *Message) error {
		close(entered)
		<-held

		return nil
	}, quickRetry)
	done := make(chan error, 1)
	go func() { done <- f(context.Background(), message("holder", "v")) }()
	<-entered

	return func() {
		releaseHeld()
		require.NoError(t, <-done)
	}
}

func TestInTx_LimitTxLetsNoMoreTransactionsOpenAtOnce(t *testing.T) {
	metrics := &countingMetrics{}
	env := testDB(t, func(o *Options) { o.Metrics = metrics })
	env.in.LimitTx(1)
	starts := &txStarts{db: env.db}
	release := holdSlot(t, env, starts)
	ran := make(chan struct{}, 1)
	f := env.in.InTx("t", starts.begin, func(ctx context.Context, m *Message) error {
		ran <- struct{}{}

		return effectInTx(ctx, m.ID)
	}, quickRetry)

	done := make(chan error, 1)
	go func() { done <- f(context.Background(), message("id-1", "v")) }()
	select {
	case <-ran:
		t.Fatal("a second message ran while the only slot was taken")
	case <-time.After(200 * time.Millisecond):
	}
	assert.Equal(t, int32(1), starts.most.Load(), "no second transaction is begun while it waits")
	release()
	require.NoError(t, <-done)
	assert.Equal(t, int32(1), starts.most.Load())
	assert.Equal(t, []string{"id-1"}, env.effects(t))
	assert.Equal(t, 1, env.in.TxLimit())
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	require.Len(t, metrics.waits, 2)
	assert.GreaterOrEqual(t, max(metrics.waits[0], metrics.waits[1]), 0.2, "the wait for the slot is measured")
}

func TestInTx_EveryAttemptFreesItsSlot(t *testing.T) {
	env := testDB(t)
	env.in.LimitTx(1)
	starts := &txStarts{db: env.db}
	run := func(t *testing.T, consumer, id string, h func(ctx context.Context, m *Message) error) error {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		return env.in.InTx(consumer, starts.begin, h, quickRetry)(ctx, message(id, "v"))
	}
	for _, c := range []struct {
		name string
		h    func(ctx context.Context, m *Message) error
		err  bool
	}{
		{"failed attempts to a dead letter", func(context.Context, *Message) error {
			return errors.New("deadlock")
		}, false},
		{"a panic", func(context.Context, *Message) error { panic("boom") }, false},
		{"an aborted transaction", func(ctx context.Context, _ *Message) error {
			tx, _ := Tx(ctx)
			_, err := tx.ExecContext(ctx, `SELECT 1/0`)
			require.Error(t, err)

			return nil
		}, false},
		{"a duplicate", func(context.Context, *Message) error { return nil }, false},
		{"a begin that fails", nil, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			consumer := "t " + c.name
			if c.h == nil {
				starts.fail.Store(true)
				require.Error(t, run(t, consumer, "id-1", func(context.Context, *Message) error { return nil }))
			} else {
				require.NoError(t, run(t, consumer, "id-1", c.h))
				require.NoError(t, run(t, consumer, "id-1", c.h), "delivered again")
			}
			require.NoError(t, run(t, consumer+" next", "id-2", func(context.Context, *Message) error {
				return nil
			}), "the next message gets the slot")
		})
	}
}

func TestInTx_AReplayedDeadLetterWaitsForASlot(t *testing.T) {
	env := testDB(t)
	env.in.LimitTx(1)
	starts := &txStarts{db: env.db}
	release := holdSlot(t, env, starts)
	ran := make(chan struct{}, 1)
	replay := env.in.inOwnTx("t", starts.begin, func(ctx context.Context, _ *Message) error {
		ran <- struct{}{}

		return effectInTx(ctx, "replayed")
	})

	done := make(chan error, 1)
	go func() { done <- replay(context.Background(), message("id-1", "v")) }()
	select {
	case <-ran:
		t.Fatal("the replay ran while the only slot was taken")
	case <-time.After(200 * time.Millisecond):
	}
	assert.Equal(t, int32(1), starts.most.Load())
	release()
	require.NoError(t, <-done)
	assert.Equal(t, []string{"replayed"}, env.effects(t))
}

func TestInTx_AMessageWaitingForASlotStopsWithItsSession(t *testing.T) {
	env := testDB(t)
	env.in.LimitTx(1)
	starts := &txStarts{db: env.db}
	release := holdSlot(t, env, starts)
	defer release()
	ran := false
	f := env.in.InTx("t", starts.begin, func(context.Context, *Message) error {
		ran = true

		return nil
	}, quickRetry)

	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- f(withStop(context.Background(), stop), message("id-1", "v")) }()
	time.Sleep(50 * time.Millisecond)
	close(stop)
	select {
	case err := <-done:
		require.ErrorIs(t, err, errStopped, "left unmarked for the partition's next owner")
	case <-time.After(time.Second):
		t.Fatal("the message went on waiting for a slot after its session ended")
	}
	assert.False(t, ran)
	assert.Zero(t, env.count(t, "consumer = 't'"))
}

func TestInTx_AReplayedDeadLetterThatPanicsIsRolledBack(t *testing.T) {
	env := testDB(t)
	var tx *sql.Tx
	begin := func(ctx context.Context) (*sql.Tx, func(), error) {
		var err error
		tx, err = env.db.BeginTx(ctx, nil)

		return tx, nil, err
	}
	run := env.in.inOwnTx("t", begin, func(ctx context.Context, _ *Message) error {
		require.NoError(t, effectInTx(ctx, "stock"))
		panic("boom")
	})

	assert.Panics(t, func() { _ = run(context.Background(), message("id-1", "v")) })
	require.ErrorIs(t, tx.Rollback(), sql.ErrTxDone, "the panic rolled the transaction back")
	assert.Empty(t, env.effects(t))
}

func TestInTx_AStatementThatAbortedTheTransactionIsDoneWithTheFailureWritesOnce(t *testing.T) {
	for name, answer := range map[string]func(ctx context.Context){
		"a failure answer": func(ctx context.Context) { OnRow(ctx, effect("failure response")) },
		"a success answer": func(ctx context.Context) {
			OnRow(ctx, effect("success response"))
			OnFailedRow(ctx, effect("failure response"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			env := testDB(t)
			starts := &txStarts{db: env.db}
			var runs atomic.Int32
			afterRan := false
			f := env.in.InTx("t", starts.begin, func(ctx context.Context, _ *Message) error {
				runs.Add(1)
				require.NoError(t, effectInTx(ctx, "stock"))
				AfterCommit(ctx, func() { afterRan = true })
				tx, _ := Tx(ctx)
				_, err := tx.ExecContext(ctx, `SELECT 1/0`)
				require.Error(t, err)
				answer(ctx)

				return nil
			}, quickRetry)

			require.NoError(t, f(context.Background(), message("id-1", "v")))
			assert.Equal(t, int32(1), runs.Load(), "the statement would fail again")
			assert.Equal(t, []string{"failure response"}, env.effects(t), "the handler's work is rolled back")
			assert.Equal(t, 1, env.count(t, "status = 0 AND attempts = 1"))
			assert.False(t, afterRan, "the AfterCommit of the rolled-back work is dropped")
			assert.Equal(t, starts.begun.Load(), starts.ended.Load())
		})
	}
}

func TestInTx_AnAbortedHandlerIsDoneWithTheFailureWritesOnce(t *testing.T) {
	for name, answer := range map[string]func(ctx context.Context){
		"a failure answer": func(ctx context.Context) { OnRow(ctx, effect("failure response")) },
		"a success answer": func(ctx context.Context) {
			OnRow(ctx, effect("success response"))
			OnFailedRow(ctx, effect("failure response"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			env := testDB(t)
			starts := &txStarts{db: env.db}
			var runs atomic.Int32
			afterRan := false
			f := env.in.InTx("t", starts.begin, func(ctx context.Context, _ *Message) error {
				runs.Add(1)
				require.NoError(t, effectInTx(ctx, "stock"))
				AfterCommit(ctx, func() { afterRan = true })
				answer(ctx)

				return Abort(errors.New("a statement was refused"))
			}, quickRetry)

			require.NoError(t, f(context.Background(), message("id-1", "v")))
			assert.Equal(t, int32(1), runs.Load(), "not run again")
			assert.Equal(t, []string{"failure response"}, env.effects(t), "the handler's work is rolled back")
			assert.Equal(t, 1, env.count(t, "status = 0 AND attempts = 1"))
			assert.False(t, afterRan)
			assert.Equal(t, starts.begun.Load(), starts.ended.Load())
		})
	}
}

func TestInTx_TheDeadLetterHasTheFailureWritesOfASuccessAnswer(t *testing.T) {
	env := testDB(t)
	starts := &txStarts{db: env.db}
	f := env.in.InTx("t", starts.begin, func(ctx context.Context, _ *Message) error {
		require.NoError(t, effectInTx(ctx, "stock"))
		OnRow(ctx, effect("success response"))
		OnFailedRow(ctx, effect("failure response"))

		return errors.New("deadlock")
	}, quickRetry)

	require.NoError(t, f(context.Background(), message("id-1", "v")))
	assert.Equal(t, []string{"failure response"}, env.effects(t))
	assert.Equal(t, 1, env.count(t, "status = 2 AND attempts = 4"))
}

func TestInTx_TheHandlerGetsItsTransactionAsTxAndNothingElse(t *testing.T) {
	env := testDB(t)
	var begun *sql.Tx
	begin := func(ctx context.Context) (*sql.Tx, func(), error) {
		var err error
		begun, err = env.db.BeginTx(ctx, nil)

		return begun, nil, err
	}
	var detachedHasTx bool
	f := env.in.InTx("t", begin, func(ctx context.Context, _ *Message) error {
		tx, ok := Tx(ctx)
		require.True(t, ok)
		assert.Same(t, begun, tx, "the transaction TxStart began")
		_, err := env.db.ExecContext(ctx, `INSERT INTO effects VALUES ('on the pool')`)
		require.NoError(t, err)
		_, detachedHasTx = Tx(Detach(ctx))

		return effectInTx(ctx, "in the transaction")
	}, quickRetry)

	require.NoError(t, f(context.Background(), message("id-1", "v")))
	assert.Equal(t, []string{"in the transaction", "on the pool"}, env.effects(t))
	assert.False(t, detachedHasTx, "work that outlives the message does not get its transaction")
	_, ok := Tx(context.Background())
	assert.False(t, ok)

	f = env.in.InTx("t2", begin, func(ctx context.Context, _ *Message) error {
		_, err := env.db.ExecContext(ctx, `INSERT INTO effects VALUES ('committed on its own')`)
		require.NoError(t, err)
		require.NoError(t, effectInTx(ctx, "rolled back"))

		return Permanent(errors.New("refused"))
	}, quickRetry)
	require.NoError(t, f(context.Background(), message("id-2", "v")))
	assert.Equal(t, []string{"committed on its own", "in the transaction", "on the pool"}, env.effects(t),
		"the inbox swaps no pool: a statement on it is not part of the message")
}
