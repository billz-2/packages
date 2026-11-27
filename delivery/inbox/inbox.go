package inbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/IBM/sarama"
)

type Header struct {
	Key   string `json:"k"`
	Value []byte `json:"v"`
}

type Message struct {
	Topic     string
	Partition int32
	Offset    int64
	Key       []byte
	Value     []byte
	Headers   []Header
	// ID is the ce_id header, or topic/partition/offset for a message without one. A ce_id the table cannot
	// hold (not UTF-8, a NUL, longer than maxIDBytes) is replaced by its sha256.
	ID string
}

// maxIDBytes keeps the primary key far below the btree's limit of about 2.7 kB.
const maxIDBytes = 512

func FromKafka(km *sarama.ConsumerMessage) *Message {
	m := &Message{Topic: km.Topic, Partition: km.Partition, Offset: km.Offset, Key: km.Key, Value: km.Value}
	for _, h := range km.Headers {
		if h == nil {
			continue
		}
		m.Headers = append(m.Headers, Header{Key: string(h.Key), Value: h.Value})
		if string(h.Key) == "ce_id" {
			m.ID = storableID(h.Value)
		}
	}
	if m.ID == "" {
		m.ID = m.Topic + "/" + strconv.FormatInt(int64(m.Partition), 10) + "/" + strconv.FormatInt(m.Offset, 10)
	}

	return m
}

func storableID(id []byte) string {
	if len(id) <= maxIDBytes && utf8.Valid(id) && !bytes.ContainsRune(id, 0) {
		return string(id)
	}
	sum := sha256.Sum256(id)

	return "sha256:" + hex.EncodeToString(sum[:])
}

func (m *Message) Header(key string) ([]byte, bool) {
	for _, h := range m.Headers {
		if h.Key == key {
			return h.Value, true
		}
	}

	return nil, false
}

// digestBytes: a reused ce_id with another payload is a different message, a redelivery is byte-identical.
const digestBytes = 16

func (m *Message) digest() []byte {
	sum := sha256.Sum256(m.Value)

	return sum[:digestBytes]
}

// DB is the inbox's pool: *sql.DB and *sqlx.DB satisfy it.
type DB interface {
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Logger is satisfied by *slog.Logger; args are key-value pairs.
type Logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// Metrics receives what the inbox counts. inboxprom implements it with Prometheus.
type Metrics interface {
	// Message counts a message by its result: done, duplicate, retry, dead, queued, or skipped (the table
	// refuses even its dead letter).
	Message(consumer, result string)
	DeadRows(n int)
	// QueuedOldestAge is the age of the oldest queued row of a consumer, 0 when it has none.
	QueuedOldestAge(consumer string, seconds float64)
	// TxDuration is how long the transaction of one InTx attempt lasted, from its begin to its end.
	TxDuration(consumer string, seconds float64)
	// TxSlotWait is how long an InTx message waited for a transaction slot (LimitTx).
	TxSlotWait(consumer string, seconds float64)
}

const (
	resultDone      = "done"
	resultDuplicate = "duplicate"
	resultRetry     = "retry"
	resultDead      = "dead"
	resultSkipped   = "skipped"
	resultQueued    = "queued"
)

// Options configure an inbox. Their zero values are the defaults.
type Options struct {
	Service string // required: the default lock key derives from it
	Table   string // default "processed_events"
	// Retention is how long a done row is kept, so a duplicate that comes later is processed again. Default 72h.
	Retention time.Duration
	// DeadRetention is how long a dead letter is kept after its last attempt; 0 keeps it until it is
	// replayed or deleted by hand.
	DeadRetention time.Duration
	// LockKey is the advisory lock of the worker: one pod at a time claims queued rows or deletes expired
	// ones. Default from Service and Table.
	LockKey int64
	Logger  Logger  // default slog.Default()
	Metrics Metrics // default none
}

const (
	defaultTable     = "processed_events"
	DefaultRetention = 72 * time.Hour
)

func (o Options) withDefaults() Options {
	if o.Table == "" {
		o.Table = defaultTable
	}
	if o.Retention == 0 {
		o.Retention = DefaultRetention
	}
	if o.LockKey == 0 {
		o.LockKey = lockKey("inbox:" + o.Service + ":" + o.Table)
	}

	return o
}

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

// maxIdentifier is NAMEDATALEN - 1: PostgreSQL cuts a longer name without an error.
const maxIdentifier = 63

var identifier = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

func (o Options) check() error {
	for _, name := range indexNames(o.Table) {
		if !identifier.MatchString(name) || len(name) > maxIdentifier {
			return fmt.Errorf("inbox: table name %q: want a lower-case identifier of at most %d bytes", name,
				maxIdentifier)
		}
	}
	if o.Retention < 0 || o.DeadRetention < 0 {
		return fmt.Errorf("inbox: Retention %s or DeadRetention %s is negative", o.Retention, o.DeadRetention)
	}

	return nil
}

func indexNames(table string) []string {
	return []string{table, table + "_queued", table + "_dead", table + "_received"}
}

// quoteIdent quotes a name that check accepted: a reserved word is a valid table name too.
func quoteIdent(name string) string {
	return `"` + name + `"`
}

type Inbox struct {
	db      DB
	opts    Options
	log     Logger
	metrics Metrics
	q       queries

	mu            sync.RWMutex
	registrations map[string]registration

	txSlots chan struct{}
}

// registration is how the worker runs a consumer's queued rows: deferred messages and replayed dead letters.
type registration struct {
	run    func(ctx context.Context, m *Message) error
	policy RetryPolicy
	queued bool
}

func (in *Inbox) register(consumer string, r registration) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.registrations[consumer] = r
}

func (in *Inbox) registration(consumer string) (registration, bool) {
	in.mu.RLock()
	defer in.mu.RUnlock()
	r, ok := in.registrations[consumer]

	return r, ok
}

func (in *Inbox) consumers() (all, deferred []string) {
	in.mu.RLock()
	defer in.mu.RUnlock()
	for name, r := range in.registrations {
		all = append(all, name)
		if r.queued {
			deferred = append(deferred, name)
		}
	}

	return all, deferred
}

type queries struct {
	check, seen, insert, deadRows, deleteExpired, deleteExpiredDead          string
	claim, heartbeat, finishDone, finishRetry, finishDead, replay, queuedAge string
}

func newQueries(table string) queries {
	r := strings.NewReplacer("{{table}}", quoteIdent(table))

	return queries{
		check: r.Replace(`SELECT 1 FROM {{table}} WHERE false UNION ALL SELECT 1`),
		seen: r.Replace(`SELECT EXISTS (SELECT 1 FROM {{table}}
			WHERE consumer = $1 AND event_id = $2 AND digest = $3)`),
		insert: r.Replace(`INSERT INTO {{table}} (consumer, event_id, digest, status, topic, kpartition, koffset, key,
			payload, headers, attempts, error) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			ON CONFLICT DO NOTHING`),
		deadRows: r.Replace(`SELECT count(*) FROM {{table}} WHERE status = $1`),
		// ctid batches: DELETE has no LIMIT, and a batch keeps each transaction and its locks short
		deleteExpired: r.Replace(`DELETE FROM {{table}} WHERE ctid = ANY(ARRAY(SELECT ctid FROM {{table}}
			WHERE status = $1 AND received_at < now() - $2 * interval '1 second' LIMIT $3))`),
		deleteExpiredDead: r.Replace(`DELETE FROM {{table}} WHERE ctid = ANY(ARRAY(SELECT ctid FROM {{table}}
			WHERE status = $1 AND updated_at < now() - $2 * interval '1 second' LIMIT $3))`),

		// The oldest free queued row of every key whose rows are all free: a key with a running row, or one
		// waiting for its retry, waits as a whole, so a key's rows run one at a time and in order.
		claim: r.Replace(`WITH free AS (
			SELECT DISTINCT ON (q.consumer, coalesce(q.key, q.event_id)) q.consumer, q.event_id, q.digest,
				q.received_at, q.koffset
			  FROM {{table}} q
			 WHERE q.status = 1 AND q.consumer IN (SELECT jsonb_array_elements_text($1::jsonb))
			   AND (q.lease_until IS NULL OR q.lease_until < now())
			   AND NOT EXISTS (SELECT 1 FROM {{table}} l WHERE l.status = 1 AND l.consumer = q.consumer
			       AND l.key = q.key AND l.lease_until >= now())
			 ORDER BY q.consumer, coalesce(q.key, q.event_id), q.received_at, q.koffset
		), picked AS (SELECT * FROM free ORDER BY received_at, koffset LIMIT $3)
		UPDATE {{table}} t SET lease_until = now() + $2 * interval '1 second', attempts = t.attempts + 1,
		       updated_at = now()
		  FROM picked WHERE t.consumer = picked.consumer AND t.event_id = picked.event_id
		   AND t.digest = picked.digest
		RETURNING t.consumer, t.event_id, t.digest, t.topic, t.kpartition, t.koffset, t.key, t.payload,
		       t.headers, t.attempts`),
		// a run whose lease was taken over (attempts moved on) changes nothing
		heartbeat: r.Replace(`UPDATE {{table}} SET lease_until = now() + $5 * interval '1 second'
			WHERE consumer = $1 AND event_id = $2 AND digest = $3 AND attempts = $4 AND status = 1`),
		finishDone: r.Replace(`UPDATE {{table}} SET status = 0, payload = NULL, headers = NULL, lease_until = NULL,
			error = NULL, updated_at = now()
			WHERE consumer = $1 AND event_id = $2 AND digest = $3 AND attempts = $4 AND status = 1`),
		finishRetry: r.Replace(`UPDATE {{table}} SET lease_until = now() + $5 * interval '1 second', error = $6,
			updated_at = now()
			WHERE consumer = $1 AND event_id = $2 AND digest = $3 AND attempts = $4 AND status = 1`),
		finishDead: r.Replace(`UPDATE {{table}} SET status = 2, lease_until = NULL, error = $5, updated_at = now()
			WHERE consumer = $1 AND event_id = $2 AND digest = $3 AND attempts = $4 AND status = 1`),
		replay: r.Replace(`UPDATE {{table}} SET status = 1, attempts = 0, lease_until = NULL, updated_at = now()
			WHERE consumer = $1 AND event_id = $2 AND status = 2`),
		queuedAge: r.Replace(`SELECT consumer, extract(epoch FROM now() - min(received_at))::float8 FROM {{table}}
			WHERE status = 1 GROUP BY consumer`),
	}
}

// New checks opts; it does not touch the database.
func New(db DB, opts Options) (*Inbox, error) {
	if db == nil {
		return nil, errors.New("inbox: no database")
	}
	if opts.Service == "" {
		return nil, errors.New("inbox: Options.Service is required")
	}
	opts = opts.withDefaults()
	if err := opts.check(); err != nil {
		return nil, err
	}

	in := &Inbox{db: db, opts: opts, log: opts.Logger, metrics: opts.Metrics, q: newQueries(opts.Table),
		registrations: make(map[string]registration)}
	if in.log == nil {
		in.log = slog.Default()
	}
	if in.metrics == nil {
		in.metrics = noMetrics{}
	}

	return in, nil
}

// undefinedTable is the SQLSTATE of a query on a table that does not exist.
const undefinedTable = "42P01"

// Check fails when the table of the migration is missing: a service should not consume then.
func (in *Inbox) Check(ctx context.Context) error {
	var one int
	err := in.db.QueryRowContext(ctx, in.q.check).Scan(&one)

	var coded interface{ SQLState() string }
	switch {
	case err == nil:
		return nil
	case errors.As(err, &coded) && coded.SQLState() == undefinedTable:
		return fmt.Errorf("inbox: table %s does not exist, its migration is not applied: %w", in.opts.Table, err)
	default:
		return fmt.Errorf("inbox: check of table %s: %w", in.opts.Table, err)
	}
}

type noMetrics struct{}

func (noMetrics) Message(string, string)          {}
func (noMetrics) DeadRows(int)                    {}
func (noMetrics) QueuedOldestAge(string, float64) {}
func (noMetrics) TxDuration(string, float64)      {}
func (noMetrics) TxSlotWait(string, float64)      {}
