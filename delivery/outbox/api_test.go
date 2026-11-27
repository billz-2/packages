package outbox //nolint:testpackage // checks the defaults and the queries of the relay

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/IBM/sarama"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew_ChecksItsOptions(t *testing.T) {
	db := &sqlx.DB{}
	_, err := New(db, Options{})
	require.Error(t, err, "no service")
	_, err = New(nil, Options{Service: "s"})
	require.Error(t, err, "no database")
	_, err = New(db, Options{Service: "s", Table: "Outbox"})
	require.Error(t, err, "a name that needs quoting")

	ob, err := New(db, Options{Service: "s"})
	require.NoError(t, err)
	_, err = ob.Relay(db, nil, RelayOptions{})
	require.Error(t, err, "no producer")
	_, err = ob.Relay(db, &fakeKafka{}, RelayOptions{ApplicationName: strings.Repeat("a", 64)})
	require.Error(t, err, "PostgreSQL would cut the name")
	_, err = ob.Relay(db, &fakeKafka{}, RelayOptions{ApplicationName: "relé"})
	require.Error(t, err, "PostgreSQL would replace the é")
	_, err = ob.Relay(db, &fakeKafka{}, RelayOptions{BatchSize: -1})
	require.Error(t, err)

	for _, o := range []RelayOptions{
		{IdleTimeout: 5 * time.Second},
		{IdleTimeout: time.Second, KeepAlive: 600 * time.Millisecond},
		{IdleTimeout: time.Microsecond, KeepAlive: time.Nanosecond},
	} {
		_, err = ob.Relay(db, &fakeKafka{}, o)
		require.Error(t, err, "the keep-alive cannot hold the session: %+v", o)
	}
	_, err = ob.Relay(db, &fakeKafka{}, RelayOptions{IdleTimeout: time.Second, KeepAlive: 500 * time.Millisecond})
	require.NoError(t, err)
}

func TestRelayOptions_Defaults(t *testing.T) {
	o := RelayOptions{}.withDefaults(Options{Service: "svc"}.withDefaults())
	assert.Equal(t, "svc-outbox-relay", o.ApplicationName)
	assert.Equal(t, 100, o.BatchSize)
	assert.Equal(t, 20, o.KeyRowsPerRound)
	assert.Equal(t, 100, o.TopicWindow)
	assert.Equal(t, 200*time.Millisecond, o.PollInterval)
	assert.Equal(t, 60*time.Second, o.IdleTimeout)
	assert.Equal(t, DefaultCloseTimeout, o.CloseTimeout)
	assert.NotZero(t, o.LockKey)

	other := RelayOptions{}.withDefaults(Options{Service: "svc", Table: "outbox_x"}.withDefaults())
	assert.NotEqual(t, o.LockKey, other.LockKey, "each table has its own lock")
	assert.Equal(t, int64(7), RelayOptions{LockKey: 7}.withDefaults(Options{}).LockKey)
}

// The lock key must stay the value the relays of every deployed version take: int64(fnv-1a >> 1).
func TestLockKey_IsTheHashShiftedRightByOne(t *testing.T) {
	source := rand.NewChaCha8([32]byte{1})
	random := rand.New(source)
	names := []string{"", "outbox:billz_inventory_service_v2:outbox"}
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

func TestTopic_EveryTopicIsOrderedByDefault(t *testing.T) {
	ob, err := New(&sqlx.DB{}, Options{Service: "s"})
	require.NoError(t, err)

	ob.Topic("ordered", Ordered())
	ob.Topic("plain")
	assert.True(t, ob.ordered("ordered"))
	assert.True(t, ob.ordered("never.registered"), "the safe default")
	assert.True(t, ob.ordered("plain"), "registered without options is the same as not registered")
}

func TestArrayLiteral(t *testing.T) {
	env := testDB(t)

	texts := []string{"", "a", `quo"te`, `back\slash`, `\"`, "comma,brace}{", "NULL", " spaced ", "new\nline", "ю"}
	var gotTexts []string
	require.NoError(t, env.conn.QueryRow(`SELECT $1::text[]`, textArray(texts)).Scan(pq.Array(&gotTexts)))
	assert.Equal(t, texts, gotTexts)

	var empty []string
	require.NoError(t, env.conn.QueryRow(`SELECT $1::text[]`, textArray(nil)).Scan(pq.Array(&empty)))
	assert.Empty(t, empty)

	ids := []int64{1, -2, 9223372036854775807}
	var gotIDs []int64
	require.NoError(t, env.conn.QueryRow(`SELECT $1::bigint[]`, int64Array(ids)).Scan(pq.Array(&gotIDs)))
	assert.Equal(t, ids, gotIDs)
}

func TestRelay_UsesTheTablesOfItsOptions(t *testing.T) {
	env := testDBWith(t, Options{Table: "outbox_x", FailedTable: "outbox_x_failed"})
	env.publish(t, msgOf("t", "k", 0), Message{Topic: "t", Key: "k", Value: []byte("too large")}, msgOf("t", "k", 2))

	kafka := &fakeKafka{fail: func(msg *sarama.ProducerMessage) (error, bool) {
		if value(msg) == "too large" {
			return sarama.ErrMessageSizeTooLarge, false
		}

		return nil, true
	}}
	ages := make(chan float64, 10)
	env.withMetrics(t, ageMetrics(ages))
	env.relay(t, "tables", kafka, func(o *RelayOptions) { o.BacklogInterval = 20 * time.Millisecond }).Start()

	require.Eventually(t, func() bool { return env.rows(t, "outbox_x") == 0 }, 10*time.Second, 10*time.Millisecond)
	assert.Equal(t, []int{0, 2}, kafka.byKey()["t/k"])
	assert.Equal(t, 1, env.rows(t, "outbox_x_failed"))
	select {
	case <-ages:
	case <-time.After(5 * time.Second):
		t.Fatal("no age read from outbox_x")
	}

	var tables int
	require.NoError(t, env.conn.Get(&tables, `SELECT count(*) FROM pg_tables WHERE schemaname = $1`, env.schema))
	assert.Equal(t, 2, tables, "no table of the default names")
}

// The runbook finds a round stuck in a send by this name, also when the pool's DSN names its sessions otherwise.
func TestRelay_NamesItsSessionPerRound(t *testing.T) {
	env := testDB(t)
	env.publish(t, msgOf("t", "k", 0))

	entered := make(chan struct{})
	release := make(chan struct{})
	kafka := &fakeKafka{before: func([]*sarama.ProducerMessage) {
		close(entered)
		<-release
	}}
	env.relayOn(t, env.pool(t, "pool-name").DB, kafka, func(o *RelayOptions) {
		o.ApplicationName = "round-name"
	}).Start()
	<-entered

	sessions := func(name string) int {
		var n int
		require.NoError(t, env.admin.Get(&n, `SELECT count(*) FROM pg_stat_activity
			WHERE application_name = $1 AND state = 'idle in transaction'`, name))

		return n
	}
	assert.Equal(t, 1, sessions("round-name"), "the round's session carries the name of the options")
	assert.Equal(t, 0, sessions("pool-name"))

	close(release)
	require.Eventually(t, func() bool { return env.rows(t, "outbox") == 0 }, 10*time.Second, 10*time.Millisecond)
	var names []string
	require.NoError(t, env.admin.Select(&names, `SELECT application_name FROM pg_stat_activity
		WHERE application_name IN ('round-name', 'pool-name') AND state = 'idle'`))
	assert.NotContains(t, names, "round-name", "the name ends with the round")
}

// A statement cache can run the read as a generic plan, which scans the whole table. The session's
// force_generic_plan stands for that cache here; the round's own setting must win over it.
func TestRelayRead_UsesTheIndexAsAPreparedStatement(t *testing.T) {
	env := testDB(t)
	_, err := env.conn.Exec(`INSERT INTO outbox (topic, key, payload)
		SELECT 'topic_' || (i % 10), 'k' || (i % 200), '\x01' FROM generate_series(1, 50000) i`)
	require.NoError(t, err)
	_, err = env.conn.Exec(`ANALYZE outbox`)
	require.NoError(t, err)

	db, err := sqlx.Connect("postgres", env.url+"&plan_cache_mode=force_generic_plan")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	r := env.relayOn(t, db.DB, &fakeKafka{})

	scans := func(round bool) []string {
		ctx := context.Background()
		tx, beginErr := r.db.BeginTx(ctx, nil)
		require.NoError(t, beginErr)
		defer func() { _ = tx.Rollback() }()

		var mode string
		require.NoError(t, tx.QueryRowContext(ctx, `SHOW plan_cache_mode`).Scan(&mode))
		require.Equal(t, "force_generic_plan", mode, "the session of the test pool")
		if round {
			var locked bool
			require.NoError(t, tx.QueryRowContext(ctx, lockQuery, r.opts.LockKey, r.idleTimeout,
				r.opts.ApplicationName).Scan(&locked))
			require.True(t, locked)
		}

		_, prepErr := tx.ExecContext(ctx, `PREPARE relay_read(text[], int, int, int) AS `+r.readQuery)
		require.NoError(t, prepErr)
		defer func() { _, _ = tx.ExecContext(ctx, `DEALLOCATE relay_read`) }()
		args := `('{}', ` + itoa(r.opts.TopicWindow) + `, ` + itoa(r.opts.KeyRowsPerRound) + `, ` +
			itoa(r.opts.BatchSize) + `)`
		for range 8 {
			_, prepErr = tx.ExecContext(ctx, `EXECUTE relay_read`+args)
			require.NoError(t, prepErr)
		}

		var plan string
		require.NoError(t, tx.QueryRowContext(ctx, `EXPLAIN (FORMAT JSON) EXECUTE relay_read`+args).Scan(&plan))

		return planScans(t, plan)
	}

	assert.Contains(t, scans(false), "Seq Scan outbox", "control: the generic plan scans the table")
	got := scans(true)
	assert.NotContains(t, got, "Seq Scan outbox")
	assert.Contains(t, got, "Index Scan outbox_topic_id")
}

func itoa(n int) string {
	raw, _ := json.Marshal(n)

	return string(raw)
}

// planScans lists the scans of an EXPLAIN (FORMAT JSON) as "<node type> <relation or index>".
func planScans(t *testing.T, plan string) []string {
	t.Helper()

	type node struct {
		Type     string `json:"Node Type"`
		Relation string `json:"Relation Name"`
		Index    string `json:"Index Name"`
		Plans    []node `json:"Plans"`
	}
	var explained []struct {
		Plan node `json:"Plan"`
	}
	require.NoError(t, json.Unmarshal([]byte(plan), &explained))
	require.Len(t, explained, 1)

	var scans []string
	var walk func(n node)
	walk = func(n node) {
		switch {
		case n.Index != "":
			scans = append(scans, n.Type+" "+n.Index)
		case n.Relation != "":
			scans = append(scans, n.Type+" "+n.Relation)
		}
		for _, child := range n.Plans {
			walk(child)
		}
	}
	walk(explained[0].Plan)

	return scans
}

type sqlStateError string

func (e sqlStateError) Error() string    { return "SQLSTATE " + string(e) }
func (e sqlStateError) SQLState() string { return string(e) }

type failingQuerier struct{ err error }

func (q failingQuerier) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return nil, q.err
}

func (q failingQuerier) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, q.err
}

// Any driver whose error has SQLState() (lib/pq, pgx's PgError) gets the missing migration named.
func TestCheck_SQLStateWithoutPq(t *testing.T) {
	ob, err := New(failingQuerier{err: fmt.Errorf("wrapped: %w", sqlStateError("42P01"))}, Options{Service: "s"})
	require.NoError(t, err)
	err = ob.Check(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "table outbox does not exist, its migration is not applied")

	ob, err = New(failingQuerier{err: sqlStateError("28P01")}, Options{Service: "s"})
	require.NoError(t, err)
	err = ob.Check(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "check of table outbox")
}

func TestCheck_UsesTheTablesOfItsOptions(t *testing.T) {
	env := testDBWith(t, Options{Table: "outbox_x", FailedTable: "outbox_x_failed"})
	require.NoError(t, env.ob.Check(context.Background()))
}
