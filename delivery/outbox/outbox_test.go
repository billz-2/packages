package outbox //nolint:testpackage // tests the relay internals against a real PostgreSQL

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IBM/sarama"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testLog = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

// relayPoolSize is what RelayDB asks for: one connection for the round, one for the backlog gauge.
const relayPoolSize = 2

type testEnv struct {
	admin  *sqlx.DB
	conn   *sqlx.DB
	ob     *Outbox
	opts   Options
	url    string
	schema string
	lock   int64
}

// testDB needs a PostgreSQL URL in OUTBOX_TEST_POSTGRES_DSN. Without one it skips, except in CI, whose
// workflow starts a PostgreSQL for these tests.
func testDB(t testing.TB) *testEnv {
	t.Helper()

	return testDBWith(t, Options{})
}

func testDBWith(t testing.TB, opts Options) *testEnv {
	t.Helper()

	url := os.Getenv("OUTBOX_TEST_POSTGRES_DSN")
	if url == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("no PostgreSQL in CI: OUTBOX_TEST_POSTGRES_DSN is not set")
		}
		t.Skip("no PostgreSQL: set OUTBOX_TEST_POSTGRES_DSN")
	}

	admin, err := sqlx.Connect("postgres", url)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })

	env := &testEnv{admin: admin, schema: "outbox_test_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")}
	_, err = admin.Exec("CREATE SCHEMA " + env.schema)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = admin.Exec("DROP SCHEMA " + env.schema + " CASCADE") })

	// the lock of the relays of this schema, so that no relay of another test can take it
	h := fnv.New64a()
	_, _ = h.Write([]byte(env.schema))
	env.lock = int64(h.Sum64() >> 1)

	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	env.url = url + sep + "search_path=" + env.schema
	env.conn, err = sqlx.Connect("postgres", env.url)
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.conn.Close() })

	up, _, err := Migration(opts)
	require.NoError(t, err)
	_, err = env.conn.Exec(up)
	require.NoError(t, err)

	opts.Service = "outbox_test"
	opts.Logger = testLog
	env.opts = opts
	env.ob, err = New(env.conn, opts)
	require.NoError(t, err)

	return env
}

func (e *testEnv) withMetrics(t testing.TB, m Metrics) {
	t.Helper()

	opts := e.opts
	opts.Metrics = m
	var err error
	e.ob, err = New(e.conn, opts)
	require.NoError(t, err)
}

// pool names its sessions so that their backends can be found in pg_stat_activity.
func (e *testEnv) pool(t testing.TB, name string) *sqlx.DB {
	t.Helper()

	conn, err := sqlx.Connect("postgres", e.url+"&application_name="+name)
	require.NoError(t, err)
	conn.SetMaxOpenConns(relayPoolSize)
	t.Cleanup(func() { _ = conn.Close() })

	return conn
}

func (e *testEnv) rows(t testing.TB, table string) int {
	t.Helper()

	var n int
	require.NoError(t, e.conn.Get(&n, `SELECT count(*) FROM `+table))

	return n
}

// relay names the round's session as the pool does, so that a test finds it by name.
func (e *testEnv) relay(t testing.TB, name string, producer Producer, opts ...func(*RelayOptions)) *Relay {
	t.Helper()

	return e.relayOn(t, e.pool(t, name).DB, producer, append([]func(*RelayOptions){func(o *RelayOptions) {
		o.ApplicationName = name
	}}, opts...)...)
}

func (e *testEnv) relayOn(t testing.TB, db RelayDB, producer Producer, opts ...func(*RelayOptions)) *Relay {
	t.Helper()

	o := RelayOptions{
		LockKey:         e.lock,
		PollInterval:    5 * time.Millisecond,
		MinResendDelay:  30 * time.Millisecond,
		CloseTimeout:    2 * time.Second,
		BacklogInterval: time.Hour,
	}
	for _, opt := range opts {
		opt(&o)
	}

	r, err := e.ob.Relay(db, producer, o)
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Close() })

	return r
}

func (e *testEnv) publish(t testing.TB, msgs ...Message) {
	t.Helper()

	for start := 0; start < len(msgs); start += 1000 {
		require.NoError(t, e.ob.Publish(context.Background(), nil, msgs[start:min(start+1000, len(msgs))]...))
	}
}

type fakeKafka struct {
	mu      sync.Mutex
	got     []*sarama.ProducerMessage
	calls   int
	latency time.Duration
	// delay replaces latency: the time of a call by its records
	delay func(msgs []*sarama.ProducerMessage) time.Duration
	// written: Kafka wrote the message, but the acknowledgement was lost on the way back
	fail func(msg *sarama.ProducerMessage) (err error, written bool)
	// before and after run outside the lock
	before func(msgs []*sarama.ProducerMessage)
	after  func(msgs []*sarama.ProducerMessage)
}

func (f *fakeKafka) SendMessages(msgs []*sarama.ProducerMessage) error {
	if f.before != nil {
		f.before(msgs)
	}
	switch {
	case f.delay != nil:
		time.Sleep(f.delay(msgs))
	case f.latency > 0:
		time.Sleep(f.latency)
	}

	f.mu.Lock()
	f.calls++
	var errs sarama.ProducerErrors
	for _, msg := range msgs {
		written := true
		var err error
		if f.fail != nil {
			err, written = f.fail(msg)
		}
		if written {
			f.got = append(f.got, msg)
		}
		if err != nil {
			errs = append(errs, &sarama.ProducerError{Msg: msg, Err: err})
		}
	}
	f.mu.Unlock()

	if f.after != nil {
		f.after(msgs)
	}
	if len(errs) > 0 {
		return errs
	}

	return nil
}

func value(msg *sarama.ProducerMessage) string {
	raw, _ := msg.Value.Encode()

	return string(raw)
}

func key(msg *sarama.ProducerMessage) string {
	raw, _ := msg.Key.Encode()

	return string(raw)
}

func (f *fakeKafka) byKey() map[string][]int {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make(map[string][]int)
	for _, msg := range f.got {
		n, _ := strconv.Atoi(value(msg))
		out[msg.Topic+"/"+key(msg)] = append(out[msg.Topic+"/"+key(msg)], n)
	}

	return out
}

func (f *fakeKafka) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.got)
}

func (f *fakeKafka) sentCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.calls
}

func msgOf(topic, key string, n int) Message {
	return Message{Topic: topic, Key: key, Value: []byte(strconv.Itoa(n))}
}

func inOrder(t *testing.T, got map[string][]int, keys []string, n int) {
	t.Helper()

	want := make([]int, n)
	for i := range want {
		want[i] = i
	}
	for _, k := range keys {
		assert.Equal(t, want, got[k], "the records of %s", k)
	}
}

// resentInOrder lets a key start again at or before a record (a round sent again), but never skip one, and
// the last record must arrive last.
func resentInOrder(t *testing.T, seq []int, n int) {
	t.Helper()

	require.NotEmpty(t, seq)
	assert.Equal(t, 0, seq[0], "the first record arrives first")
	for i := 1; i < len(seq); i++ {
		assert.LessOrEqual(t, seq[i], seq[i-1]+1, "record %d arrived after %d: one was skipped", seq[i], seq[i-1])
	}
	assert.Equal(t, n-1, seq[len(seq)-1], "the last record arrives last")
}

func TestPublish_FollowsTheTransaction(t *testing.T) {
	env := testDB(t)
	ob := env.ob
	ctx := context.Background()

	tx, err := env.conn.Beginx()
	require.NoError(t, err)
	require.NoError(t, ob.Publish(ctx, tx, msgOf("t", "k", 1)))
	require.NoError(t, tx.Rollback())
	assert.Equal(t, 0, env.rows(t, "outbox"), "a rolled back write is gone")

	tx, err = env.conn.Beginx()
	require.NoError(t, err)
	require.NoError(t, ob.Publish(ctx, tx, msgOf("t", "k", 1), msgOf("t", "k", 2)))
	require.NoError(t, tx.Commit())
	assert.Equal(t, 2, env.rows(t, "outbox"))

	require.NoError(t, ob.Publish(ctx, nil, msgOf("t", "k", 3)))
	assert.Equal(t, 3, env.rows(t, "outbox"), "without a transaction it is written at once")

	assert.Error(t, ob.Publish(ctx, nil, Message{Key: "k"}), "a message without a topic")
}

func TestMigration_TheDownToleratesAMissingTableAndKeepsItsGuard(t *testing.T) {
	env := testDB(t)
	_, down, err := Migration(Options{})
	require.NoError(t, err)
	_, err = env.conn.Exec(`INSERT INTO outbox_failed (id, topic, key, created_at, error)
		VALUES (1, 't', 'k', now(), 'e')`)
	require.NoError(t, err)
	_, err = env.conn.Exec(down)
	require.ErrorContains(t, err, "outbox_failed still has rows")

	_, err = env.conn.Exec(`DELETE FROM outbox_failed`)
	require.NoError(t, err)
	_, err = env.conn.Exec(down)
	require.NoError(t, err)
	_, err = env.conn.Exec(down)
	require.NoError(t, err, "the tables are gone: the down passes")
}

func TestRelay_SendsTheRecordAndDeletesTheRow(t *testing.T) {
	env := testDB(t)
	kafka := &fakeKafka{}

	env.publish(t, Message{
		Topic:   "v2.test.created",
		Key:     "company-1",
		Value:   []byte(`{"a":1}`),
		Headers: []Header{{Key: "ce_id", Value: []byte("id-1")}, {Key: "ce_type", Value: []byte("x")}},
	}, Message{Topic: "v2.test.created", Key: "", Value: nil})

	env.relay(t, "send", kafka).Start()
	require.Eventually(t, func() bool { return kafka.count() == 2 && env.rows(t, "outbox") == 0 },
		10*time.Second, 10*time.Millisecond)

	kafka.mu.Lock()
	defer kafka.mu.Unlock()
	first := kafka.got[0]
	assert.Equal(t, "v2.test.created", first.Topic)
	assert.Equal(t, "company-1", key(first))
	assert.JSONEq(t, `{"a":1}`, value(first))
	assert.Equal(t, []sarama.RecordHeader{
		{Key: []byte("ce_id"), Value: []byte("id-1")},
		{Key: []byte("ce_type"), Value: []byte("x")},
	}, first.Headers)

	second := kafka.got[1]
	assert.Empty(t, key(second), "an empty key is sent as an empty key")
	assert.Nil(t, second.Value, "a nil value stays nil")
}

func TestRelay_KeepsTheOrderOfEachKey(t *testing.T) {
	env := testDB(t)
	kafka := &fakeKafka{}

	const n = 60
	keys := []string{"a", "b", "c", "d"}
	var msgs []Message
	for i := range n {
		for _, k := range keys {
			msgs = append(msgs, msgOf("t", k, i))
		}
	}
	env.publish(t, msgs...)

	env.relay(t, "order", kafka, func(o *RelayOptions) { o.BatchSize = 25 }).Start()
	require.Eventually(t, func() bool { return env.rows(t, "outbox") == 0 }, 10*time.Second, 10*time.Millisecond)

	got := kafka.byKey()
	inOrder(t, got, []string{"t/a", "t/b", "t/c", "t/d"}, n)
}

func TestRelay_CutBatchKeepsItsUnsentRows(t *testing.T) {
	env := testDB(t)

	const n = 10
	var msgs []Message
	for i := range n {
		msgs = append(msgs, msgOf("t", "k", i))
	}
	env.publish(t, msgs...)

	release := make(chan struct{})
	inThirdWave := make(chan struct{})
	calls := 0
	kafka := &fakeKafka{before: func([]*sarama.ProducerMessage) {
		calls++
		if calls == 3 {
			close(inThirdWave)
			<-release
		}
	}}

	r := env.relay(t, "cut", kafka)
	r.Start()
	<-inThirdWave

	closed := make(chan error, 1)
	go func() { closed <- r.Close() }()
	time.Sleep(50 * time.Millisecond)
	close(release)
	require.NoError(t, <-closed)

	assert.Equal(t, 3, kafka.count(), "three waves of one record each")
	assert.Equal(t, n-3, env.rows(t, "outbox"), "only the acknowledged rows are deleted")

	next := &fakeKafka{}
	env.relay(t, "cut-next", next).Start()
	require.Eventually(t, func() bool { return env.rows(t, "outbox") == 0 }, 10*time.Second, 10*time.Millisecond)
	assert.Equal(t, []int{3, 4, 5, 6, 7, 8, 9}, next.byKey()["t/k"])
}

func TestRelay_KafkaFailureInTheMiddleOfABatchKeepsTheOrderOfEachKey(t *testing.T) {
	for _, written := range []bool{false, true} {
		t.Run(fmt.Sprintf("written=%v", written), func(t *testing.T) {
			env := testDB(t)

			const n = 20
			var msgs []Message
			for i := range n {
				msgs = append(msgs, msgOf("t", "a", i), msgOf("t", "b", i))
			}
			env.publish(t, msgs...)

			failures := 0
			kafka := &fakeKafka{fail: func(msg *sarama.ProducerMessage) (error, bool) {
				if key(msg) == "a" && value(msg) == "5" && failures < 3 {
					failures++
					return sarama.ErrNotLeaderForPartition, written
				}

				return nil, true
			}}

			r := env.relay(t, "fail", kafka)
			r.Start()
			require.Eventually(t, func() bool { return env.rows(t, "outbox") == 0 }, 10*time.Second,
				10*time.Millisecond)

			got := kafka.byKey()
			want := make([]int, 0, n)
			for i := range n {
				want = append(want, i)
				if written && i == 5 {
					// the resends of a record whose acknowledgement was lost come before the next record
					want = append(want, 5, 5, 5)
				}
			}
			assert.Equal(t, want, got["t/a"])
			resentInOrder(t, got["t/a"], n)
			inOrder(t, got, []string{"t/b"}, n)
			kafka.mu.Lock()
			assert.Equal(t, 3, failures)
			kafka.mu.Unlock()
		})
	}
}

func TestRelay_AFailingTopicDoesNotHoldTheOthers(t *testing.T) {
	env := testDB(t)

	var msgs []Message
	for i := range 30 {
		msgs = append(msgs, msgOf("denied", "k"+strconv.Itoa(i%3), i))
	}
	for i := range 10 {
		msgs = append(msgs, msgOf("ok", "k", i))
	}
	env.publish(t, msgs...)

	kafka := &fakeKafka{fail: func(msg *sarama.ProducerMessage) (error, bool) {
		if msg.Topic == "denied" {
			return sarama.ErrTopicAuthorizationFailed, false
		}

		return nil, true
	}}

	env.relay(t, "isolation", kafka, func(o *RelayOptions) { o.BatchSize = 10 }).Start()
	require.Eventually(t, func() bool { return len(kafka.byKey()["ok/k"]) == 10 && env.rows(t, "outbox") == 30 },
		10*time.Second, 10*time.Millisecond, "the other topic is sent while the failing topic's rows wait")

	assert.Equal(t, 0, env.rows(t, "outbox_failed"), "an authorization error is tried again")
	inOrder(t, kafka.byKey(), []string{"ok/k"}, 10)
}

func TestRelay_TwoRelaysOnOneTable(t *testing.T) {
	env := testDB(t)
	kafka := &fakeKafka{latency: time.Millisecond}

	batch := func(o *RelayOptions) { o.BatchSize = 7 }
	env.relay(t, "pod1", kafka, batch).Start()
	env.relay(t, "pod2", kafka, batch).Start()

	const n = 50
	keys := []string{"a", "b", "c"}
	var wg sync.WaitGroup
	for _, k := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range n {
				assert.NoError(t, env.ob.Publish(context.Background(), nil, msgOf("t", k, i)))
			}
		}()
	}
	wg.Wait()

	require.Eventually(t, func() bool { return kafka.count() == n*len(keys) && env.rows(t, "outbox") == 0 },
		20*time.Second, 10*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, n*len(keys), kafka.count(), "every record once")
	inOrder(t, kafka.byKey(), []string{"t/a", "t/b", "t/c"}, n)
}

// The round notices the lost session before its next wave and stops.
func TestRelay_CrashBetweenSendAndDeleteSendsAgainAndLosesNothing(t *testing.T) {
	env := testDB(t)

	const n = 12
	var msgs []Message
	for i := range n {
		msgs = append(msgs, msgOf("t", "k", i))
	}
	env.publish(t, msgs...)

	calls := 0
	kafka := &fakeKafka{}
	kafka.after = func([]*sarama.ProducerMessage) {
		calls++
		if calls == 4 {
			_, err := env.admin.Exec(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity
				WHERE application_name = 'crash' AND state = 'idle in transaction'`)
			assert.NoError(t, err)
		}
	}

	env.relay(t, "crash", kafka, func(o *RelayOptions) {
		o.BatchSize = 6
		o.SessionCheck = time.Nanosecond
	}).Start()
	require.Eventually(t, func() bool { return env.rows(t, "outbox") == 0 }, 10*time.Second, 10*time.Millisecond)

	got := kafka.byKey()["t/k"]
	assert.Equal(t, []int{0, 1, 2, 3, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}, got,
		"the round stops at the lost session; its four records are sent again, in order, before the others")
	resentInOrder(t, got, n)
}

func TestRelay_SetsAsideWhatCanNeverBeSent(t *testing.T) {
	env := testDB(t)

	env.publish(t,
		msgOf("t", "k", 0),
		Message{Topic: "t", Key: "k", Value: []byte("too large")},
		msgOf("t", "k", 2),
		Message{Topic: "missing", Key: "k", Value: []byte("m0")},
		Message{Topic: "missing", Key: "k2", Value: []byte("m1")},
	)
	_, err := env.conn.Exec(`UPDATE outbox SET headers = '{"not":"a list"}' WHERE payload = '2'`)
	require.NoError(t, err)
	env.publish(t, msgOf("t", "k", 3))

	kafka := &fakeKafka{fail: func(msg *sarama.ProducerMessage) (error, bool) {
		switch {
		case value(msg) == "too large":
			return sarama.ErrMessageSizeTooLarge, false
		case msg.Topic == "missing":
			return sarama.ErrUnknownTopicOrPartition, false
		}

		return nil, true
	}}

	r := env.relay(t, "aside", kafka, func(o *RelayOptions) { o.UnknownTopicGrace = 300 * time.Millisecond })
	start := time.Now()
	r.Start()
	require.Eventually(t, func() bool { return env.rows(t, "outbox_failed") == 4 }, 10*time.Second,
		10*time.Millisecond)
	assert.GreaterOrEqual(t, time.Since(start), 300*time.Millisecond, "the unknown topic is tried for its grace")
	require.Eventually(t, func() bool { return len(kafka.byKey()["t/k"]) == 2 }, 10*time.Second,
		10*time.Millisecond)

	assert.Equal(t, []int{0, 3}, kafka.byKey()["t/k"], "the records after a set aside one are sent")

	var failed []struct {
		Topic   string `db:"topic"`
		Payload string `db:"payload"`
		Error   string `db:"error"`
	}
	require.NoError(t, env.conn.Select(&failed, `SELECT topic, payload, error FROM outbox_failed ORDER BY id`))
	require.Len(t, failed, 4)
	assert.Equal(t, "too large", failed[0].Payload)
	assert.Contains(t, failed[0].Error, "Message was too large")
	assert.Equal(t, "2", failed[1].Payload)
	assert.Contains(t, failed[1].Error, "headers")
	for i, payload := range []string{"m0", "m1"} {
		assert.Equal(t, payload, failed[2+i].Payload)
		assert.Contains(t, failed[2+i].Error, "does not exist")
	}
	assert.Equal(t, 0, env.rows(t, "outbox"))
}

func TestRelay_SetsAsideARecordTheBrokersReject(t *testing.T) {
	env := testDB(t)
	env.publish(t,
		msgOf("t", "k", 0),
		Message{Topic: "t", Key: "k", Value: []byte("invalid")},
		Message{Topic: "t", Key: "k2", Value: []byte("old")},
		msgOf("t", "k", 3),
	)

	kafka := &fakeKafka{fail: func(msg *sarama.ProducerMessage) (error, bool) {
		switch value(msg) {
		case "invalid":
			return sarama.ErrInvalidRecord, false
		case "old":
			return sarama.ErrInvalidTimestamp, false
		}

		return nil, true
	}}

	env.relay(t, "rejected", kafka).Start()
	require.Eventually(t, func() bool { return env.rows(t, "outbox") == 0 }, 5*time.Second, 10*time.Millisecond,
		"the rejected records do not hold their topic")

	assert.Equal(t, []int{0, 3}, kafka.byKey()["t/k"])
	var failed []string
	require.NoError(t, env.conn.Select(&failed, `SELECT payload FROM outbox_failed ORDER BY id`))
	assert.Equal(t, []string{"invalid", "old"}, failed)
}

// The grace counts from the topic's first failure, not from the row.
func TestRelay_AnUnknownTopicForAMomentIsTriedAgain(t *testing.T) {
	env := testDB(t)
	env.publish(t, msgOf("t", "k", 0), msgOf("t", "k", 1))
	_, err := env.conn.Exec(`UPDATE outbox SET created_at = now() - interval '3 minutes'`)
	require.NoError(t, err)

	var failures atomic.Int32
	kafka := &fakeKafka{fail: func(msg *sarama.ProducerMessage) (error, bool) {
		if value(msg) == "0" && failures.Add(1) == 1 {
			return sarama.ErrUnknownTopicOrPartition, false
		}

		return nil, true
	}}

	env.relay(t, "unknown", kafka).Start()
	require.Eventually(t, func() bool { return env.rows(t, "outbox") == 0 }, 10*time.Second, 10*time.Millisecond)

	assert.Equal(t, []int{0, 1}, kafka.byKey()["t/k"])
	assert.Equal(t, 0, env.rows(t, "outbox_failed"))
}

func TestRelay_UnknownTopicGrace(t *testing.T) {
	r := &Relay{opts: RelayOptions{}.withDefaults(Options{}), metrics: noMetrics{}, log: testLog}
	r.held = map[string]hold{}
	now := time.Now()
	unknown := sarama.ErrUnknownTopicOrPartition
	grace := r.opts.UnknownTopicGrace

	r.hold("missing", now.Add(-grace), true)
	r.hold("missing", now.Add(-time.Second), true)
	assert.True(t, r.setAside("missing", unknown, now), "unknown since its first failure")
	assert.False(t, r.setAside("other", unknown, now), "a topic that has not failed yet")

	r.hold("denied", now.Add(-grace), false)
	assert.False(t, r.setAside("denied", unknown, now), "only unknown-topic failures start the grace")

	sent := row{id: 1, msg: msgOf("missing", "k", 0)}
	r.settle([]row{sent}, []error{nil}, map[pair][]row{{topic: "missing", key: "k"}: {sent}}, &outcome{})
	assert.False(t, r.setAside("missing", unknown, now), "a send that succeeds starts it again")
}

func TestRelay_CloseFinishesTheWaveInFlight(t *testing.T) {
	env := testDB(t)
	env.publish(t, msgOf("t", "a", 0), msgOf("t", "a", 1), msgOf("t", "b", 0), msgOf("t", "b", 1))

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	kafka := &fakeKafka{before: func([]*sarama.ProducerMessage) {
		once.Do(func() {
			close(entered)
			<-release
		})
	}}

	r := env.relay(t, "close", kafka)
	r.Start()
	<-entered

	closed := make(chan error, 1)
	start := time.Now()
	go func() { closed <- r.Close() }()
	time.Sleep(100 * time.Millisecond)
	close(release)

	require.NoError(t, <-closed)
	assert.Less(t, time.Since(start), time.Second)
	assert.Equal(t, 2, kafka.count(), "the wave in flight, and no new one")
	assert.Equal(t, 2, env.rows(t, "outbox"), "the acknowledged rows are deleted, the others stay")
	assert.NoError(t, r.Close(), "a second Close")
}

// execHookConnector calls onExec before each Exec (not Query), so that a test can act during a statement.
type execHookConnector struct {
	driver.Connector
	onExec func(query string)
}

func (c execHookConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}

	return execHookConn{Conn: conn, onExec: c.onExec}, nil
}

type execHookConn struct {
	driver.Conn
	onExec func(query string)
}

func (c execHookConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.onExec(query)

	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (c execHookConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func (c execHookConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}

func TestRelay_CloseDuringTheSessionCheckSendsNoMoreWaves(t *testing.T) {
	env := testDB(t)
	env.publish(t, msgOf("t", "k", 0), msgOf("t", "k", 1), msgOf("t", "k", 2))

	kafka := &fakeKafka{}
	var (
		r      *Relay
		once   sync.Once
		closed = make(chan error, 1)
	)
	connector, err := pq.NewConnector(env.url + "&application_name=check")
	require.NoError(t, err)
	db := sql.OpenDB(execHookConnector{Connector: connector, onExec: func(query string) {
		// the session check after the first wave; the one before it finds no call yet
		if query != `SELECT 1` || kafka.sentCalls() != 1 {
			return
		}
		once.Do(func() {
			go func() { closed <- r.Close() }()
			<-r.stop
		})
	}})
	db.SetMaxOpenConns(relayPoolSize)
	t.Cleanup(func() { _ = db.Close() })

	// a session check before every wave, and no keep-alive during one
	r = env.relayOn(t, db, kafka, func(o *RelayOptions) {
		o.SessionCheck = time.Nanosecond
		o.KeepAlive = time.Hour
		o.IdleTimeout = 2 * time.Hour
	})
	r.Start()

	select {
	case err = <-closed:
		require.NoError(t, err, "the round ends without its next wave")
	case <-time.After(10 * time.Second):
		t.Fatal("the round did not reach the session check after its first wave")
	}

	assert.Equal(t, []int{0}, kafka.byKey()["t/k"], "no wave is sent after Close")
	var left []int
	require.NoError(t, env.conn.Select(&left, `SELECT convert_from(payload, 'UTF8')::int FROM outbox ORDER BY id`))
	assert.Equal(t, []int{1, 2}, left, "the rows not sent stay in the outbox")
}

// Another pod must not send the rows ahead of the stuck record: Kafka gets [0 1], never [0 1 0].
func TestRelay_CloseLeavesAStuckSendItsLock(t *testing.T) {
	env := testDB(t)
	env.publish(t, msgOf("t", "k", 0), msgOf("t", "k", 1))

	entered := make(chan struct{})
	release := make(chan struct{})
	var first atomic.Bool
	kafka := &fakeKafka{before: func([]*sarama.ProducerMessage) {
		if first.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
	}}

	stuck := env.relay(t, "stuck", kafka, func(o *RelayOptions) { o.CloseTimeout = 300 * time.Millisecond })
	stuck.Start()
	<-entered

	start := time.Now()
	err := stuck.Close()
	elapsed := time.Since(start)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "keeps the lock")
	assert.GreaterOrEqual(t, elapsed, 300*time.Millisecond)
	assert.Less(t, elapsed, 600*time.Millisecond)

	env.relay(t, "next", kafka).Start()
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, 0, kafka.count(), "no other relay sends while the stuck send may still land")

	close(release)
	require.Eventually(t, func() bool { return env.rows(t, "outbox") == 0 && kafka.sentCalls() >= 2 }, 10*time.Second,
		10*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, []int{0, 1}, kafka.byKey()["t/k"], "the stuck record lands before the next one is sent")
}

func TestRelay_ASlowSendKeepsItsSession(t *testing.T) {
	env := testDB(t)
	env.publish(t, msgOf("t", "k", 0), msgOf("t", "k", 1))

	entered := make(chan struct{})
	var first, slowReturned atomic.Bool
	kafka := &fakeKafka{before: func([]*sarama.ProducerMessage) {
		if first.CompareAndSwap(false, true) {
			close(entered)
			time.Sleep(1500 * time.Millisecond)
			slowReturned.Store(true)
		}
	}}

	env.relay(t, "slow", kafka, func(o *RelayOptions) {
		o.IdleTimeout = 500 * time.Millisecond
		o.KeepAlive = 100 * time.Millisecond
	}).Start()
	<-entered
	env.relay(t, "waiting", kafka).Start()

	require.Eventually(t, func() bool { return slowReturned.Load() && env.rows(t, "outbox") == 0 }, 10*time.Second,
		10*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, []int{0, 1}, kafka.byKey()["t/k"], "the slow record lands before the next one is sent")
}

// The other keys of the busy key's own topic wait while it has more than TopicWindow rows ahead of them.
func TestRelay_ABusyKeyDoesNotHoldTheOthers(t *testing.T) {
	env := testDB(t)

	const burst = 200
	var msgs []Message
	for i := range burst {
		msgs = append(msgs, msgOf("t", "busy", i))
	}
	for i := range 5 {
		msgs = append(msgs, msgOf("t", "quiet", i), msgOf("u", "k", i))
	}
	env.publish(t, msgs...)

	var (
		mu                   sync.Mutex
		busy, quiet, other   int
		busyAtOther, busyAtQ = -1, -1
	)
	kafka := &fakeKafka{latency: time.Millisecond}
	kafka.after = func(sent []*sarama.ProducerMessage) {
		mu.Lock()
		defer mu.Unlock()
		for _, msg := range sent {
			switch {
			case key(msg) == "busy":
				busy++
			case msg.Topic == "t":
				quiet++
			default:
				other++
			}
		}
		if other == 5 && busyAtOther < 0 {
			busyAtOther = busy
		}
		if quiet == 5 && busyAtQ < 0 {
			busyAtQ = busy
		}
	}

	r := env.relay(t, "busy", kafka)
	r.Start()
	require.Eventually(t, func() bool { return env.rows(t, "outbox") == 0 }, 20*time.Second, 10*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	keyRows, window := r.opts.KeyRowsPerRound, r.opts.TopicWindow
	assert.LessOrEqual(t, busyAtOther, keyRows, "the other topic is sent within the first round")
	assert.LessOrEqual(t, busyAtQ, burst-window+2*keyRows,
		"the other key of the busy topic is sent once the busy key is less than TopicWindow rows ahead")
	inOrder(t, kafka.byKey(), []string{"t/busy"}, burst)
	inOrder(t, kafka.byKey(), []string{"t/quiet", "u/k"}, 5)
}

func TestRelay_AnIdleRoundLosesTheLock(t *testing.T) {
	env := testDB(t)
	env.publish(t, msgOf("t", "k", 0), msgOf("t", "k", 1))

	connector, err := pq.NewConnector(env.url + "&application_name=lost")
	require.NoError(t, err)
	stuck := make(chan struct{})
	// the pod is gone: its keep-alive never reaches the database
	db := sql.OpenDB(execHookConnector{Connector: connector, onExec: func(query string) {
		if query == `SELECT 1` {
			<-stuck
		}
	}})
	db.SetMaxOpenConns(relayPoolSize)
	t.Cleanup(func() { _ = db.Close() })
	t.Cleanup(func() { close(stuck) })

	entered := make(chan struct{})
	var once sync.Once
	env.relayOn(t, db, &fakeKafka{before: func([]*sarama.ProducerMessage) {
		once.Do(func() { close(entered) })
		<-stuck
	}}, func(o *RelayOptions) {
		o.IdleTimeout = 500 * time.Millisecond
		o.KeepAlive = 100 * time.Millisecond
		o.CloseTimeout = 100 * time.Millisecond
	}).Start()
	<-entered

	other := &fakeKafka{}
	env.relay(t, "other", other).Start()
	require.Eventually(t, func() bool { return env.rows(t, "outbox") == 0 }, 10*time.Second, 10*time.Millisecond)
	inOrder(t, other.byKey(), []string{"t/k"}, 2)
}

func TestRelay_CloseBeforeStart(t *testing.T) {
	env := testDB(t)
	r := env.relay(t, "never", &fakeKafka{})
	assert.NoError(t, r.Close())
}

func TestRelay_ReportsTheAgeOfTheOldestRow(t *testing.T) {
	env := testDB(t)
	env.publish(t, msgOf("t", "k", 0))
	_, err := env.conn.Exec(`UPDATE outbox SET created_at = now() - interval '90 seconds'`)
	require.NoError(t, err)

	ages := make(chan float64, 10)
	env.withMetrics(t, ageMetrics(ages))
	env.relay(t, "age", &fakeKafka{fail: func(*sarama.ProducerMessage) (error, bool) {
		return sarama.ErrBrokerNotAvailable, false
	}}, func(o *RelayOptions) { o.BacklogInterval = 20 * time.Millisecond }).Start()

	select {
	case age := <-ages:
		assert.InDelta(t, 90, age, 5)
	case <-time.After(5 * time.Second):
		t.Fatal("no age reported")
	}
}

func TestSendErrors(t *testing.T) {
	msg := func(i any) *sarama.ProducerMessage { return &sarama.ProducerMessage{Metadata: i} }
	boom := errors.New("boom")

	assert.Equal(t, []error{nil, nil}, sendErrors(nil, 2))
	assert.Equal(t, []error{nil, boom, nil},
		sendErrors(sarama.ProducerErrors{{Msg: msg(1), Err: boom}}, 3), "only the failed one")
	assert.Equal(t, []error{boom, boom}, sendErrors(boom, 2), "an error that names no message fails all")

	unplaced := sarama.ProducerErrors{{Msg: msg(0), Err: boom}, {Msg: msg("x"), Err: boom}}
	for _, err := range sendErrors(unplaced, 2) {
		assert.Error(t, err, "a message the error cannot place fails all")
	}
}

func TestPermanent(t *testing.T) {
	assert.True(t, permanent(sarama.ErrMessageSizeTooLarge))
	assert.True(t, permanent(sarama.ErrInvalidTopic))
	assert.False(t, permanent(sarama.ErrInvalidMessage), "a corrupt message is tried again, as sarama does")
	assert.True(t, permanent(sarama.ErrInvalidRecord), "rejected by a broker-side validation")
	assert.True(t, permanent(sarama.ErrInvalidTimestamp))
	assert.True(t, permanent(sarama.ConfigurationError(
		"Attempt to produce message larger than configured Producer.MaxMessageBytes: 2 > 1")))
	assert.False(t, permanent(sarama.ErrUnknownTopicOrPartition), "unknown topics have a grace")
	assert.False(t, permanent(sarama.ErrTopicAuthorizationFailed))
	assert.False(t, permanent(sarama.ErrNotLeaderForPartition))
	assert.False(t, permanent(errors.New("eof")))
	// a topic whose in-sync set dips, and the errors after which sarama bumps the producer epoch, wait and go again
	for _, err := range []error{
		sarama.ErrNotEnoughReplicas, sarama.ErrNotEnoughReplicasAfterAppend, sarama.ErrOutOfOrderSequenceNumber,
		sarama.ErrInvalidProducerEpoch, sarama.ErrUnknownProducerID,
	} {
		assert.False(t, permanent(err), "%v", err)
	}
}

type ageMetrics chan float64

func (ageMetrics) Sent(string)       {}
func (ageMetrics) SendFailed(string) {}
func (ageMetrics) SetAside(string)   {}
func (ageMetrics) WeakTopics(int)    {}

func (m ageMetrics) OldestRowAge(seconds float64) {
	select {
	case m <- seconds:
	default:
	}
}

func TestCheck_FailsWithoutTheTables(t *testing.T) {
	ctx := context.Background()
	for _, table := range []string{"outbox", "outbox_failed"} {
		env := testDB(t)
		require.NoError(t, env.ob.Check(ctx))

		_, err := env.conn.Exec(`DROP TABLE ` + table)
		require.NoError(t, err)
		err = env.ob.Check(ctx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "table "+table+" does not exist")
		assert.Contains(t, err.Error(), "migration is not applied")
	}
}
