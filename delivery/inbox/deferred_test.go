package inbox //nolint:testpackage // reads the rows of the worker

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func companyKey(m *Message) string {
	v, _ := m.Header("ce_companyid")

	return string(v)
}

func queued(id, company string) *Message {
	m := message(id, "payload of "+id)
	m.Headers = append(m.Headers, Header{Key: "ce_companyid", Value: []byte(company)})

	return m
}

// worker runs the heartbeats on the inbox's pool unless opts give another.
func worker(t testing.TB, in *Inbox, opts WorkerOptions) *Worker {
	t.Helper()
	if opts.HeartbeatDB == nil {
		opts.HeartbeatDB = in.db
	}
	w, err := in.Worker(opts)
	require.NoError(t, err)

	return w
}

// fastWorker polls every 10 ms with a lease of lease.
func fastWorker(t testing.TB, in *Inbox, lease time.Duration) *Worker {
	t.Helper()

	return worker(t, in, WorkerOptions{Poll: 10 * time.Millisecond, Lease: lease, Heartbeat: lease / 3})
}

func startWorker(ctx context.Context, t *testing.T, w *Worker) {
	t.Helper()
	w.Start(ctx)
	t.Cleanup(func() { _ = w.Stop(context.Background()) })
}

// secondPod is another inbox on the same table and lock, as a second pod has.
func (e *testEnv) secondPod(t *testing.T) *Inbox {
	t.Helper()
	in, err := New(e.db, e.in.opts)
	require.NoError(t, err)

	return in
}

func TestDefer_QueuesOnceAndTheWorkerRunsIt(t *testing.T) {
	env := testDB(t)
	var runs atomic.Int32
	var got *Message
	f := env.in.Defer("finish", companyKey, func(ctx context.Context, m *Message) error {
		runs.Add(1)
		got = m
		OnRow(ctx, effect("response"))

		return nil
	}, RetryPolicy{})

	require.NoError(t, f(context.Background(), queued("id-1", "c1")))
	require.NoError(t, f(context.Background(), queued("id-1", "c1")))
	assert.Equal(t, 1, env.count(t, "status = 1 AND key = 'c1' AND payload IS NOT NULL"))

	startWorker(context.Background(), t, fastWorker(t, env.in, time.Minute))
	require.Eventually(t, func() bool { return env.count(t, "status = 0") == 1 }, 2*time.Second, 5*time.Millisecond)
	assert.Equal(t, int32(1), runs.Load())
	assert.Equal(t, "payload of id-1", string(got.Value))
	company, _ := got.Header("ce_companyid")
	assert.Equal(t, "c1", string(company))
	assert.Equal(t, 1, env.count(t, "status = 0 AND payload IS NULL AND headers IS NULL AND lease_until IS NULL"),
		"a done row keeps no payload")
	assert.Equal(t, []string{"response"}, env.effects(t))
}

func TestWorker_RunsOneMessageOfACompanyAtATimeAcrossThePods(t *testing.T) {
	env := testDB(t)
	var (
		mu      sync.Mutex
		running = map[string]int{}
		most    = map[string]int{}
		order   []string
	)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	handler := func(_ context.Context, m *Message) error {
		company := companyKey(m)
		mu.Lock()
		running[company]++
		most[company] = max(most[company], running[company])
		order = append(order, m.ID)
		mu.Unlock()
		if company == "a" {
			<-release
		}
		mu.Lock()
		running[company]--
		mu.Unlock()

		return nil
	}
	pods := []*Inbox{env.in, env.secondPod(t)}
	fs := make([]Func, 0, len(pods))
	for _, in := range pods {
		fs = append(fs, in.Defer("finish", companyKey, handler, RetryPolicy{}))
	}
	ctx := context.Background()
	require.NoError(t, fs[0](ctx, queued("a-1", "a")))
	require.NoError(t, fs[1](ctx, queued("a-2", "a")))
	require.NoError(t, fs[0](ctx, queued("b-1", "b")))

	for _, in := range pods {
		startWorker(ctx, t, fastWorker(t, in, time.Minute))
	}
	t.Cleanup(releaseAll) // before the workers' Stop, which waits for the handlers
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()

		return len(order) >= 2
	}, 2*time.Second, 5*time.Millisecond, "a-1 and b-1 run together")
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	assert.ElementsMatch(t, []string{"a-1", "b-1"}, order, "a-2 waits for a-1")
	mu.Unlock()

	releaseAll()
	require.Eventually(t, func() bool { return env.count(t, "status = 0") == 3 }, 2*time.Second, 5*time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, most["a"])
	assert.Equal(t, "a-2", order[2])
}

func TestWorker_ALostPodsMessageRunsAgainAfterItsLease(t *testing.T) {
	env := testDB(t)
	var runs atomic.Int32
	handler := func(ctx context.Context, _ *Message) error {
		if runs.Add(1) == 1 {
			<-ctx.Done()

			return ctx.Err()
		}

		return nil
	}
	lost, other := env.in, env.secondPod(t)
	f := lost.Defer("finish", companyKey, handler, RetryPolicy{})
	other.Defer("finish", companyKey, handler, RetryPolicy{})
	require.NoError(t, f(context.Background(), queued("id-1", "c1")))

	lostCtx, loseThePod := context.WithCancel(context.Background())
	startWorker(lostCtx, t, fastWorker(t, lost, 300*time.Millisecond))
	require.Eventually(t, func() bool { return runs.Load() == 1 }, 2*time.Second, 5*time.Millisecond)
	loseThePod()
	assert.Equal(t, 1, env.count(t, "status = 1 AND lease_until > now()"), "in progress, under its lease")

	start := time.Now()
	startWorker(context.Background(), t, fastWorker(t, other, 300*time.Millisecond))
	require.Eventually(t, func() bool { return env.count(t, "status = 0") == 1 }, 3*time.Second, 5*time.Millisecond)
	assert.Equal(t, int32(2), runs.Load())
	assert.Greater(t, time.Since(start), 100*time.Millisecond, "taken over once the lease ended")
	assert.Equal(t, 1, env.count(t, "status = 0 AND attempts = 2"))
}

func TestWorker_TheHeartbeatKeepsALongMessage(t *testing.T) {
	env := testDB(t)
	var runs atomic.Int32
	handler := func(context.Context, *Message) error {
		runs.Add(1)
		time.Sleep(time.Second)

		return nil
	}
	f := env.in.Defer("finish", companyKey, handler, RetryPolicy{})
	second := env.secondPod(t)
	second.Defer("finish", companyKey, handler, RetryPolicy{})
	require.NoError(t, f(context.Background(), queued("id-1", "c1")))

	startWorker(context.Background(), t, fastWorker(t, env.in, 300*time.Millisecond))
	require.Eventually(t, func() bool { return runs.Load() == 1 }, 2*time.Second, 5*time.Millisecond)
	startWorker(context.Background(), t, fastWorker(t, second, 300*time.Millisecond))
	require.Eventually(t, func() bool { return env.count(t, "status = 0") == 1 }, 3*time.Second, 5*time.Millisecond)
	assert.Equal(t, int32(1), runs.Load(), "a message running past its first lease is not run twice")
}

func TestWorker_RetriesByThePolicyThenADeadLetter(t *testing.T) {
	env := testDB(t)
	var (
		mu    sync.Mutex
		times []time.Time
	)
	f := env.in.Defer("finish", companyKey, func(ctx context.Context, _ *Message) error {
		mu.Lock()
		times = append(times, time.Now())
		n := len(times)
		mu.Unlock()
		OnRow(ctx, effect("failure response "+string(rune('0'+n))))

		return errors.New("timeout")
	}, RetryPolicy{Attempts: 2, Pauses: []time.Duration{200 * time.Millisecond}})
	require.NoError(t, f(context.Background(), queued("id-1", "c1")))

	startWorker(context.Background(), t, fastWorker(t, env.in, time.Minute))
	require.Eventually(t, func() bool { return env.count(t, "status = 2") == 1 }, 3*time.Second, 5*time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, times, 2)
	assert.GreaterOrEqual(t, times[1].Sub(times[0]), 200*time.Millisecond)
	assert.Equal(t, 1, env.count(t, "status = 2 AND attempts = 2 AND error = 'timeout' AND payload IS NOT NULL"))
	assert.Equal(t, []string{"failure response 2"}, env.effects(t))
}

func TestReplay_RunsADeadLetterAgain(t *testing.T) {
	env := testDB(t)
	var fail atomic.Bool
	fail.Store(true)
	f := env.in.After("t", func(context.Context, *Message) error {
		if fail.Load() {
			return errors.New("timeout")
		}

		return nil
	}, RetryPolicy{})
	require.NoError(t, f(context.Background(), message("id-1", "v")))
	require.Equal(t, 1, env.count(t, "status = 2"))

	n, err := env.in.Replay(context.Background(), "t", "id-2")
	require.NoError(t, err)
	assert.Zero(t, n)

	fail.Store(false)
	n, err = env.in.Replay(context.Background(), "t", "id-1")
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
	startWorker(context.Background(), t, fastWorker(t, env.in, time.Minute))
	require.Eventually(t, func() bool { return env.count(t, "status = 0") == 1 }, 2*time.Second, 5*time.Millisecond)
	require.NoError(t, f(context.Background(), message("id-1", "v")), "delivered again: still a duplicate")
}

func TestWorker_StopWaitsForTheRunningMessages(t *testing.T) {
	env := testDB(t)
	release := make(chan struct{})
	started := make(chan struct{}, 2)
	f := env.in.Defer("finish", companyKey, func(context.Context, *Message) error {
		started <- struct{}{}
		<-release

		return nil
	}, RetryPolicy{})
	require.NoError(t, f(context.Background(), queued("id-1", "c1")))

	w := fastWorker(t, env.in, time.Minute)
	w.Start(context.Background())
	<-started
	short, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	require.Error(t, w.Stop(short))
	require.NoError(t, f(context.Background(), queued("id-2", "c2")))

	close(release)
	require.NoError(t, w.Stop(context.Background()))
	assert.Equal(t, 1, env.count(t, "status = 0"), "the running message is recorded")
	assert.Equal(t, 1, env.count(t, "status = 1"), "no message is claimed after Stop")
}

func TestWorker_ReportsTheAgeOfTheQueuedRows(t *testing.T) {
	metrics := &countingMetrics{}
	env := testDB(t, func(o *Options) { o.Metrics = metrics })
	env.in.Defer("finish", companyKey, func(context.Context, *Message) error { return nil }, RetryPolicy{})
	env.in.Defer("other", companyKey, func(context.Context, *Message) error { return nil }, RetryPolicy{})
	_, err := env.db.Exec(`INSERT INTO processed_events (consumer, event_id, digest, status, topic, kpartition,
		koffset, received_at, lease_until) VALUES ('finish', 'e', '\x00', 1, 't', 0, 0, now() - interval '90 seconds',
		now() + interval '1 hour')`)
	require.NoError(t, err)

	startWorker(context.Background(), t, worker(t, env.in, WorkerOptions{Poll: time.Hour}))
	require.Eventually(t, func() bool {
		metrics.mu.Lock()
		defer metrics.mu.Unlock()

		return len(metrics.ages) == 2
	}, 2*time.Second, 5*time.Millisecond)
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	assert.InDelta(t, 90, metrics.ages["finish"], 5)
	assert.Zero(t, metrics.ages["other"])
}

func TestWorker_ARunThatLostItsLeaseRecordsNothing(t *testing.T) {
	env := testDB(t)
	f := env.in.Defer("finish", companyKey, func(context.Context, *Message) error { return nil }, RetryPolicy{})
	other := env.secondPod(t)
	other.Defer("finish", companyKey, func(context.Context, *Message) error { return nil }, RetryPolicy{})
	require.NoError(t, f(context.Background(), queued("id-1", "c1")))

	stale, err := env.in.claim(context.Background(), 1, 50*time.Millisecond)
	require.NoError(t, err)
	require.Len(t, stale, 1)
	time.Sleep(100 * time.Millisecond)
	current, err := other.claim(context.Background(), 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, current, 1, "taken over once the lease ended")

	worker(t, env.in, WorkerOptions{}).finish(context.Background(), stale[0], env.in.q.finishDone, resultDone,
		&rowWrites{fns: []func(context.Context, *sql.Tx) error{effect("stale response")}})
	assert.Empty(t, env.effects(t))
	assert.Equal(t, 1, env.count(t, "status = 1 AND attempts = 2 AND lease_until > now()"))
}

func TestDefer_ADatabaseErrorLeavesTheMessageUnmarked(t *testing.T) {
	env := testDB(t)
	closed, err := sql.Open("postgres", env.url)
	require.NoError(t, err)
	require.NoError(t, closed.Close())
	in, err := New(closed, env.in.opts)
	require.NoError(t, err)
	f := in.Defer("finish", companyKey, func(context.Context, *Message) error { return nil }, RetryPolicy{})

	stop := make(chan struct{})
	time.AfterFunc(300*time.Millisecond, func() { close(stop) })
	require.ErrorIs(t, f(withStop(context.Background(), stop), queued("id-1", "c1")), errStopped)
	assert.Equal(t, 0, env.count(t, "true"))
}

func TestDefer_AKeyTheTableCannotHoldIsLeftOut(t *testing.T) {
	env := testDB(t)
	f := env.in.Defer("finish", companyKey, func(context.Context, *Message) error { return nil }, RetryPolicy{})
	require.NoError(t, f(context.Background(), queued("id-1", "c\x001")))
	assert.Equal(t, 1, env.count(t, "status = 1 AND key IS NULL"))
}

func TestWorker_ARunWhoseLeaseIsTakenOverIsCut(t *testing.T) {
	env := testDB(t)
	cut := make(chan error, 1)
	started := make(chan struct{})
	var once sync.Once
	f := env.in.Defer("finish", companyKey, func(ctx context.Context, _ *Message) error {
		once.Do(func() { close(started) })
		var err error
		select {
		case <-ctx.Done():
			err = ctx.Err()
		case <-time.After(1500 * time.Millisecond):
		}
		select {
		case cut <- err:
		default: // a later run, once the lease ended
		}

		return nil
	}, RetryPolicy{})
	require.NoError(t, f(context.Background(), queued("id-1", "c1")))
	startWorker(context.Background(), t, worker(t, env.in, WorkerOptions{Poll: 10 * time.Millisecond,
		Lease: 300 * time.Millisecond, Heartbeat: 50 * time.Millisecond}))
	<-started
	_, err := env.db.Exec(`UPDATE processed_events SET attempts = attempts + 1`)
	require.NoError(t, err)

	require.ErrorIs(t, <-cut, context.Canceled)
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 1, env.count(t, "status = 1"), "the run that lost its lease records nothing")
}

// beatlessDB fails the heartbeats, as a saturated pool or a failover would.
type beatlessDB struct {
	*sql.DB
	heartbeat string
}

func (d beatlessDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if query == d.heartbeat {
		return nil, errors.New("connection refused")
	}

	return d.DB.ExecContext(ctx, query, args...)
}

func TestWorker_ARunThatCannotExtendItsLeaseIsCutBeforeItEnds(t *testing.T) {
	env := testDB(t)
	in, err := New(beatlessDB{DB: env.db, heartbeat: env.in.q.heartbeat}, env.in.opts)
	require.NoError(t, err)
	cutAfter := make(chan time.Duration, 1)
	f := in.Defer("finish", companyKey, func(ctx context.Context, _ *Message) error {
		start := time.Now()
		after := time.Duration(-1)
		select {
		case <-ctx.Done():
			after = time.Since(start)
		case <-time.After(2 * time.Second):
		}
		select {
		case cutAfter <- after:
		default: // a later run, once the lease ended
		}

		return nil
	}, RetryPolicy{})
	require.NoError(t, f(context.Background(), queued("id-1", "c1")))
	startWorker(context.Background(), t, worker(t, in, WorkerOptions{Poll: 10 * time.Millisecond,
		Lease: 400 * time.Millisecond, Heartbeat: 100 * time.Millisecond}))

	var after time.Duration
	select {
	case after = <-cutAfter:
	case <-time.After(5 * time.Second):
		t.Fatal("the run did not end")
	}
	assert.Positive(t, after, "the run was cut")
	assert.Less(t, after, 400*time.Millisecond, "before its lease could end")
}

// A queued row without a key (an import without companyid) and a live lease: a second pod must not run it.
func TestWorker_ARowWithoutAKeyIsNotClaimedTwice(t *testing.T) {
	env := testDB(t)
	var runs atomic.Int32
	release := make(chan struct{})
	handler := func(context.Context, *Message) error {
		runs.Add(1)
		<-release

		return nil
	}
	noKey := func(*Message) string { return "" }
	pods := []*Inbox{env.in, env.secondPod(t)}
	f := pods[0].Defer("finish", noKey, handler, RetryPolicy{})
	pods[1].Defer("finish", noKey, handler, RetryPolicy{})
	require.NoError(t, f(context.Background(), queued("nokey", "")))
	for _, in := range pods {
		startWorker(context.Background(), t, fastWorker(t, in, time.Minute))
	}
	t.Cleanup(func() { close(release) })
	require.Eventually(t, func() bool { return runs.Load() >= 1 }, 2*time.Second, 5*time.Millisecond)
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, int32(1), runs.Load())
	assert.Equal(t, 1, env.count(t, "status = 1 AND key IS NULL AND attempts = 1"))
}

func TestReplay_GivesTheMessageItsAttemptsAgain(t *testing.T) {
	env := testDB(t)
	var runs atomic.Int32
	f := env.in.Defer("finish", companyKey, func(context.Context, *Message) error {
		if runs.Add(1) == 3 {
			return nil
		}

		return errors.New("timeout")
	}, RetryPolicy{Attempts: 2, Pauses: []time.Duration{10 * time.Millisecond}})
	require.NoError(t, f(context.Background(), queued("id-1", "c1")))
	startWorker(context.Background(), t, fastWorker(t, env.in, time.Minute))
	require.Eventually(t, func() bool { return env.count(t, "status = 2 AND attempts = 2") == 1 }, 2*time.Second,
		5*time.Millisecond)

	n, err := env.in.Replay(context.Background(), "finish", "id-1")
	require.NoError(t, err)
	require.Equal(t, int64(1), n)
	require.Eventually(t, func() bool { return env.count(t, "status = 0") == 1 }, 2*time.Second,
		5*time.Millisecond, "the replay runs again with a full policy")
	assert.Equal(t, int32(3), runs.Load())
	assert.Equal(t, 1, env.count(t, "status = 0 AND attempts = 1"))
}

func TestWorker_APanicInARowWriteIsADeadLetterWithoutIt(t *testing.T) {
	env := testDB(t)
	var runs atomic.Int32
	f := env.in.Defer("finish", companyKey, func(ctx context.Context, _ *Message) error {
		runs.Add(1)
		OnRow(ctx, func(context.Context, *sql.Tx) error { panic("encode the response") })

		return nil
	}, RetryPolicy{})
	require.NoError(t, f(context.Background(), queued("id-1", "c1")))
	startWorker(context.Background(), t, fastWorker(t, env.in, time.Minute))
	require.Eventually(t, func() bool { return env.count(t, "status = 2") == 1 }, 2*time.Second, 5*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, int32(1), runs.Load())
	assert.Equal(t, 1, env.count(t, "status = 2 AND error = 'panic in a row write: encode the response'"))
}

func TestWorker_ChecksItsOptions(t *testing.T) {
	env := testDB(t)
	_, err := env.in.Worker(WorkerOptions{})
	require.ErrorContains(t, err, "HeartbeatDB is required")
	_, err = env.in.Worker(WorkerOptions{HeartbeatDB: (*sql.DB)(nil)})
	require.ErrorContains(t, err, "HeartbeatDB is required")
	_, err = env.in.Worker(WorkerOptions{Lease: 3 * time.Second, Heartbeat: 1001 * time.Millisecond,
		HeartbeatDB: env.db})
	require.Error(t, err)
	w := worker(t, env.in, WorkerOptions{Lease: 3 * time.Second, Heartbeat: time.Second})
	assert.Equal(t, time.Second, w.opts.Heartbeat)
	w = worker(t, env.in, WorkerOptions{})
	assert.Equal(t, defaultLease/3, w.opts.Heartbeat)
}

// The handler holds the only connection of the main pool, as long transactions of a busy pod do.
func TestWorker_AStarvedPoolDoesNotCutARunWhoseHeartbeatsHaveTheirOwnPool(t *testing.T) {
	env := testDB(t)
	small, err := sql.Open("postgres", env.url)
	require.NoError(t, err)
	t.Cleanup(func() { _ = small.Close() })
	small.SetMaxOpenConns(1)
	in, err := New(small, env.in.opts)
	require.NoError(t, err)
	cut := make(chan error, 1)
	var runs atomic.Int32
	f := in.Defer("finish", companyKey, func(ctx context.Context, _ *Message) error {
		runs.Add(1)
		tx, txErr := small.BeginTx(context.Background(), nil)
		if txErr != nil {
			return txErr
		}
		defer func() { _ = tx.Rollback() }()
		select {
		case <-ctx.Done():
			cut <- ctx.Err()
		case <-time.After(2 * time.Second):
			cut <- nil
		}

		return nil
	}, RetryPolicy{})
	require.NoError(t, f(context.Background(), queued("id-1", "c1")))
	startWorker(context.Background(), t, worker(t, in, WorkerOptions{Poll: 10 * time.Millisecond,
		Lease: 900 * time.Millisecond, Heartbeat: 300 * time.Millisecond, HeartbeatDB: env.db}))

	require.NoError(t, <-cut)
	require.Eventually(t, func() bool { return env.count(t, "status = 0") == 1 }, 3*time.Second, 10*time.Millisecond)
	assert.Equal(t, int32(1), runs.Load())
}

// failingBeatDB fails one heartbeat, the fail-th.
type failingBeatDB struct {
	*sql.DB
	heartbeat string
	fail      int32
	beats     atomic.Int32
}

func (d *failingBeatDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if query == d.heartbeat && d.beats.Add(1) == d.fail {
		return nil, errors.New("connection refused")
	}

	return d.DB.ExecContext(ctx, query, args...)
}

func TestWorker_OneFailedHeartbeatAfterGoodOnesDoesNotCutTheRun(t *testing.T) {
	env := testDB(t)
	cut := make(chan error, 1)
	f := env.in.Defer("finish", companyKey, func(ctx context.Context, _ *Message) error {
		select {
		case <-ctx.Done():
			cut <- ctx.Err()
		case <-time.After(1300 * time.Millisecond):
			cut <- nil
		}

		return nil
	}, RetryPolicy{})
	require.NoError(t, f(context.Background(), queued("id-1", "c1")))
	beats := &failingBeatDB{DB: env.db, heartbeat: env.in.q.heartbeat, fail: 3}
	startWorker(context.Background(), t, worker(t, env.in, WorkerOptions{Poll: 10 * time.Millisecond,
		Lease: 600 * time.Millisecond, Heartbeat: 200 * time.Millisecond, HeartbeatDB: beats}))

	require.NoError(t, <-cut)
	assert.GreaterOrEqual(t, beats.beats.Load(), int32(4))
	require.Eventually(t, func() bool { return env.count(t, "status = 0") == 1 }, 2*time.Second, 10*time.Millisecond)
}

func TestWorker_ARunCutForItsLeaseRecordsNothing(t *testing.T) {
	env := testDB(t)
	cut := make(chan struct{})
	var runs atomic.Int32
	f := env.in.Defer("finish", companyKey, func(ctx context.Context, _ *Message) error {
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
			return errors.New("not cut")
		}
		if runs.Add(1) == 1 {
			close(cut)
		}

		return ctx.Err()
	}, RetryPolicy{})
	require.NoError(t, f(context.Background(), queued("id-1", "c1")))
	startWorker(context.Background(), t, worker(t, env.in, WorkerOptions{Poll: 10 * time.Millisecond,
		Lease: 1200 * time.Millisecond, Heartbeat: 400 * time.Millisecond,
		HeartbeatDB: beatlessDB{DB: env.db, heartbeat: env.in.q.heartbeat}}))

	select {
	case <-cut:
	case <-time.After(5 * time.Second):
		t.Fatal("the run was not cut")
	}
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 1, env.count(t, "status = 1 AND attempts = 1 AND error IS NULL"))
}

// hangingBeatDB holds every heartbeat until its context ends, as a lock or a starved pool does.
type hangingBeatDB struct {
	*sql.DB
	heartbeat string
}

func (d hangingBeatDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if query == d.heartbeat {
		<-ctx.Done()

		return nil, ctx.Err()
	}

	return d.DB.ExecContext(ctx, query, args...)
}

func TestWorker_HangingHeartbeatsCutTheRunBeforeItsLeaseEnds(t *testing.T) {
	env := testDB(t)
	leaseLeft := make(chan time.Duration, 1)
	var runs atomic.Int32
	f := env.in.Defer("finish", companyKey, func(ctx context.Context, _ *Message) error {
		if runs.Add(1) > 1 {
			return nil
		}
		select {
		case <-ctx.Done():
			var left float64
			if err := env.db.QueryRow(`SELECT extract(epoch FROM lease_until - clock_timestamp())
				FROM processed_events`).Scan(&left); err != nil {
				t.Error(err)
			}
			leaseLeft <- time.Duration(left * float64(time.Second))
		case <-time.After(5 * time.Second):
			leaseLeft <- 0
		}

		return nil
	}, RetryPolicy{})
	require.NoError(t, f(context.Background(), queued("id-1", "c1")))
	startWorker(context.Background(), t, worker(t, env.in, WorkerOptions{Poll: 10 * time.Millisecond,
		Lease: 1200 * time.Millisecond, Heartbeat: 400 * time.Millisecond,
		HeartbeatDB: hangingBeatDB{DB: env.db, heartbeat: env.in.q.heartbeat}}))

	assert.Positive(t, <-leaseLeft, "cut before the lease ended")
	assert.Equal(t, int32(1), runs.Load())
}

// slowThenHangingBeatDB: the first heartbeat goes through but comes back late, as one that waited on a row lock
// after its now(); every later one hangs to its deadline.
type slowThenHangingBeatDB struct {
	*sql.DB
	heartbeat string
	slow      time.Duration
	beats     atomic.Int32
}

func (d *slowThenHangingBeatDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if query != d.heartbeat {
		return d.DB.ExecContext(ctx, query, args...)
	}
	if d.beats.Add(1) == 1 {
		res, err := d.DB.ExecContext(ctx, query, args...)
		time.Sleep(d.slow)

		return res, err
	}
	<-ctx.Done()

	return nil, ctx.Err()
}

func TestWorker_ASlowHeartbeatThatWentThroughDoesNotDelayTheCutPastTheLease(t *testing.T) {
	env := testDB(t)
	leaseLeft := make(chan time.Duration, 1)
	var runs atomic.Int32
	f := env.in.Defer("finish", companyKey, func(ctx context.Context, _ *Message) error {
		if runs.Add(1) > 1 {
			return nil
		}
		select {
		case <-ctx.Done():
			var left float64
			if err := env.db.QueryRow(`SELECT extract(epoch FROM lease_until - clock_timestamp())
				FROM processed_events`).Scan(&left); err != nil {
				t.Error(err)
			}
			leaseLeft <- time.Duration(left * float64(time.Second))
		case <-time.After(5 * time.Second):
			leaseLeft <- -time.Hour
		}

		return nil
	}, RetryPolicy{})
	require.NoError(t, f(context.Background(), queued("id-1", "c1")))
	beats := &slowThenHangingBeatDB{DB: env.db, heartbeat: env.in.q.heartbeat, slow: 180 * time.Millisecond}
	startWorker(context.Background(), t, worker(t, env.in, WorkerOptions{Poll: 10 * time.Millisecond,
		Lease: 1200 * time.Millisecond, Heartbeat: 400 * time.Millisecond, HeartbeatDB: beats}))

	assert.Greater(t, <-leaseLeft, 100*time.Millisecond, "about half a heartbeat of the lease is left at the cut")
}
