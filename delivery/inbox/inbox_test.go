package inbox //nolint:testpackage // tests the row writes and the retention against a real PostgreSQL

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math/rand/v2"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IBM/sarama"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testLog = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 4}))

type testEnv struct {
	db  *sql.DB
	in  *Inbox
	url string
}

// testDB needs a PostgreSQL URL in INBOX_TEST_POSTGRES_DSN. Without one it skips, except in CI.
func testDB(t testing.TB, opts ...func(*Options)) *testEnv {
	t.Helper()

	url := os.Getenv("INBOX_TEST_POSTGRES_DSN")
	if url == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("no PostgreSQL in CI: INBOX_TEST_POSTGRES_DSN is not set")
		}
		t.Skip("no PostgreSQL: set INBOX_TEST_POSTGRES_DSN")
	}

	admin, err := sql.Open("postgres", url)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })
	schema := "inbox_test_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	_, err = admin.Exec("CREATE SCHEMA " + schema)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = admin.Exec("DROP SCHEMA " + schema + " CASCADE") })

	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	env := &testEnv{url: url + sep + "search_path=" + schema}
	env.db, err = sql.Open("postgres", env.url)
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.db.Close() })

	o := Options{Service: "inbox_test", Logger: testLog, LockKey: int64(len(schema))<<32 | int64(uuid.New().ID())}
	for _, opt := range opts {
		opt(&o)
	}
	up, _, err := Migration(o)
	require.NoError(t, err)
	_, err = env.db.Exec(up + `; CREATE TABLE effects (note TEXT NOT NULL)`)
	require.NoError(t, err)

	env.in, err = New(env.db, o)
	require.NoError(t, err)

	return env
}

func (e *testEnv) count(t testing.TB, where string, args ...any) int {
	t.Helper()

	var n int
	require.NoError(t, e.db.QueryRow(`SELECT count(*) FROM processed_events WHERE `+where, args...).Scan(&n))

	return n
}

func (e *testEnv) effects(t testing.TB) []string {
	t.Helper()

	rows, err := e.db.Query(`SELECT note FROM effects ORDER BY note`)
	require.NoError(t, err)
	defer rows.Close()
	var notes []string
	for rows.Next() {
		var note string
		require.NoError(t, rows.Scan(&note))
		notes = append(notes, note)
	}
	require.NoError(t, rows.Err())

	return notes
}

func effect(note string) func(ctx context.Context, tx *sql.Tx) error {
	return func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO effects (note) VALUES ($1)`, note)

		return err
	}
}

func message(id, value string) *Message {
	return &Message{Topic: "t", Partition: 1, Offset: 7, Key: []byte("company"), Value: []byte(value), ID: id,
		Headers: []Header{{Key: "ce_id", Value: []byte(id)}}}
}

// quickRetry is DefaultRetry without its pauses.
var quickRetry = RetryPolicy{Attempts: 4, Pauses: []time.Duration{time.Millisecond}}

func TestAfter_ADuplicateIsSkipped(t *testing.T) {
	env := testDB(t)
	var runs atomic.Int32
	f := env.in.After("t", func(context.Context, *Message) error {
		runs.Add(1)

		return nil
	}, RetryPolicy{})

	for range 3 {
		require.NoError(t, f(context.Background(), message("id-1", `{"a":1}`)))
	}
	assert.Equal(t, int32(1), runs.Load())
	assert.Equal(t, 1, env.count(t, "status = 0 AND payload IS NULL AND headers IS NULL"))
}

func TestAfter_TheSameIDOnAnotherConsumerOrWithAnotherPayloadIsProcessed(t *testing.T) {
	env := testDB(t)
	var runs atomic.Int32
	handler := func(context.Context, *Message) error {
		runs.Add(1)

		return nil
	}
	ctx := context.Background()

	require.NoError(t, env.in.After("request", handler, RetryPolicy{})(ctx, message("id-1", `{"a":1}`)))
	require.NoError(t, env.in.After("response", handler, RetryPolicy{})(ctx, message("id-1", `{"a":1}`)))
	require.NoError(t, env.in.After("request", handler, RetryPolicy{})(ctx, message("id-1", `{"a":2}`)))
	require.NoError(t, env.in.After("request", handler, RetryPolicy{})(ctx, message("id-1", `{"a":2}`)))

	assert.Equal(t, int32(3), runs.Load())
	assert.Equal(t, 3, env.count(t, "event_id = 'id-1'"))
}

func TestAfter_TheResponseCommitsWithTheRow(t *testing.T) {
	env := testDB(t)
	f := env.in.After("t", func(ctx context.Context, _ *Message) error {
		require.True(t, OnRow(ctx, effect("response")))

		return nil
	}, RetryPolicy{})

	require.NoError(t, f(context.Background(), message("id-1", "v")))
	require.NoError(t, f(context.Background(), message("id-1", "v")))
	assert.Equal(t, []string{"response"}, env.effects(t))
	assert.False(t, OnRow(context.Background(), effect("outside")))
}

func TestAfter_RetriesFollowThePolicy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy RetryPolicy
		err    error
		runs   int32
	}{
		{"no policy runs once", RetryPolicy{}, errors.New("timeout"), 1},
		{"a policy runs to its attempts", quickRetry, errors.New("timeout"), 4},
		{"a permanent error is not tried again", quickRetry, Permanent(errors.New("bad")), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := testDB(t)
			var runs atomic.Int32
			f := env.in.After("t", func(context.Context, *Message) error {
				runs.Add(1)

				return tc.err
			}, tc.policy)

			require.NoError(t, f(context.Background(), message("id-1", "v")), "a dead letter is recorded")
			assert.Equal(t, tc.runs, runs.Load())
			assert.Equal(t, 1, env.count(t, "status = 2 AND attempts = $1", tc.runs))
		})
	}
}

func TestAfter_ASuccessAfterAFailureIsDone(t *testing.T) {
	env := testDB(t)
	var runs atomic.Int32
	f := env.in.After("t", func(ctx context.Context, _ *Message) error {
		n := runs.Add(1)
		OnRow(ctx, effect(fmt.Sprintf("response %d", n)))
		if n < 3 {
			return errors.New("timeout")
		}

		return nil
	}, quickRetry)

	require.NoError(t, f(context.Background(), message("id-1", "v")))
	assert.Equal(t, int32(3), runs.Load())
	assert.Equal(t, 1, env.count(t, "status = 0 AND attempts = 3"))
	assert.Equal(t, []string{"response 3"}, env.effects(t), "the failed attempts write no response")
}

func TestDeadLetter_KeepsThePayloadAndTheLastAttemptsWrites(t *testing.T) {
	env := testDB(t)
	var runs atomic.Int32
	f := env.in.After("t", func(ctx context.Context, _ *Message) error {
		OnRow(ctx, effect(fmt.Sprintf("failure response %d", runs.Add(1))))

		return errors.New("deadlock")
	}, quickRetry)

	m := message("id-1", `{"a":1}`)
	require.NoError(t, f(context.Background(), m))
	assert.Equal(t, []string{"failure response 4"}, env.effects(t))

	var (
		payload, headers, cause, key string
		attempts                     int
	)
	require.NoError(t, env.db.QueryRow(`SELECT convert_from(payload, 'UTF8'), headers::text, error, key, attempts
		FROM processed_events WHERE status = 2`).Scan(&payload, &headers, &cause, &key, &attempts))
	assert.Equal(t, `{"a":1}`, payload)
	assert.JSONEq(t, `[{"k":"ce_id","v":"aWQtMQ=="}]`, headers)
	assert.Equal(t, "deadlock", cause)
	assert.Equal(t, "company", key)
	assert.Equal(t, 4, attempts)

	require.NoError(t, f(context.Background(), m))
	assert.Equal(t, int32(4), runs.Load(), "a dead message delivered again is skipped")
}

func TestAfter_APanicIsADeadLetterWithoutRetries(t *testing.T) {
	env := testDB(t)
	var runs atomic.Int32
	f := env.in.After("t", func(context.Context, *Message) error {
		runs.Add(1)
		panic("nil map")
	}, quickRetry)

	require.NoError(t, f(context.Background(), message("id-1", "v")))
	assert.Equal(t, int32(1), runs.Load())
	assert.Equal(t, 1, env.count(t, "status = 2 AND error = 'panic: nil map'"))
}

func TestAfter_AMessageWhoseSessionEndsDuringAPauseIsNotRecorded(t *testing.T) {
	env := testDB(t)
	stop := make(chan struct{})
	var runs atomic.Int32
	f := env.in.After("t", func(context.Context, *Message) error {
		if runs.Add(1) == 1 {
			time.AfterFunc(50*time.Millisecond, func() { close(stop) })
		}

		return errors.New("timeout")
	}, DefaultRetry)

	start := time.Now()
	err := f(withStop(context.Background(), stop), message("id-1", "v"))
	require.ErrorIs(t, err, errStopped)
	assert.Less(t, time.Since(start), 900*time.Millisecond, "the pause of 1 s ended with the session")
	assert.Equal(t, int32(1), runs.Load())
	assert.Equal(t, 0, env.count(t, "true"), "no dead letter: the next owner gets the message")
}

func TestAfter_AHandlerCutByTheCancelIsNotRecorded(t *testing.T) {
	env := testDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	f := env.in.After("t", func(context.Context, *Message) error {
		cancel()

		return context.Canceled
	}, RetryPolicy{})

	require.ErrorIs(t, f(ctx, message("id-1", "v")), errStopped)
	assert.Equal(t, 0, env.count(t, "true"))
}

// flakyDB fails the first BeginTx calls as a lost connection would.
type flakyDB struct {
	*sql.DB
	fails atomic.Int32
}

func (f *flakyDB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	if f.fails.Add(-1) >= 0 {
		return nil, driver.ErrBadConn
	}

	return f.DB.BeginTx(ctx, opts)
}

func TestAfter_TheRecordIsTriedAgainNotTheHandler(t *testing.T) {
	env := testDB(t)
	flaky := &flakyDB{DB: env.db}
	flaky.fails.Store(2)
	in, err := New(flaky, env.in.opts)
	require.NoError(t, err)
	var runs atomic.Int32
	f := in.After("t", func(ctx context.Context, _ *Message) error {
		runs.Add(1)
		OnRow(ctx, effect("response"))

		return nil
	}, RetryPolicy{})

	require.NoError(t, f(context.Background(), message("id-1", "v")))
	assert.Equal(t, int32(1), runs.Load())
	assert.Equal(t, 1, env.count(t, "status = 0"))
	assert.Equal(t, []string{"response"}, env.effects(t))
}

func TestRetention_DeletesOnlyExpiredDoneRowsInBatches(t *testing.T) {
	env := testDB(t, func(o *Options) { o.Retention = time.Hour })
	insert := func(n, status int, age string) {
		_, err := env.db.Exec(`INSERT INTO processed_events (consumer, event_id, digest, status, topic, kpartition,
			koffset, received_at) SELECT 'c', $1 || i, '\x00', $2, 't', 0, i, now() - $3::interval
			FROM generate_series(1, $4) i`, fmt.Sprintf("%d-%s-", status, age), status, age, n)
		require.NoError(t, err)
	}
	insert(12, statusDone, "2 hours")
	insert(3, statusDone, "10 minutes")
	insert(2, statusQueued, "2 hours")
	insert(2, statusDead, "2 hours")

	var batches []int64
	for {
		n, locked, err := env.in.deleteBatch(context.Background(), env.in.q.deleteExpired, statusDone, time.Hour, 5)
		require.NoError(t, err)
		require.True(t, locked)
		batches = append(batches, n)
		if n < 5 {
			break
		}
	}
	assert.Equal(t, []int64{5, 5, 2}, batches)
	assert.Equal(t, 3, env.count(t, "status = 0"))
	assert.Equal(t, 2, env.count(t, "status = 1"))
	assert.Equal(t, 2, env.count(t, "status = 2"))

	insert(7, statusDone, "3 hours")
	n, err := env.in.deleteExpired(context.Background(), 5)
	require.NoError(t, err)
	assert.Equal(t, int64(7), n)
	assert.Equal(t, 3, env.count(t, "status = 0"))
	assert.Equal(t, 2, env.count(t, "status = 2"), "dead letters are kept without DeadRetention")
}

func TestRetention_DeletesDeadLettersPastTheirOwnRetention(t *testing.T) {
	env := testDB(t, func(o *Options) {
		o.Retention = time.Hour
		o.DeadRetention = 24 * time.Hour
	})
	for i, age := range []string{"2 hours", "2 days", "3 days"} {
		_, err := env.db.Exec(`INSERT INTO processed_events (consumer, event_id, digest, status, topic, kpartition,
			koffset, received_at, updated_at) VALUES ('c', $1, '\x00', 2, 't', 0, 0, now() - $2::interval,
			now() - $2::interval)`, fmt.Sprint("dead-", i), age)
		require.NoError(t, err)
	}
	// a queued row however old, and a dead letter received long ago but tried again an hour ago, are kept
	_, err := env.db.Exec(`INSERT INTO processed_events (consumer, event_id, digest, status, topic, kpartition,
		koffset, received_at, updated_at) VALUES
		('c', 'queued', '\x00', 1, 't', 0, 0, now() - interval '5 days', now() - interval '5 days'),
		('c', 'dead-again', '\x00', 2, 't', 0, 0, now() - interval '5 days', now() - interval '1 hour')`)
	require.NoError(t, err)

	n, err := env.in.deleteExpired(context.Background(), 5)
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)
	assert.Equal(t, 1, env.count(t, "status = 2 AND event_id = 'dead-0'"))
	assert.Equal(t, 1, env.count(t, "status = 2 AND event_id = 'dead-again'"))
	assert.Equal(t, 1, env.count(t, "status = 1"))
}

func TestRetention_OnePodAtATime(t *testing.T) {
	env := testDB(t, func(o *Options) { o.Retention = time.Hour })
	_, err := env.db.Exec(`INSERT INTO processed_events (consumer, event_id, digest, status, topic, kpartition,
		koffset, received_at) VALUES ('c', 'e', '\x00', 0, 't', 0, 0, now() - interval '2 hours')`)
	require.NoError(t, err)

	holder, err := env.db.Begin()
	require.NoError(t, err)
	defer func() { _ = holder.Rollback() }()
	_, err = holder.Exec(`SELECT pg_advisory_xact_lock($1)`, env.in.opts.LockKey)
	require.NoError(t, err)

	n, err := env.in.deleteExpired(context.Background(), 5)
	require.NoError(t, err)
	assert.Zero(t, n)
	assert.Equal(t, 1, env.count(t, "true"))
}

type countingMetrics struct {
	mu       sync.Mutex
	results  map[string]int
	deadRows int
	ages     map[string]float64
	txs      map[string]int
	txTook   []float64
	waits    []float64
}

func (m *countingMetrics) TxDuration(consumer string, seconds float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.txs == nil {
		m.txs = map[string]int{}
	}
	m.txs[consumer]++
	m.txTook = append(m.txTook, seconds)
}

func (m *countingMetrics) TxSlotWait(_ string, seconds float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.waits = append(m.waits, seconds)
}

func (m *countingMetrics) QueuedOldestAge(consumer string, seconds float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ages == nil {
		m.ages = map[string]float64{}
	}
	m.ages[consumer] = seconds
}

func (m *countingMetrics) Message(consumer, result string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.results == nil {
		m.results = map[string]int{}
	}
	m.results[consumer+"/"+result]++
}

func (m *countingMetrics) DeadRows(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deadRows = n
}

func (m *countingMetrics) snapshot() (map[string]int, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]int{}
	for k, v := range m.results {
		out[k] = v
	}

	return out, m.deadRows
}

func TestMetrics_CountTheResultsAndTheDeadRows(t *testing.T) {
	metrics := &countingMetrics{}
	env := testDB(t, func(o *Options) { o.Metrics = metrics })
	fails := env.in.After("t", func(context.Context, *Message) error { return errors.New("timeout") },
		RetryPolicy{Attempts: 2})
	ok := env.in.After("t", func(context.Context, *Message) error { return nil }, RetryPolicy{})
	ctx := context.Background()
	require.NoError(t, fails(ctx, message("id-1", "v")))
	require.NoError(t, ok(ctx, message("id-2", "v")))
	require.NoError(t, ok(ctx, message("id-2", "v")))

	w := worker(t, env.in, WorkerOptions{DeadRowsEvery: 10 * time.Millisecond})
	w.Start(ctx)
	require.Eventually(t, func() bool {
		_, dead := metrics.snapshot()

		return dead == 1
	}, 2*time.Second, 10*time.Millisecond)
	require.NoError(t, w.Stop(ctx))

	results, _ := metrics.snapshot()
	assert.Equal(t, map[string]int{"t/retry": 1, "t/dead": 1, "t/done": 1, "t/duplicate": 1}, results)
}

func TestWorker_StopWithoutStart(t *testing.T) {
	env := testDB(t)
	require.NoError(t, worker(t, env.in, WorkerOptions{}).Stop(context.Background()))
}

func TestCheck_FailsWithoutTheTable(t *testing.T) {
	env := testDB(t)
	require.NoError(t, env.in.Check(context.Background()))

	other, err := New(env.db, Options{Service: "x", Table: "processed_events_missing"})
	require.NoError(t, err)
	err = other.Check(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "its migration is not applied")
}

// A ce_id that TEXT or the primary key cannot hold must not hold the partition: it is replaced by its digest.
func TestAfter_APoisonIDDoesNotHoldThePartition(t *testing.T) {
	env := testDB(t)
	var runs atomic.Int32
	f := env.in.After("t", func(context.Context, *Message) error {
		runs.Add(1)

		return nil
	}, RetryPolicy{})
	for _, id := range []string{"bad-\xff-id", "nul-\x00-id", strings.Repeat("x", 3000)} {
		m := FromKafka(&sarama.ConsumerMessage{Topic: "t", Value: []byte("v"),
			Headers: []*sarama.RecordHeader{{Key: []byte("ce_id"), Value: []byte(id)}}})
		stop := make(chan struct{})
		timer := time.AfterFunc(4*time.Second, func() { close(stop) })
		start := time.Now()
		require.NoError(t, f(withStop(context.Background(), stop), m))
		require.NoError(t, f(withStop(context.Background(), stop), m), "delivered again: a duplicate")
		timer.Stop()
		assert.Less(t, time.Since(start), time.Second)
	}
	assert.Equal(t, int32(3), runs.Load())
	assert.Equal(t, 3, env.count(t, "status = 0 AND event_id LIKE 'sha256:%'"))
}

func TestDeadLetter_HeadersAndErrorsTheTableCannotHoldAreStored(t *testing.T) {
	env := testDB(t)
	f := env.in.After("t", func(context.Context, *Message) error {
		return errors.New("bad \x00 byte \xff")
	}, RetryPolicy{})
	m := message("id-1", "v")
	m.Headers = append(m.Headers, Header{Key: "k\x00\xff", Value: []byte{0, 0xff}})

	require.NoError(t, f(context.Background(), m))
	assert.Equal(t, 1, env.count(t, "status = 2 AND headers IS NOT NULL AND error = 'bad  byte \uFFFD'"))
}

func TestRecord_AWriteTheTableRefusesIsDroppedAndTheMessageIsADeadLetter(t *testing.T) {
	env := testDB(t)
	var runs atomic.Int32
	f := env.in.After("t", func(ctx context.Context, _ *Message) error {
		runs.Add(1)
		OnRow(ctx, func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `SELECT 'not a number'::int`)

			return err
		})

		return nil
	}, quickRetry)

	stop := make(chan struct{})
	time.AfterFunc(2*time.Second, func() { close(stop) })
	require.NoError(t, f(withStop(context.Background(), stop), message("id-1", "v")))
	assert.Equal(t, int32(1), runs.Load())
	assert.Equal(t, 1, env.count(t, "status = 2 AND error LIKE '%invalid input syntax%'"))
}

func TestRecord_AMessageTheTableRefusesEntirelyIsSkipped(t *testing.T) {
	metrics := &countingMetrics{}
	env := testDB(t, func(o *Options) { o.Metrics = metrics })
	_, err := env.db.Exec(`CREATE FUNCTION refuse() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'refused' USING ERRCODE = '22000'; END $$;
		CREATE TRIGGER refuse BEFORE INSERT ON processed_events FOR EACH ROW
		WHEN (NEW.event_id LIKE 'refuse%') EXECUTE FUNCTION refuse()`)
	require.NoError(t, err)
	var runs atomic.Int32
	f := env.in.After("t", func(context.Context, *Message) error {
		runs.Add(1)

		return nil
	}, RetryPolicy{})

	stop := make(chan struct{})
	time.AfterFunc(2*time.Second, func() { close(stop) })
	require.NoError(t, f(withStop(context.Background(), stop), message("refuse-1", "v")))
	assert.Equal(t, int32(1), runs.Load())
	assert.Equal(t, 0, env.count(t, "true"))
	results, _ := metrics.snapshot()
	assert.Equal(t, 1, results["t/skipped"])
}

func TestRecord_APanicInARowWriteIsADeadLetterWithoutIt(t *testing.T) {
	env := testDB(t)
	var runs atomic.Int32
	f := env.in.After("t", func(ctx context.Context, _ *Message) error {
		runs.Add(1)
		OnRow(ctx, func(context.Context, *sql.Tx) error { panic("encode the response") })

		return nil
	}, quickRetry)

	stop := make(chan struct{})
	time.AfterFunc(2*time.Second, func() { close(stop) })
	require.NoError(t, f(withStop(context.Background(), stop), message("id-1", "v")))
	assert.Equal(t, int32(1), runs.Load(), "the handler is not run again")
	assert.Equal(t, 1, env.count(t, "status = 2 AND error = 'panic in a row write: encode the response'"))
}

// The dedup check or the record fails until the session ends: the message must not count as recorded.
func TestAfter_ADatabaseErrorLeavesTheMessageUnmarked(t *testing.T) {
	env := testDB(t)
	closed, err := sql.Open("postgres", env.url)
	require.NoError(t, err)
	require.NoError(t, closed.Close())
	flaky := &flakyDB{DB: env.db}
	flaky.fails.Store(1 << 30)
	for name, db := range map[string]DB{"dedup check": closed, "record": flaky} {
		in, newErr := New(db, env.in.opts)
		require.NoError(t, newErr)
		f := in.After("t", func(context.Context, *Message) error { return nil }, RetryPolicy{})
		stop := make(chan struct{})
		time.AfterFunc(300*time.Millisecond, func() { close(stop) })
		require.ErrorIs(t, f(withStop(context.Background(), stop), message("id-"+name, "v")), errStopped, name)
	}
	assert.Equal(t, 0, env.count(t, "true"))
}

func TestMigration_TheDownToleratesAMissingTableAndKeepsItsGuard(t *testing.T) {
	env := testDB(t)
	_, down, err := Migration(Options{})
	require.NoError(t, err)
	_, err = env.db.Exec(`INSERT INTO processed_events (consumer, event_id, digest, status, topic, kpartition,
		koffset) VALUES ('c', 'e', '\x00', 2, 't', 0, 0)`)
	require.NoError(t, err)
	_, err = env.db.Exec(down)
	require.ErrorContains(t, err, "still has queued or dead rows")

	_, err = env.db.Exec(`DELETE FROM processed_events`)
	require.NoError(t, err)
	_, err = env.db.Exec(down)
	require.NoError(t, err)
	_, err = env.db.Exec(down)
	require.NoError(t, err, "the table is gone: the down passes")
}

func TestFromKafka_TakesTheIDFromTheCloudevent(t *testing.T) {
	m := FromKafka(&sarama.ConsumerMessage{Topic: "t", Partition: 2, Offset: 9, Value: []byte("v"),
		Headers: []*sarama.RecordHeader{{Key: []byte("ce_type"), Value: []byte("x")}, nil,
			{Key: []byte("ce_id"), Value: []byte("abc")}}})
	assert.Equal(t, "abc", m.ID)
	typ, ok := m.Header("ce_type")
	assert.True(t, ok)
	assert.Equal(t, "x", string(typ))

	assert.Equal(t, "t/2/9", FromKafka(&sarama.ConsumerMessage{Topic: "t", Partition: 2, Offset: 9}).ID)

	poison := func(id string) string {
		return FromKafka(&sarama.ConsumerMessage{Headers: []*sarama.RecordHeader{{Key: []byte("ce_id"),
			Value: []byte(id)}}}).ID
	}
	stable := poison("a\xffb")
	assert.Equal(t, stable, poison("a\xffb"))
	assert.NotEqual(t, poison("a\xffb"), poison("a\xfeb"))
	assert.Regexp(t, `^sha256:[0-9a-f]{64}$`, poison(strings.Repeat("x", maxIDBytes+1)))
	assert.Equal(t, strings.Repeat("x", maxIDBytes), poison(strings.Repeat("x", maxIDBytes)))
}

func TestTransient(t *testing.T) {
	for _, err := range []error{
		driver.ErrBadConn,
		fmt.Errorf("query: %w", context.DeadlineExceeded),
		&pq.Error{Code: "08006"},
		&pq.Error{Code: "40001"},
		&pq.Error{Code: "40P01"},
		&pq.Error{Code: "53300"},
		&pq.Error{Code: "57P01"},
		&netTimeoutError{},
	} {
		assert.True(t, Transient(err), "%v", err)
	}
	for _, err := range []error{
		nil,
		errors.New("business"),
		sql.ErrNoRows,
		context.Canceled,
		&pq.Error{Code: "23505"},
		&pq.Error{Code: "22P02"},
		Permanent(driver.ErrBadConn),
	} {
		assert.False(t, Transient(err), "%v", err)
	}
}

func TestSetCause_ReachesOnlyItsOwnCaller(t *testing.T) {
	SetCause(context.Background(), driver.ErrBadConn) // no place for it: nothing happens

	ctx, cause := WithCause(context.Background())
	require.NoError(t, cause())
	SetCause(ctx, nil)
	require.NoError(t, cause(), "nil leaves nothing")
	SetCause(context.WithValue(ctx, rowKey{}, &rowWrites{}), errors.New("first"))
	SetCause(ctx, driver.ErrBadConn)
	assert.Equal(t, driver.ErrBadConn, cause(), "a later call replaces it, through derived contexts too")

	SetCause(Detach(ctx), errors.New("after the handler"))
	assert.Equal(t, driver.ErrBadConn, cause(), "work that outlives the handler does not reach it")
}

type netTimeoutError struct{}

func (netTimeoutError) Error() string   { return "i/o timeout" }
func (netTimeoutError) Timeout() bool   { return true }
func (netTimeoutError) Temporary() bool { return true }

func TestNew_ChecksItsOptions(t *testing.T) {
	db := &sql.DB{}
	for _, opts := range []Options{
		{},
		{Service: "s", Table: "Processed"},
		{Service: "s", Table: `x"y`},
		{Service: "s", Table: strings.Repeat("p", 55)}, // its index name is 64 bytes
		{Service: "s", Retention: -time.Second},
		{Service: "s", DeadRetention: -time.Second},
	} {
		_, err := New(db, opts)
		require.Error(t, err, "%+v", opts)
	}
	_, err := New(nil, Options{Service: "s"})
	require.Error(t, err)

	in, err := New(db, Options{Service: "s"})
	require.NoError(t, err)
	assert.Equal(t, DefaultRetention, in.opts.Retention)
	assert.NotZero(t, in.opts.LockKey)
}

// The lock key must stay the value the workers of every deployed version take: int64(fnv-1a >> 1).
func TestLockKey_IsTheHashShiftedRightByOne(t *testing.T) {
	source := rand.NewChaCha8([32]byte{2})
	random := rand.New(source)
	names := []string{"", "inbox:billz_inventory_service_v2:processed_events"}
	for range 100_000 {
		name := make([]byte, random.IntN(64))
		_, _ = source.Read(name)
		names = append(names, string(name))
	}
	for _, name := range names {
		h := fnv.New64a()
		_, _ = h.Write([]byte(name))
		require.Equal(t, int64(h.Sum64()>>1), lockKey(name), "%q", name)
	}
}

func TestMigration_RendersTheTableName(t *testing.T) {
	up, down, err := Migration(Options{Table: "inbox_x"})
	require.NoError(t, err)
	assert.Contains(t, up, `CREATE TABLE IF NOT EXISTS "inbox_x" (`)
	assert.Contains(t, up, `CREATE INDEX IF NOT EXISTS "inbox_x_dead" ON "inbox_x" (updated_at) WHERE status = 2;`)
	assert.Contains(t, down, `'inbox_x still has queued or dead rows: they would be lost'`)
	for _, s := range []string{up, down} {
		assert.NotContains(t, s, "processed_events")
		assert.NotContains(t, s, "{{")
	}
}
