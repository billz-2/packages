package outbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/IBM/sarama"
)

// Producer returns once every record is acknowledged or failed: nil, or a sarama.ProducerErrors of the failed
// ones, holding the messages it was given (the relay finds them by their Metadata). A sarama.SyncProducer is one.
type Producer interface {
	SendMessages(msgs []*sarama.ProducerMessage) error
}

// RelayDB is the relay's own pool: *sql.DB and *sqlx.DB satisfy it. A round is one *sql.Tx, which pins one
// connection, so the transaction-scoped lock, the keep-alive and the session check all run on the session that
// holds the lock. Give it at least 2 connections, one for the round and one for the backlog gauge.
type RelayDB interface {
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// RelayOptions tune the relay. Their zero values are the defaults.
type RelayOptions struct {
	// LockKey is the advisory lock that lets one round run at a time. Every relay of a table must use the same
	// key, also the pods of an older version during a deploy. Default: derived from Service and Table.
	LockKey         int64
	ApplicationName string // the round's session name; default Service + "-outbox-relay"

	BatchSize int // 100
	// KeyRowsPerRound caps the rows of a key in a round: they go one per round trip, so a burst on one key
	// would hold the rest of the batch for up to BatchSize round trips. Default 20.
	KeyRowsPerRound int
	// TopicWindow bounds the rows a read looks at per topic. While one key has more rows than this ahead of
	// the other keys of its topic, they go at its pace. Default 100.
	TopicWindow int

	PollInterval   time.Duration // 200ms
	MinResendDelay time.Duration // 1s: the pause of a topic after a failure, doubling while it fails
	MaxResendDelay time.Duration // 30s
	// UnknownTopicGrace: the brokers report an unknown topic also while they restart or move partitions, and
	// auto-create may add it a moment later. It counts from the topic's first failure in a row. Default 2m.
	UnknownTopicGrace time.Duration
	// IdleTimeout ends the session of a lost pod, so that its lock does not wait for TCP keepalive. Default 60s.
	IdleTimeout time.Duration
	// KeepAlive keeps a slow send's session within IdleTimeout: sarama can take minutes on a record (30 s
	// network timeouts, 3 retries, metadata refreshes). Default 10s.
	KeepAlive time.Duration
	// SessionCheck: before its next wave a round checks that its session, and so the lock, is still there; if
	// it was ended (a failover, a network partition), another pod may already send the same rows. Default 1s.
	SessionCheck time.Duration
	// CloseTimeout bounds Close. A round still sending then keeps its lock until its send ends or the process
	// exits. Default DefaultCloseTimeout.
	CloseTimeout    time.Duration
	BacklogInterval time.Duration // 15s: how often every relay reads the age of the oldest row

	// Permanent sets aside more errors, on top of the ones the relay knows cannot be fixed by sending again.
	Permanent func(error) bool
}

const DefaultCloseTimeout = 5 * time.Second

const (
	defaultBatchSize         = 100
	defaultKeyRowsPerRound   = 20
	defaultTopicWindow       = 100
	defaultPollInterval      = 200 * time.Millisecond
	defaultMinResendDelay    = time.Second
	defaultMaxResendDelay    = 30 * time.Second
	defaultUnknownTopicGrace = 2 * time.Minute
	defaultIdleTimeout       = 60 * time.Second
	defaultKeepAlive         = 10 * time.Second
	defaultSessionCheck      = time.Second
	defaultBacklogInterval   = 15 * time.Second

	resendBackoff       = 2
	backlogQueryTimeout = 5 * time.Second
)

// lockKey is the fnv-1a hash of name shifted right by one, as int64(h.Sum64() >> 1), built from the hash's bytes
// so that no uint64 is converted.
func lockKey(name string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(name))
	const bitsPerByte = 8
	var k int64
	for _, b := range h.Sum(nil) {
		k = k<<bitsPerByte | int64(b)
	}

	return (k >> 1) & math.MaxInt64
}

func (o RelayOptions) withDefaults(ob Options) RelayOptions {
	if o.LockKey == 0 {
		o.LockKey = lockKey("outbox:" + ob.Service + ":" + ob.Table)
	}
	if o.ApplicationName == "" {
		o.ApplicationName = ob.Service + "-outbox-relay"
	}
	setDefault(&o.BatchSize, defaultBatchSize)
	setDefault(&o.KeyRowsPerRound, defaultKeyRowsPerRound)
	setDefault(&o.TopicWindow, defaultTopicWindow)
	setDefault(&o.PollInterval, defaultPollInterval)
	setDefault(&o.MinResendDelay, defaultMinResendDelay)
	setDefault(&o.MaxResendDelay, defaultMaxResendDelay)
	setDefault(&o.UnknownTopicGrace, defaultUnknownTopicGrace)
	setDefault(&o.IdleTimeout, defaultIdleTimeout)
	setDefault(&o.KeepAlive, defaultKeepAlive)
	setDefault(&o.SessionCheck, defaultSessionCheck)
	setDefault(&o.CloseTimeout, DefaultCloseTimeout)
	setDefault(&o.BacklogInterval, defaultBacklogInterval)

	return o
}

func setDefault[T int | time.Duration](v *T, def T) {
	if *v == 0 {
		*v = def
	}
}

func (o RelayOptions) check() error {
	for _, n := range []int{o.BatchSize, o.KeyRowsPerRound, o.TopicWindow} {
		if n < 0 {
			return errors.New("outbox relay: a negative size in RelayOptions")
		}
	}
	for _, d := range []time.Duration{o.PollInterval, o.MinResendDelay, o.MaxResendDelay, o.UnknownTopicGrace,
		o.IdleTimeout, o.KeepAlive, o.SessionCheck, o.CloseTimeout, o.BacklogInterval} {
		if d < 0 {
			return errors.New("outbox relay: a negative duration in RelayOptions")
		}
	}
	// a slow send's session would end between two keep-alives, and with it the lock while sarama may still deliver
	if o.IdleTimeout < time.Millisecond || 2*o.KeepAlive > o.IdleTimeout {
		return fmt.Errorf("outbox relay: IdleTimeout %s: want 1ms or more and at least twice KeepAlive %s",
			o.IdleTimeout, o.KeepAlive)
	}
	// PostgreSQL cuts a longer name and replaces what is not ASCII: the runbook would not find the session
	if len(o.ApplicationName) > maxIdentifier || strings.IndexFunc(o.ApplicationName, func(r rune) bool {
		return r < ' ' || r > '~'
	}) >= 0 {
		return fmt.Errorf("outbox relay: ApplicationName %q: want printable ASCII of at most %d bytes",
			o.ApplicationName, maxIdentifier)
	}

	return nil
}

// pair is the unit of order: the records of one topic and key are sent one after the other.
type pair struct {
	topic string
	key   string
}

type hold struct {
	until        time.Time
	delay        time.Duration
	last         time.Time
	unknownSince time.Time
}

type row struct {
	id  int64
	msg Message
}

type outcome struct {
	acked      []int64
	failed     []int64
	failErrors []string
	failTopics []string
	retry      bool
}

func (o *outcome) setAside(r row, err error) {
	o.failed = append(o.failed, r.id)
	o.failErrors = append(o.failErrors, err.Error())
	o.failTopics = append(o.failTopics, r.msg.Topic)
}

// Relay sends the outbox to Kafka. Every pod runs one; a round holds the advisory lock LockKey, so one round
// at a time sends and the rows go in id order whichever pod sends them.
//
// Order: a round sends in waves, the next row of every key at once. The next row of a key is sent only after
// Kafka acknowledged the one before. When a row fails, the rows of its topic after it are not sent in that
// round, so a record never overtakes an earlier one of its key.
//
// The lock is held until the round's sends have ended; only a session ended under the round (a failover, a
// network partition) frees it during a send.
//
// At least once: a row is deleted only after Kafka acknowledged it, in the round's transaction.
//
// Each round forces custom plans for its own transaction (plan_cache_mode, PostgreSQL 12 or later).
type Relay struct {
	db       RelayDB
	producer Producer
	log      Logger
	metrics  Metrics
	opts     RelayOptions

	failedTable string
	idleTimeout string
	readQuery   string
	deleteQuery string
	asideQuery  string
	ageQuery    string

	// used by the run goroutine only
	held map[string]hold

	stop     chan struct{}
	stopOnce sync.Once
	started  atomic.Bool
	done     chan struct{}
}

func (o *Outbox) Relay(db RelayDB, p Producer, opts RelayOptions) (*Relay, error) {
	if db == nil || p == nil {
		return nil, errors.New("outbox relay: a database and a producer are required")
	}

	opts = opts.withDefaults(o.opts)
	if err := opts.check(); err != nil {
		return nil, err
	}

	tables := strings.NewReplacer("{{table}}", quoteIdent(o.opts.Table), "{{failed}}", quoteIdent(o.opts.FailedTable))

	return &Relay{
		db:          db,
		producer:    p,
		log:         o.log,
		metrics:     o.metrics,
		opts:        opts,
		failedTable: o.opts.FailedTable,
		idleTimeout: strconv.FormatInt(opts.IdleTimeout.Milliseconds(), 10) + "ms",
		readQuery:   tables.Replace(readQuery),
		deleteQuery: tables.Replace(deleteQuery),
		asideQuery:  tables.Replace(setAsideQuery),
		ageQuery:    tables.Replace(oldestRowAgeQuery),
		held:        make(map[string]hold),
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
	}, nil
}

func (r *Relay) Start() {
	if !r.started.CompareAndSwap(false, true) {
		return
	}

	go r.watchBacklog()
	go r.run()
}

// Close stops the relay. A round still sending after CloseTimeout is left open with its lock, and Close
// returns an error: a rollback would let another pod send the rows while sarama may still deliver them.
func (r *Relay) Close() error {
	r.stopOnce.Do(func() { close(r.stop) })
	if !r.started.Load() {
		return nil
	}

	timer := time.NewTimer(r.opts.CloseTimeout)
	defer timer.Stop()

	select {
	case <-r.done:
		return nil
	case <-timer.C:
		return errors.New("outbox relay: a send was still running at the close timeout: its round keeps the " +
			"lock until the send ends or the process exits, and the rows it did not commit are sent again")
	}
}

func (r *Relay) stopping() bool {
	select {
	case <-r.stop:
		return true
	default:
		return false
	}
}

func (r *Relay) run() {
	defer close(r.done)

	for !r.stopping() {
		out, err := r.round()
		if err != nil {
			r.log.Error("outbox relay: the round failed, its rows are sent again", "error", err)
		}

		pause := r.opts.PollInterval
		switch {
		case err == nil && len(out.acked)+len(out.failed) > 0:
			continue
		case err != nil || out.retry:
			// do not spin
			pause = r.opts.MinResendDelay
		}

		select {
		case <-r.stop:
			return
		case <-time.After(pause):
		}
	}
}

// lockQuery: set_config runs in the FROM clause, before the target list, so the session is named before the
// lock is taken; all four settings end with the transaction, so none leaks through a pooler.
// No JIT: it takes longer to compile the read than the read takes to run.
const lockQuery = `SELECT pg_try_advisory_xact_lock($1)
	FROM set_config('idle_in_transaction_session_timeout', $2, true) idle,
		set_config('jit', 'off', true) jit,
		set_config('plan_cache_mode', 'force_custom_plan', true) plan,
		set_config('application_name', $3, true) app`

func (r *Relay) round() (outcome, error) {
	var out outcome
	ctx := context.Background()

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()

	var locked bool
	err = tx.QueryRowContext(ctx, lockQuery, r.opts.LockKey, r.idleTimeout, r.opts.ApplicationName).Scan(&locked)
	if err != nil || !locked {
		return out, err
	}

	rows, err := r.read(tx, &out)
	if err != nil {
		return out, err
	}

	r.send(tx, rows, &out)

	if err = r.finish(tx, out); err != nil {
		return out, err
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}

	for i, id := range out.failed {
		r.metrics.SetAside(out.failTopics[i])
		r.log.Error("outbox relay: a record can never be sent, set aside in "+r.failedTable,
			"id", id, "topic", out.failTopics[i], "error", out.failErrors[i])
	}

	return out, nil
}

// readQuery bounds the topic by a row comparison, not an =, which keeps the planner on the (topic, id)
// index: with an = it may scan the primary key and read every row ahead. It is fast only as a custom plan
// (lockQuery forces one).
const readQuery = `WITH RECURSIVE topics(topic) AS (
	(SELECT topic FROM {{table}} ORDER BY topic LIMIT 1)
	UNION ALL
	SELECT (SELECT o.topic FROM {{table}} o WHERE o.topic > t.topic ORDER BY o.topic LIMIT 1)
	FROM topics t WHERE t.topic IS NOT NULL
), next AS (
	SELECT w.id, row_number() OVER (PARTITION BY w.topic, w.key ORDER BY w.id) AS nth
	FROM topics t CROSS JOIN LATERAL (
		SELECT o.id, o.topic, o.key FROM {{table}} o
		WHERE (o.topic, o.id) > (t.topic, 0) AND o.topic <= t.topic ORDER BY o.topic, o.id LIMIT $2
	) w
	WHERE t.topic IS NOT NULL AND t.topic <> ALL($1::text[])
)
SELECT o.id, o.topic, o.key, o.payload, o.headers
FROM {{table}} o JOIN (SELECT id FROM next WHERE nth <= $3 ORDER BY id LIMIT $4) n ON n.id = o.id
ORDER BY o.id`

func (r *Relay) read(tx *sql.Tx, out *outcome) ([]row, error) {
	result, err := tx.QueryContext(context.Background(), r.readQuery,
		textArray(r.heldTopics(time.Now())), r.opts.TopicWindow, r.opts.KeyRowsPerRound, r.opts.BatchSize)
	if err != nil {
		return nil, err
	}
	defer result.Close()

	var rows []row
	for result.Next() {
		var (
			ro      row
			headers []byte
		)
		if err = result.Scan(&ro.id, &ro.msg.Topic, &ro.msg.Key, &ro.msg.Value, &headers); err != nil {
			return nil, err
		}

		if err = json.Unmarshal(headers, &ro.msg.Headers); err != nil {
			out.setAside(ro, fmt.Errorf("outbox row headers: %w", err))

			continue
		}
		rows = append(rows, ro)
	}

	return rows, result.Err()
}

func (r *Relay) send(tx *sql.Tx, rows []row, out *outcome) {
	queues := make(map[pair][]row)
	var order []pair
	for _, ro := range rows {
		p := pair{topic: ro.msg.Topic, key: ro.msg.Key}
		if _, ok := queues[p]; !ok {
			order = append(order, p)
		}
		queues[p] = append(queues[p], ro)
	}

	checked := time.Now()
	for !r.stopping() && r.sessionAlive(tx, &checked) {
		var wave []row
		for _, p := range order {
			if queue := queues[p]; len(queue) > 0 {
				wave = append(wave, queue[0])
			}
		}
		if len(wave) == 0 {
			return
		}

		msgs := make([]*sarama.ProducerMessage, len(wave))
		for i, ro := range wave {
			msgs[i] = ro.msg.ProducerMessage()
			msgs[i].Metadata = i
		}
		// again: once Close gave up, the producer may be closed
		if r.stopping() {
			return
		}
		r.settle(wave, sendErrors(r.produce(tx, msgs), len(msgs)), queues, out)
	}
}

// produce keeps the round's session alive while Kafka has the wave, so that the idle timeout does not free
// the lock while sarama may still deliver the records.
func (r *Relay) produce(tx *sql.Tx, msgs []*sarama.ProducerMessage) error {
	sent := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)

		ticker := time.NewTicker(r.opts.KeepAlive)
		defer ticker.Stop()
		for {
			select {
			case <-sent:
				return
			case <-ticker.C:
				if _, err := tx.ExecContext(context.Background(), `SELECT 1`); err != nil {
					return
				}
			}
		}
	}()

	err := r.producer.SendMessages(msgs)
	close(sent)
	<-stopped

	return err
}

func (r *Relay) sessionAlive(tx *sql.Tx, checked *time.Time) bool {
	if time.Since(*checked) < r.opts.SessionCheck {
		return true
	}
	if _, err := tx.ExecContext(context.Background(), `SELECT 1`); err != nil {
		r.log.Error("outbox relay: the round lost its database session, it stops", "error", err)

		return false
	}
	*checked = time.Now()

	return true
}

func (r *Relay) settle(wave []row, errs []error, queues map[pair][]row, out *outcome) {
	now := time.Now()
	failed := make(map[string]bool) // topic → one of its failures was an unknown topic
	sent := make(map[string]bool)

	for i, ro := range wave {
		p := pair{topic: ro.msg.Topic, key: ro.msg.Key}
		err := errs[i]

		switch {
		case err == nil:
			r.metrics.Sent(p.topic)
			out.acked = append(out.acked, ro.id)
			sent[p.topic] = true
		case r.setAside(p.topic, err, now):
			r.metrics.SendFailed(p.topic)
			out.setAside(ro, err)
		default:
			// the rows after this one must not go before it: its topic waits with it
			r.metrics.SendFailed(p.topic)
			failed[p.topic] = failed[p.topic] || errors.Is(err, sarama.ErrUnknownTopicOrPartition)
			r.log.Error("outbox relay: a record was not sent, its topic waits and tries again",
				"error", err, "id", ro.id, "topic", p.topic, "key", p.key)

			continue
		}
		queues[p] = queues[p][1:]
	}

	for topic := range sent {
		if _, ok := failed[topic]; !ok {
			delete(r.held, topic)
		}
	}
	for topic, unknown := range failed {
		for p := range queues {
			if p.topic == topic {
				delete(queues, p)
			}
		}
		r.hold(topic, now, unknown)
		out.retry = true
	}
}

func (r *Relay) setAside(topic string, err error, now time.Time) bool {
	if permanent(err) || (r.opts.Permanent != nil && r.opts.Permanent(err)) {
		return true
	}
	if !errors.Is(err, sarama.ErrUnknownTopicOrPartition) {
		return false
	}

	h, ok := r.held[topic]
	if !ok || h.unknownSince.IsZero() || now.Sub(h.unknownSince) < r.opts.UnknownTopicGrace {
		return false
	}
	h.last = now
	r.held[topic] = h

	return true
}

// sendErrors fails every message when the error names none, or one it cannot place: a message is taken as
// sent only when the producer says so.
func sendErrors(err error, n int) []error {
	errs := make([]error, n)
	if err == nil {
		return errs
	}

	var perrs sarama.ProducerErrors
	if errors.As(err, &perrs) {
		placed := true
		for _, perr := range perrs {
			i, ok := -1, false
			if perr != nil && perr.Msg != nil {
				i, ok = perr.Msg.Metadata.(int)
			}
			if !ok || i < 0 || i >= n {
				placed = false

				break
			}
			errs[i] = perr.Err
			if errs[i] == nil {
				errs[i] = err
			}
		}
		if placed {
			return errs
		}
	}

	for i := range errs {
		errs[i] = err
	}

	return errs
}

func (r *Relay) hold(topic string, now time.Time, unknown bool) {
	h := r.held[topic]
	h.delay = min(max(resendBackoff*h.delay, r.opts.MinResendDelay), r.opts.MaxResendDelay)
	h.until = now.Add(h.delay)
	h.last = now
	if unknown && h.unknownSince.IsZero() {
		h.unknownSince = now
	}
	r.held[topic] = h
}

// heldTopics also forgets a topic quiet for a while: its next failure starts the back-off and the grace again.
func (r *Relay) heldTopics(now time.Time) []string {
	topics := []string{}
	for topic, h := range r.held {
		switch {
		case now.After(h.last.Add(h.delay + r.opts.MaxResendDelay)):
			delete(r.held, topic)
		case now.Before(h.until):
			topics = append(topics, topic)
		}
	}

	return topics
}

const deleteQuery = `DELETE FROM {{table}} WHERE id = ANY($1::bigint[])`

const setAsideQuery = `WITH moved AS (DELETE FROM {{table}} WHERE id = ANY($1::bigint[]) RETURNING *)
INSERT INTO {{failed}} (id, topic, key, payload, headers, created_at, error)
SELECT m.id, m.topic, m.key, m.payload, m.headers, m.created_at, e.error
FROM moved m JOIN unnest($1::bigint[], $2::text[]) AS e(id, error) ON e.id = m.id`

func (r *Relay) finish(tx *sql.Tx, out outcome) error {
	ctx := context.Background()
	if len(out.acked) > 0 {
		if _, err := tx.ExecContext(ctx, r.deleteQuery, int64Array(out.acked)); err != nil {
			return err
		}
	}
	if len(out.failed) > 0 {
		if _, err := tx.ExecContext(ctx, r.asideQuery, int64Array(out.failed), textArray(out.failErrors)); err != nil {
			return err
		}
	}

	return nil
}

// permanent reports an error that sending again cannot fix. The others are tried again, a missing ACL during
// a rollout too, and an unknown topic for UnknownTopicGrace. INVALID_RECORD is what a broker-side schema
// validation answers; sarama retries a corrupt message itself.
func permanent(err error) bool {
	var configErr sarama.ConfigurationError
	if errors.As(err, &configErr) && strings.Contains(string(configErr), "larger than") {
		// a value over Producer.MaxMessageBytes
		return true
	}

	return errors.Is(err, sarama.ErrMessageSizeTooLarge) || errors.Is(err, sarama.ErrInvalidTopic) ||
		errors.Is(err, sarama.ErrInvalidRecord) || errors.Is(err, sarama.ErrInvalidTimestamp)
}

// watchBacklog runs on every relay, not only the one with the lock, so the gauge is there while one pod runs.
func (r *Relay) watchBacklog() {
	ticker := time.NewTicker(r.opts.BacklogInterval)
	defer ticker.Stop()

	for {
		age, err := r.oldestRowAge()
		if err != nil {
			r.log.Error("outbox relay: could not read the age of the oldest row", "error", err)
		} else {
			r.metrics.OldestRowAge(age)
		}

		select {
		case <-r.stop:
			return
		case <-ticker.C:
		}
	}
}

const oldestRowAgeQuery = `SELECT COALESCE(
	(SELECT EXTRACT(EPOCH FROM now() - created_at) FROM {{table}} ORDER BY id LIMIT 1), 0)`

func (r *Relay) oldestRowAge() (float64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), backlogQueryTimeout)
	defer cancel()

	var age float64
	err := r.db.QueryRowContext(ctx, r.ageQuery).Scan(&age)

	return age, err
}
